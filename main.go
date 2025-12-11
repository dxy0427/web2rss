package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gorilla/feeds"
	"github.com/gorilla/mux"
	"github.com/patrickmn/go-cache"
)

var (
	c                *cache.Cache
	httpClient       *http.Client
	maxConcurrency   int
	retryMax         int
	retryInterval    time.Duration
	cacheLock        sync.Map
	userAgents       []string
	cstZone          *time.Location

	sizeRegex = regexp.MustCompile(`\s*\[[\d\.]+(?:GB|MB|TB|KB)\]$`)

	sizeExtractRegex = regexp.MustCompile(`(?i)(\d+(\.\d+)?)\s*([GMK]B)`)

	whitespaceRegex = regexp.MustCompile(`[\s\t\n\r]+`)

	episodeFullRegex   = regexp.MustCompile(`\[\s*全(\d+)集\s*\]`)
	episodeRangeRegex  = regexp.MustCompile(`\[\s*第(\d+)\s*-\s*(\d+)\s*集\s*\]`)
	episodeSingleRegex = regexp.MustCompile(`\[\s*第(\d+)\s*集\s*\]`)

	timeLayout = "2006-01-02 15:04:05"
)

func cleanString(str string) string {
	s := whitespaceRegex.ReplaceAllString(str, " ")
	return strings.TrimSpace(s)
}

func getEnvInt(key string, defaultValue int) int {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultValue
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return defaultValue
	}
	return val
}

func getEnvStr(key, defaultValue string) string {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}
	return val
}

func initHttpClient() {
	retryMax = getEnvInt("RETRY_MAX", 2)
	retryInterval = time.Duration(getEnvInt("RETRY_INTERVAL_SEC", 1)) * time.Second
	maxConcurrency = getEnvInt("MAX_CONCURRENCY", 10)

	var err error
	cstZone, err = time.LoadLocation("Asia/Shanghai")
	if err != nil {
		cstZone = time.FixedZone("CST", 8*3600)
	}

	httpClient = &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:       10,
			IdleConnTimeout:    30 * time.Second,
			DisableCompression: false,
		},
	}

	userAgents = []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Safari/537.36",
	}
}

func httpGetWithRetry(ctx context.Context, url string) (*http.Response, error) {
	var resp *http.Response
	var err error

	if len(userAgents) == 0 {
		userAgents = []string{"Mozilla/5.0 (Go-http-client)"}
	}

	for i := 0; i <= retryMax; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		req.Header.Set("User-Agent", userAgents[i%len(userAgents)])
		if strings.Contains(url, "/tdown/") {
			req.Header.Set("Referer", baseURL+"/")
		}

		resp, err = httpClient.Do(req)
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				return resp, nil
			}
			resp.Body.Close()
			err = fmt.Errorf("status code: %d", resp.StatusCode)
		}

		if i < retryMax {
			time.Sleep(retryInterval)
		}
	}
	return resp, fmt.Errorf("请求 %s 失败：%v", url, err)
}

const (
	baseURL   = "https://www.btbtla.com"
	detailURL = baseURL + "/detail/%s.html"
)

const (
	resTypeFull   = 0
	resTypeRange  = 1
	resTypeSingle = 2
	resTypeOther  = 9
)

type PageInfo struct {
	Title     string
	DetailURL string
	Resources []ResourceInfo
}

type ResourceInfo struct {
	ResourceTitle string
	Magnet        string
	Size          string
	Bytes         int64
	resType       int
	fullEpCount   int
	rangeStart    int
	rangeEnd      int
	singleEp      int
	titleRaw      string
	SeedTime      time.Time
	DetailPath    string
}

func parseSizeToBytes(sizeStr string) int64 {
	s := strings.TrimSpace(sizeStr)
	s = strings.ToUpper(s)
	matches := sizeExtractRegex.FindStringSubmatch(s)
	if len(matches) < 4 {
		return 0
	}
	numStr := matches[1]
	unit := matches[3]
	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0
	}
	var multiplier float64
	switch unit {
	case "TB":
		multiplier = 1024 * 1024 * 1024 * 1024
	case "GB":
		multiplier = 1024 * 1024 * 1024
	case "MB":
		multiplier = 1024 * 1024
	case "KB":
		multiplier = 1024
	default:
		multiplier = 1
	}
	return int64(val * multiplier)
}

func extractResourceType(title string) (int, int, int, int) {
	fullMatches := episodeFullRegex.FindStringSubmatch(title)
	if len(fullMatches) >= 2 {
		epCount, _ := strconv.Atoi(fullMatches[1])
		return resTypeFull, epCount, 0, 0
	}
	rangeMatches := episodeRangeRegex.FindStringSubmatch(title)
	if len(rangeMatches) >= 3 {
		start, _ := strconv.Atoi(rangeMatches[1])
		end, _ := strconv.Atoi(rangeMatches[2])
		return resTypeRange, 0, start, end
	}
	singleMatches := episodeSingleRegex.FindStringSubmatch(title)
	if len(singleMatches) >= 2 {
		ep, _ := strconv.Atoi(singleMatches[1])
		return resTypeSingle, 0, 0, ep
	}
	return resTypeOther, 0, 0, 0
}

func sortResources(resources []ResourceInfo) []ResourceInfo {
	if len(resources) <= 1 {
		return resources
	}
	sorted := make([]ResourceInfo, len(resources))
	copy(sorted, resources)

	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].resType != sorted[j].resType {
			return sorted[i].resType < sorted[j].resType
		}

		if sorted[i].resType == resTypeFull {
			return sorted[i].fullEpCount < sorted[j].fullEpCount
		}

		if sorted[i].resType == resTypeRange {
			if sorted[i].rangeStart != sorted[j].rangeStart {
				return sorted[i].rangeStart < sorted[j].rangeStart
			}
			return sorted[i].rangeEnd < sorted[j].rangeEnd
		}

		if sorted[i].resType == resTypeSingle {
			return sorted[i].singleEp < sorted[j].singleEp
		}

		return sorted[i].titleRaw < sorted[j].titleRaw
	})
	return sorted
}

func ScrapeBtMovie(ctx context.Context, resourceID string) (*PageInfo, error) {
	pageInfo := &PageInfo{
		DetailURL: fmt.Sprintf(detailURL, resourceID),
	}

	resp, err := httpGetWithRetry(ctx, pageInfo.DetailURL)
	if err != nil {
		return pageInfo, err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return pageInfo, err
	}

	pageInfo.Title = cleanString(doc.Find("h1.page-title").First().Text())
	links := doc.Find("a.module-row-text.copy")

	if links.Length() == 0 {
		return pageInfo, nil
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, maxConcurrency)

	for i := 0; i < links.Length(); i++ {
		if ctx.Err() != nil {
			break
		}
		s := links.Eq(i)
		path, exists := s.Attr("href")
		if !exists || strings.Contains(path, "/pdown/") {
			continue
		}

		rawTitle := cleanString(s.Find("h4").Text())
		cleanTitle := sizeRegex.ReplaceAllString(rawTitle, "")

		rType, fullEp, rStart, singleEp := extractResourceType(cleanTitle)
		rEnd := 0
		if rType == resTypeRange {
			matches := episodeRangeRegex.FindStringSubmatch(cleanTitle)
			if len(matches) >= 3 {
				rEnd, _ = strconv.Atoi(matches[2])
			}
		}

		wg.Add(1)
		go func(downPath, titleStr string, rt, fEp, rs, re, se int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			downResp, err := httpGetWithRetry(ctx, baseURL+downPath)
			if err != nil {
				return
			}
			defer downResp.Body.Close()

			downDoc, err := goquery.NewDocumentFromReader(downResp.Body)
			if err != nil {
				return
			}

			magnet := downDoc.Find("a[href^='magnet:']").First().AttrOr("href", "")
			if magnet == "" {
				magnet = downDoc.Find("a[data-magnet]").AttrOr("data-magnet", "")
			}
			if magnet == "" {
				return
			}

			sizeStr := "0B"
			sizeText := cleanString(downDoc.Find(".video-info-items").FilterFunction(func(i int, s *goquery.Selection) bool {
				return strings.Contains(s.Text(), "影片大小")
			}).Find(".video-info-item").Text())
			
			if sizeText != "" {
				sizeStr = sizeText
			}

			seedTime := time.Now()
			timeText := downDoc.Find(".video-info-items").FilterFunction(func(i int, s *goquery.Selection) bool {
				return strings.Contains(s.Text(), "种子时间")
			}).Find(".video-info-item").Text()
			
			timeText = cleanString(timeText)

			if timeText != "" {
				t, err := time.ParseInLocation(timeLayout, timeText, cstZone)
				if err == nil {
					seedTime = t
				} else {
					log.Printf("时间解析失败 [%s]: %v", timeText, err)
				}
			}

			bytes := parseSizeToBytes(sizeStr)

			mu.Lock()
			pageInfo.Resources = append(pageInfo.Resources, ResourceInfo{
				ResourceTitle: titleStr,
				Magnet:        magnet,
				Size:          sizeStr,
				Bytes:         bytes,
				resType:       rt,
				fullEpCount:   fEp,
				rangeStart:    rs,
				rangeEnd:      re,
				singleEp:      se,
				titleRaw:      titleStr,
				SeedTime:      seedTime,
				DetailPath:    downPath,
			})
			mu.Unlock()
		}(path, rawTitle, rType, fullEp, rStart, rEnd, singleEp)
	}

	wg.Wait()
	pageInfo.Resources = sortResources(pageInfo.Resources)
	return pageInfo, nil
}

func rssHandler(w http.ResponseWriter, r *http.Request) {
	timeoutSec := getEnvInt("SCRAPE_TIMEOUT_SEC", 60)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	vars := mux.Vars(r)
	resourceID := vars["resource_id"]
	if resourceID == "" {
		http.Error(w, "Resource ID is required", http.StatusBadRequest)
		return
	}

	cacheKey := "bt_rss_v4_" + resourceID
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")

	if rss, found := c.Get(cacheKey); found {
		w.WriteHeader(http.StatusOK)
		w.Write(rss.([]byte))
		return
	}

	lockKey := "lock_" + resourceID
	lockVal, _ := cacheLock.LoadOrStore(lockKey, &sync.Mutex{})
	lock := lockVal.(*sync.Mutex)
	lock.Lock()
	defer func() {
		lock.Unlock()
		cacheLock.Delete(lockKey)
	}()

	if rss, found := c.Get(cacheKey); found {
		w.WriteHeader(http.StatusOK)
		w.Write(rss.([]byte))
		return
	}

	type result struct {
		info *PageInfo
		err  error
	}
	resChan := make(chan result, 1)

	go func() {
		info, err := ScrapeBtMovie(ctx, resourceID)
		resChan <- result{info, err}
	}()

	var pageInfo *PageInfo
	var err error

	select {
	case res := <-resChan:
		pageInfo = res.info
		err = res.err
	case <-ctx.Done():
		err = ctx.Err()
	}

	if pageInfo == nil {
		pageInfo = &PageInfo{DetailURL: fmt.Sprintf(detailURL, resourceID), Title: "未知资源"}
	}

	feedTitle := pageInfo.Title
	if feedTitle == "" {
		feedTitle = fmt.Sprintf("BT资源 - %s", resourceID)
	}

	feed := &feeds.Feed{
		Title:       feedTitle,
		Link:        &feeds.Link{Href: pageInfo.DetailURL},
		Description: feedTitle,
		Created:     time.Now(),
	}

	if err != nil {
		feed.Items = append(feed.Items, &feeds.Item{
			Title:       fmt.Sprintf("抓取失败: %s", feedTitle),
			Description: fmt.Sprintf("错误: %v", err),
			Link:        &feeds.Link{Href: pageInfo.DetailURL},
			Created:     time.Now(),
		})
	} else {
		for _, res := range pageInfo.Resources {
			item := &feeds.Item{
				Title:       res.titleRaw,
				Link:        &feeds.Link{Href: baseURL + res.DetailPath},
				Description: fmt.Sprintf("%s [%s]", res.titleRaw, res.Size),
				Id:          res.titleRaw,
				Created:     res.SeedTime,
				Enclosure: &feeds.Enclosure{
					Url:    res.Magnet,
					Type:   "application/x-bittorrent",
					Length: strconv.FormatInt(res.Bytes, 10),
				},
			}
			feed.Items = append(feed.Items, item)
		}
	}

	rssStr, rssErr := feed.ToRss()
	if rssErr != nil {
		http.Error(w, "RSS Generation Error", http.StatusInternalServerError)
		return
	}

	rssStr = strings.Replace(rssStr, ` xmlns:content="http://purl.org/rss/1.0/modules/content/"`, "", 1)
	rssStr = strings.ReplaceAll(rssStr, `<guid>`, `<guid isPermaLink="false">`)

	rssBytes := []byte(rssStr)

	c.Set(cacheKey, rssBytes, func() time.Duration {
		if err != nil {
			return 5 * time.Minute
		}
		return cache.DefaultExpiration
	}())

	w.WriteHeader(http.StatusOK)
	w.Write(rssBytes)
}

func main() {
	initHttpClient()

	defaultExpirationMinutes := getEnvInt("CACHE_EXPIRATION_MINUTES", 15)
	c = cache.New(time.Duration(defaultExpirationMinutes)*time.Minute, time.Duration(defaultExpirationMinutes*2)*time.Minute)

	r := mux.NewRouter()
	r.HandleFunc("/rss/btmovie/{resource_id}", rssHandler)

	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("BT影视RSS服务运行中\n示例：/rss/btmovie/44851494"))
	})

	port := getEnvStr("PORT", "8888")
	log.Printf("服务启动在 :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}