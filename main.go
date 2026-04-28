package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
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
	idRegex          = regexp.MustCompile(`^\d+$`)

	sizeRegex         = regexp.MustCompile(`\s*\[[\d\.]+(?:GB|MB|TB|KB)\]$`)
	sizeExtractRegex  = regexp.MustCompile(`(?i)(\d+(\.\d+)?)\s*([GMK]B)`)
	whitespaceRegex   = regexp.MustCompile(`[\s\t\n\r]+`)
	episodeFullRegex  = regexp.MustCompile(`\[\s*全(\d+)集\s*\]`)
	episodeRangeRegex = regexp.MustCompile(`\[\s*第(\d+)\s*-\s*(\d+)\s*集\s*\]`)
	episodeSingleRegex = regexp.MustCompile(`\[\s*第(\d+)\s*集\s*\]`)
	timeLayout = "2006-01-02 15:04:05"
)

func cleanString(str string) string {
	str = whitespaceRegex.ReplaceAllString(str, " ")
	return strings.TrimSpace(str)
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
	maxConcurrency = getEnvInt("MAX_CONCURRENCY", math.MaxInt32)

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
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
	}
}

func httpGetWithRetry(ctx context.Context, url string) (*http.Response, error) {
	var resp *http.Response
	var err error

	for i := 0; i <= retryMax; i++ {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("上下文超时")
		}

		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		req.Header.Set("User-Agent", userAgents[i%len(userAgents)])
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

		resp, err = httpClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			return resp, nil
		}
		if resp != nil {
			resp.Body.Close()
		}

		if i < retryMax {
			time.Sleep(retryInterval * time.Duration(i+1))
		}
	}
	return nil, err
}

const (
	baseURL   = "https://www.btbtla.com"
	detailURL = baseURL + "/detail/%s.html"
)

// Mukaku (web5.mukaku.com) 配置
const (
	mukakuBaseURL  = "https://web5.mukaku.com"
	mukakuDetailAPI = mukakuBaseURL + "/prod/api/v1/getVideoDetail"
	mukakuAppID    = "83768d9ad4"
	mukakuIdentity = "23734adac0301bccdcb107c4aa21f96c"
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

// Mukaku API 响应结构
type MukakuResponse struct {
	Code    int            `json:"code"`
	Success bool           `json:"success"`
	Message string         `json:"message"`
	Data    *MukakuMovie   `json:"data"`
}

type MukakuMovie struct {
	ID       int                          `json:"id"`
	IDCode   string                       `json:"idcode"`
	Title    string                       `json:"title"`
	Image    string                       `json:"image"`
	Years    string                       `json:"years"`
	Alias    string                       `json:"alias"`
	Abstract string                       `json:"abstract"`
	Ecca     map[string][]MukakuSeed      `json:"ecca"`
	Arrare   []string                     `json:"arrare"`
}

type MukakuSeed struct {
	ID             int    `json:"id"`
	ZName          string `json:"zname"`
	ZSize          string `json:"zsize"`
	ZLink          string `json:"zlink"`
	Down           string `json:"down"`
	ZQXD           string `json:"zqxd"`
	EZT            string `json:"ezt"`
	DefinitionGroup string `json:"definition_group"`
	New            int    `json:"new"`
}

func searchNameToDetailPath(name string) string {
	searchURL := baseURL + "/search/" + url.PathEscape(name)
	log.Printf("开始搜索：%s", searchURL)
	resp, err := httpGetWithRetry(context.Background(), searchURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	doc, _ := goquery.NewDocumentFromReader(resp.Body)
	link := doc.Find(fmt.Sprintf(`.module-items .module-item .module-item-titlebox a[title="%s"]`, name)).AttrOr("href", "")
	if link != "" {
		log.Printf("搜索成功：%s -> %s", name, link)
	}
	return link
}

func parseSizeToBytes(sizeStr string) int64 {
	matches := sizeExtractRegex.FindStringSubmatch(strings.ToUpper(sizeStr))
	if len(matches) < 4 {
		return 0
	}
	val, _ := strconv.ParseFloat(matches[1], 64)
	switch matches[3] {
	case "TB":
		return int64(val * 1024 * 1024 * 1024 * 1024)
	case "GB":
		return int64(val * 1024 * 1024 * 1024)
	case "MB":
		return int64(val * 1024 * 1024)
	case "KB":
		return int64(val * 1024)
	}
	return 0
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
		return sorted[i].titleRaw < sorted[j].titleRaw
	})
	return sorted
}

func ScrapeBtMovie(ctx context.Context, param string) (*PageInfo, error) {
	pageInfo := &PageInfo{}

	if idRegex.MatchString(param) {
		pageInfo.DetailURL = fmt.Sprintf(detailURL, param)
	} else {
		path := searchNameToDetailPath(param)
		if path == "" {
			return nil, fmt.Errorf("not found")
		}
		pageInfo.DetailURL = baseURL + path
	}

	resp, err := httpGetWithRetry(ctx, pageInfo.DetailURL)
	if err != nil {
		return pageInfo, err
	}
	defer resp.Body.Close()

	doc, _ := goquery.NewDocumentFromReader(resp.Body)
	pageInfo.Title = cleanString(doc.Find("h1.page-title").First().Text())
	log.Printf("解析标题：%s", pageInfo.Title)

	links := doc.Find("div[name=download-list] .module-downlist.selected .module-row-one.active .module-row-info")
	log.Printf("找到 %d 个资源", links.Length())

	var wg sync.WaitGroup
	var mu sync.Mutex
	var failCount int
	sem := make(chan struct{}, maxConcurrency)

	links.Each(func(i int, s *goquery.Selection) {
		downPath := s.Find(".module-row-text").AttrOr("href", "")
		rawTitle := cleanString(s.Find(".module-row-title h4").Text())

		if downPath == "" || strings.Contains(downPath, "/pdown/") {
			return
		}

		rType, fullEp, rStart, singleEp := extractResourceType(rawTitle)
		rEnd := 0
		if rType == resTypeRange {
			matches := episodeRangeRegex.FindStringSubmatch(rawTitle)
			if len(matches) >= 3 {
				rEnd, _ = strconv.Atoi(matches[2])
			}
		}

		wg.Add(1)
		go func(downPath, titleStr string, rt, fEp, rs, re, se int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			downResp, err := httpGetWithRetry(ctx, baseURL+downPath)
			if downResp == nil || err != nil {
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}
			defer downResp.Body.Close()
			downDoc, _ := goquery.NewDocumentFromReader(downResp.Body)

			magnet := downDoc.Find(".btn-important").AttrOr("href", "")

			sizeStr := cleanString(downDoc.Find(".video-info-items:contains('影片大小') .video-info-item").Text())
			timeText := cleanString(downDoc.Find(".video-info-items:contains('种子时间') .video-info-item").Text())
			seedTime := time.Now()
			if t, err := time.ParseInLocation(timeLayout, timeText, cstZone); err == nil {
				seedTime = t
			}

			mu.Lock()
			pageInfo.Resources = append(pageInfo.Resources, ResourceInfo{
				ResourceTitle: titleStr,
				Magnet:        magnet,
				Size:          sizeStr,
				Bytes:         parseSizeToBytes(sizeStr),
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
		}(downPath, rawTitle, rType, fullEp, rStart, rEnd, singleEp)
	})

	wg.Wait()
	if failCount > 0 {
		log.Printf("抓取失败：%d 个资源获取失败", failCount)
	}
	pageInfo.Resources = sortResources(pageInfo.Resources)
	return pageInfo, nil
}

func ScrapeMukaku(ctx context.Context, idCode string) (*PageInfo, error) {
	apiURL := fmt.Sprintf("%s?id=%s&app_id=%s&identity=%s",
		mukakuDetailAPI, idCode, mukakuAppID, mukakuIdentity)

	log.Printf("请求 Mukaku API：%s", apiURL)
	resp, err := httpGetWithRetry(ctx, apiURL)
	if err != nil {
		return nil, fmt.Errorf("请求 Mukaku API 失败: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	var apiResp MukakuResponse
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("解析 JSON 失败: %w", err)
	}

	if !apiResp.Success || apiResp.Data == nil {
		return nil, fmt.Errorf("API 返回失败: %s (code=%d)", apiResp.Message, apiResp.Code)
	}

	movie := apiResp.Data
	pageInfo := &PageInfo{
		Title:     movie.Title,
		DetailURL: fmt.Sprintf("%s/mv/%s", mukakuBaseURL, idCode),
	}

	log.Printf("解析标题：%s, 分类数: %d", movie.Title, len(movie.Arrare))

	for _, seeds := range movie.Ecca {
		for _, seed := range seeds {
			seedTime := time.Now()
			if seed.EZT != "" {
				if t, err := time.ParseInLocation("2006-01-02", seed.EZT, cstZone); err == nil {
					seedTime = t
				}
			}

			rType, fullEp, rStart, singleEp := extractResourceType(seed.ZName)
			rEnd := 0
			if rType == resTypeRange {
				matches := episodeRangeRegex.FindStringSubmatch(seed.ZName)
				if len(matches) >= 3 {
					rEnd, _ = strconv.Atoi(matches[2])
				}
			}

			magnet := seed.ZLink
			if magnet == "" && seed.Down != "" {
				// 如果没有直接的 magnet，尝试用下载链接
				magnet = mukakuBaseURL + seed.Down
			}

			pageInfo.Resources = append(pageInfo.Resources, ResourceInfo{
				ResourceTitle: seed.ZName,
				Magnet:        magnet,
				Size:          seed.ZSize,
				Bytes:         parseSizeToBytes(seed.ZSize),
				resType:       rType,
				fullEpCount:   fullEp,
				rangeStart:    rStart,
				rangeEnd:      rEnd,
				singleEp:      singleEp,
				titleRaw:      seed.ZName,
				SeedTime:      seedTime,
				DetailPath:    fmt.Sprintf("/tr/%d.html", seed.ID),
			})
		}
	}

	pageInfo.Resources = sortResources(pageInfo.Resources)
	return pageInfo, nil
}

func rssHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	timeoutSec := getEnvInt("SCRAPE_TIMEOUT_SEC", 60)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	resourceID := mux.Vars(r)["resource_id"]
	log.Printf("收到请求：/rss/btmovie/%s", resourceID)
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	cacheKey := "bt_rss_v5_" + resourceID

	var rss interface{}
	var found bool
	if rss, found = c.Get(cacheKey); found {
		w.Write(rss.([]byte))
		log.Printf("响应完成，耗时：%s", time.Since(start))
		return
	}

	lockKey := "lock_" + resourceID
	lockVal, _ := cacheLock.LoadOrStore(lockKey, &sync.Mutex{})
	lock := lockVal.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	if rss, found = c.Get(cacheKey); found {
		w.Write(rss.([]byte))
		log.Printf("响应完成，耗时：%s", time.Since(start))
		return
	}

	pageInfo, err := ScrapeBtMovie(ctx, resourceID)
	if pageInfo == nil {
		pageInfo = &PageInfo{Title: "未知资源"}
	}

	feed := &feeds.Feed{
		Title:       pageInfo.Title,
		Link:        &feeds.Link{Href: pageInfo.DetailURL},
		Description: pageInfo.Title,
		Created:     time.Now(),
	}

	if err != nil {
		feed.Items = append(feed.Items, &feeds.Item{
			Title:       "抓取失败",
			Description: err.Error(),
			Created:     time.Now(),
		})
	} else {
		for _, res := range pageInfo.Resources {
			item := &feeds.Item{
				Title:       res.titleRaw,
				Link:        &feeds.Link{Href: baseURL + res.DetailPath},
				Description: fmt.Sprintf("%s [%s]", res.titleRaw, res.Size),
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

	rssStr, _ := feed.ToRss()
	rssBytes := []byte(rssStr)
	c.Set(cacheKey, rssBytes, time.Duration(getEnvInt("CACHE_EXPIRATION_MINUTES", 15))*time.Minute)
	w.Write(rssBytes)
	log.Printf("响应完成，耗时：%s", time.Since(start))
}

func mukakuRssHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	timeoutSec := getEnvInt("SCRAPE_TIMEOUT_SEC", 60)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	resourceID := mux.Vars(r)["resource_id"]
	log.Printf("收到请求：/rss/mukaku/%s", resourceID)
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	cacheKey := "mukaku_rss_v1_" + resourceID

	var rss []byte
	var found bool
	if rss, found = c.Get(cacheKey).([]byte); found {
		w.Write(rss)
		log.Printf("响应完成（缓存），耗时：%s", time.Since(start))
		return
	}

	lockKey := "mukaku_lock_" + resourceID
	lockVal, _ := cacheLock.LoadOrStore(lockKey, &sync.Mutex{})
	lock := lockVal.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	if rss, found = c.Get(cacheKey).([]byte); found {
		w.Write(rss)
		log.Printf("响应完成（缓存），耗时：%s", time.Since(start))
		return
	}

	pageInfo, err := ScrapeMukaku(ctx, resourceID)
	if pageInfo == nil {
		pageInfo = &PageInfo{Title: "未知资源"}
	}

	feed := &feeds.Feed{
		Title:       pageInfo.Title,
		Link:        &feeds.Link{Href: pageInfo.DetailURL},
		Description: pageInfo.Title,
		Created:     time.Now(),
	}

	if err != nil {
		feed.Items = append(feed.Items, &feeds.Item{
			Title:       "抓取失败",
			Description: err.Error(),
			Created:     time.Now(),
		})
	} else {
		for _, res := range pageInfo.Resources {
			item := &feeds.Item{
				Title:       res.titleRaw,
				Link:        &feeds.Link{Href: mukakuBaseURL + res.DetailPath},
				Description: fmt.Sprintf("%s [%s]", res.titleRaw, res.Size),
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

	rssStr, _ := feed.ToRss()
	rssBytes := []byte(rssStr)
	c.Set(cacheKey, rssBytes, time.Duration(getEnvInt("CACHE_EXPIRATION_MINUTES", 15))*time.Minute)
	w.Write(rssBytes)
	log.Printf("响应完成，耗时：%s", time.Since(start))
}

func main() {
	initHttpClient()
	exp := time.Duration(getEnvInt("CACHE_EXPIRATION_MINUTES", 15)) * time.Minute
	c = cache.New(exp, exp*2)

	r := mux.NewRouter()
	r.HandleFunc("/rss/btmovie/{resource_id}", rssHandler)
	r.HandleFunc("/rss/mukaku/{resource_id}", mukakuRssHandler)

	port := getEnvStr("PORT", "8888")
	log.Printf("web2rss 启动，端口：%s", port)
	log.Printf("btbtla 路由: /rss/btmovie/{resource_id}")
	log.Printf("mukaku 路由: /rss/mukaku/{idcode}")
	log.Fatal(http.ListenAndServe(":"+port, r))
}