package main

import (
	"context"
	"fmt"
	"log"
	"math"
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
		log.Printf("警告：环境变量 %s 无效（%s），使用默认值 %d", key, valStr, defaultValue)
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
		log.Printf("加载时区失败，使用固定时区CST：%v", err)
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
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Edge/128.0.0.0 Safari/537.36",
	}
	log.Println("HTTP客户端初始化完成，User-Agents列表已加载")
}

func httpGetWithRetry(ctx context.Context, url string) (*http.Response, error) {
	var resp *http.Response
	var err error

	if len(userAgents) == 0 {
		userAgents = []string{"Mozilla/5.0 (Go-http-client)"}
	}

	for i := 0; i <= retryMax; i++ {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("上下文已取消/超时：%w", ctx.Err())
		}

		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		req.Header.Set("User-Agent", userAgents[i%len(userAgents)])
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		if strings.Contains(url, "/tdown/") {
			req.Header.Set("Referer", baseURL+"/")
		}

		resp, err = httpClient.Do(req)
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				return resp, nil
			}
			resp.Body.Close()
			err = fmt.Errorf("状态码：%d", resp.StatusCode)
		}

		if i < retryMax {
			waitTime := retryInterval * time.Duration(i+1)
			log.Printf("请求 %s 失败（%v），%v后重试（第%d次）", url, err, waitTime, i+1)
			select {
			case <-time.After(waitTime):
				continue
			case <-ctx.Done():
				return nil, fmt.Errorf("重试等待时上下文取消：%w", ctx.Err())
			}
		}
	}
	return resp, fmt.Errorf("请求 %s 重试%d次后仍失败：%w", url, retryMax, err)
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
		log.Printf("大小字符串解析失败（格式不匹配）：%s", sizeStr)
		return 0
	}
	numStr := matches[1]
	unit := matches[3]
	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		log.Printf("大小数值解析失败：%s，错误：%v", numStr, err)
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
		log.Printf("未知大小单位：%s，默认按1计算", unit)
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
	log.Printf("开始抓取详情页: %s", pageInfo.DetailURL)

	resp, err := httpGetWithRetry(ctx, pageInfo.DetailURL)
	if err != nil {
		return pageInfo, fmt.Errorf("请求详情页失败: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return pageInfo, fmt.Errorf("解析详情页HTML失败: %w", err)
	}
	log.Println("详情页抓取并解析成功")

	pageInfo.Title = cleanString(doc.Find("h1.page-title").First().Text())
	links := doc.Find("a.module-row-text.copy")
	totalLinks := links.Length()
	log.Printf("资源 %s 共找到 %d 个下载链接，并发数限制为 %d", resourceID, totalLinks, maxConcurrency)

	if totalLinks == 0 {
		return pageInfo, nil
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, maxConcurrency)
	failCount := 0
	filterCount := 0

	for i := 0; i < links.Length(); i++ {
		if ctx.Err() != nil {
			log.Printf("上下文已取消/超时，停止抓取资源 %s", resourceID)
			break
		}
		s := links.Eq(i)
		path, exists := s.Attr("href")
		if !exists {
			continue
		}

		if strings.Contains(path, "/pdown/") {
			mu.Lock()
			filterCount++
			mu.Unlock()
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
				log.Printf("资源下载链接抓取上下文取消：%s", downPath)
				return
			}

			downResp, err := httpGetWithRetry(ctx, baseURL+downPath)
			if err != nil {
				log.Printf("请求下载页 %s 失败：%v", baseURL+downPath, err)
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}
			defer downResp.Body.Close()

			downDoc, err := goquery.NewDocumentFromReader(downResp.Body)
			if err != nil {
				log.Printf("解析下载页 %s 失败：%v", baseURL+downPath, err)
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}

			var magnet string
			// 尝试多个选择器获取磁力链接
			magnet = downDoc.Find("a[href^='magnet:']").First().AttrOr("href", "")
			if magnet == "" {
				magnet = downDoc.Find("div.video-info-footer a[href^='magnet:']").AttrOr("href", "")
			}
			if magnet == "" {
				magnet = downDoc.Find("div.download-container a[href^='magnet:']").AttrOr("href", "")
			}
			if magnet == "" {
				magnet = downDoc.Find("a[data-magnet]").AttrOr("data-magnet", "")
			}
			if magnet == "" {
				log.Printf("下载页 %s 未找到磁力链接", baseURL+downPath)
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}
			magnet = strings.TrimSpace(magnet)

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
	successCount := totalLinks - failCount - filterCount
	log.Printf("资源 %s 抓取完成：成功%d个，失败%d个，过滤%d个", resourceID, successCount, failCount, filterCount)

	pageInfo.Resources = sortResources(pageInfo.Resources)
	log.Printf("资源 %s 排序完成，共 %d 个有效资源", resourceID, len(pageInfo.Resources))
	return pageInfo, nil
}

func rssHandler(w http.ResponseWriter, r *http.Request) {
	timeoutSec := getEnvInt("SCRAPE_TIMEOUT_SEC", 60)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	startTime := time.Now()
	vars := mux.Vars(r)
	resourceID := vars["resource_id"]
	if resourceID == "" {
		http.Error(w, "Resource ID is required", http.StatusBadRequest)
		log.Println("收到无效请求：Resource ID为空")
		return
	}
	defer func() {
		log.Printf("资源 %s 请求处理完成，总耗时：%v", resourceID, time.Since(startTime))
	}()
	log.Printf("接收到请求，Resource ID: %s", resourceID)

	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	cacheKey := "bt_rss_v4_" + resourceID

	if rss, found := c.Get(cacheKey); found {
		log.Printf("缓存命中, Key: %s", cacheKey)
		w.WriteHeader(http.StatusOK)
		w.Write(rss.([]byte))
		return
	}
	log.Printf("缓存未命中, Key: %s，开始抓取", cacheKey)

	lockKey := "lock_" + resourceID
	lockVal, _ := cacheLock.LoadOrStore(lockKey, &sync.Mutex{})
	lock := lockVal.(*sync.Mutex)
	lock.Lock()
	defer func() {
		lock.Unlock()
		cacheLock.Delete(lockKey)
		log.Printf("释放资源 %s 缓存锁", resourceID)
	}()

	if rss, found := c.Get(cacheKey); found {
		log.Printf("二次缓存命中, Key: %s", cacheKey)
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
		err = fmt.Errorf("抓取超时（%d秒）", timeoutSec)
		log.Printf("资源 %s %v", resourceID, err)
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
		Author:      &feeds.Author{Name: "Go RSS Generator"},
		Created:     time.Now(),
	}

	if err != nil {
		log.Printf("抓取过程中发生错误: %v", err)
		reason := fmt.Sprintf("%v", err)
		if ctx.Err() == context.DeadlineExceeded {
			reason = "抓取时间过长，已触发超时限制"
		}

		item := &feeds.Item{
			Title:       fmt.Sprintf("抓取失败: %s", feedTitle),
			Description: fmt.Sprintf("错误: %s", reason),
			Link:        &feeds.Link{Href: pageInfo.DetailURL},
			Created:     time.Now(),
			Id:          pageInfo.DetailURL,
		}
		feed.Items = append(feed.Items, item)
	} else {
		log.Printf("成功抓取 %d 个资源，生成RSS条目", len(pageInfo.Resources))
		for _, res := range pageInfo.Resources {
			item := &feeds.Item{
				Title:       res.titleRaw,
				Link:        &feeds.Link{Href: baseURL + res.DetailPath},
				Description: fmt.Sprintf("%s [%s]", res.titleRaw, res.Size),
				Id:          res.Magnet,
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
		log.Printf("生成RSS失败：%v", rssErr)
		return
	}

	rssStr = strings.Replace(rssStr, ` xmlns:content="http://purl.org/rss/1.0/modules/content/"`, "", 1)
	rssStr = strings.ReplaceAll(rssStr, `<guid>`, `<guid isPermaLink="false">`)

	rssBytes := []byte(rssStr)
	cacheDuration := func() time.Duration {
		if err != nil {
			return 5 * time.Minute
		}
		return cache.DefaultExpiration
	}()
	c.Set(cacheKey, rssBytes, cacheDuration)
	log.Printf("已将结果存入缓存, Key: %s，缓存时长：%v", cacheKey, cacheDuration)

	w.WriteHeader(http.StatusOK)
	w.Write(rssBytes)
	log.Printf("成功响应请求，Resource ID: %s", resourceID)
}

func main() {
	log.Println("初始化BT影视RSS服务...")
	initHttpClient()

	defaultExpirationMinutes := getEnvInt("CACHE_EXPIRATION_MINUTES", 15)
	expirationDuration := time.Duration(defaultExpirationMinutes) * time.Minute
	cleanupInterval := expirationDuration * 2
	c = cache.New(expirationDuration, cleanupInterval)
	log.Printf("缓存服务初始化成功，缓存时间：%d分钟，清理间隔：%d分钟", defaultExpirationMinutes, cleanupInterval/time.Minute)

	r := mux.NewRouter()
	r.HandleFunc("/rss/btmovie/{resource_id}", rssHandler)

	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("BT影视RSS服务运行中\n使用方式：/rss/btmovie/[资源ID]\n示例：/rss/btmovie/44851494"))
		log.Println("根路由被访问，返回服务说明")
	})

	port := getEnvStr("PORT", "8888")
	log.Printf("服务启动在 :%s，测试地址：http://localhost:%s/rss/btmovie/44851494", port, port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}