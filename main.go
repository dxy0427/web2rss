package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
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
	c              *cache.Cache
	httpClient     *http.Client
	maxConcurrency int
	retryMax       int
	retryInterval  time.Duration
	cacheLock      sync.Map
	userAgents     []string
)

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
	maxConcurrency = getEnvInt("MAX_CONCURRENCY", 3)

	httpClient = &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:    10,
			IdleConnTimeout: 30 * time.Second,
			DisableCompression: false,
		},
	}

	userAgents = []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Edge/128.0.0.0 Safari/537.36",
	}
}

func httpGetWithRetry(url string) (*http.Response, error) {
	var resp *http.Response
	var err error
	for i := 0; i <= retryMax; i++ {
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("User-Agent", userAgents[i%len(userAgents)])
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

		resp, err = httpClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			return resp, nil
		}
		if i < retryMax {
			waitTime := retryInterval * time.Duration(i+1)
			log.Printf("请求 %s 失败（%v），%v后重试（第%d次）", url, err, waitTime, i+1)
			time.Sleep(waitTime)
		}
	}
	return resp, fmt.Errorf("请求 %s 重试%d次后仍失败：%v", url, retryMax, err)
}

const (
	siteName  = "BT影视"
	baseURL   = "https://www.btbtla.com"
	detailURL = baseURL + "/detail/%s.html"
)

type ResourceInfo struct {
	ResourceTitle string
	Magnet        string
	Size          string
}

type PageInfo struct {
	Title     string
	Type      string
	Year      string
	DetailURL string
	Resources []ResourceInfo
}

func ScrapeBtMovie(resourceID string) (*PageInfo, error) {
	pageInfo := &PageInfo{DetailURL: fmt.Sprintf(detailURL, resourceID)}
	log.Printf("开始抓取详情页: %s", pageInfo.DetailURL)

	resp, err := httpGetWithRetry(pageInfo.DetailURL)
	if err != nil {
		return nil, fmt.Errorf("请求详情页失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("详情页返回非200状态码: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("解析详情页HTML失败: %w", err)
	}
	log.Println("详情页抓取并解析成功")

	pageInfo.Title = strings.TrimSpace(doc.Find("h1.page-title").First().Text())
	pageInfo.Year = strings.TrimSpace(doc.Find("div.video-info-aux a.tag-link").Last().Text())
	var types []string
	doc.Find("div.video-info-aux div.tag-link a[href*='/']").Each(func(i int, s *goquery.Selection) {
		types = append(types, strings.TrimSpace(s.Text()))
	})
	pageInfo.Type = strings.Join(types, " / ")

	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, maxConcurrency)
	failCount := 0
	filterCount := 0 // 原skipCount改为filterCount（过滤数）
	totalLinks := doc.Find("a.module-row-text.copy").Length()
	log.Printf("资源 %s 共找到 %d 个下载链接，并发数限制为 %d", resourceID, totalLinks, maxConcurrency)
	if totalLinks == 0 {
		return pageInfo, nil // 无链接时不返回错误，避免错误日志
	}

	links := doc.Find("a.module-row-text.copy")
	for i := 0; i < links.Length(); i++ {
		s := links.Eq(i)
		downloadPath, exists := s.Attr("href")
		if !exists {
			continue
		}

		// 网盘链接计入“过滤数”，不打印单条日志
		if strings.Contains(downloadPath, "/pdown/") {
			mu.Lock()
			filterCount++
			mu.Unlock()
			continue
		}

		resourceTitle := strings.TrimSpace(s.Find("h4").Text())
		re := regexp.MustCompile(`\s*\[[\d\.]+(?:GB|MB|TB)\]$`)
		resourceTitle = re.ReplaceAllString(resourceTitle, "")
		if resourceTitle == "" {
			resourceTitle = fmt.Sprintf("未知版本（%d）", i+1)
		}

		wg.Add(1)
		go func(path, title string) {
			sem <- struct{}{}
			defer func() {
				wg.Done()
				<-sem
			}()

			downloadURL := baseURL + path
			respDown, err := httpGetWithRetry(downloadURL)
			if err != nil {
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}
			defer respDown.Body.Close()
			if respDown.StatusCode != http.StatusOK {
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}

			docDown, err := goquery.NewDocumentFromReader(respDown.Body)
			if err != nil {
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}

			var magnetLink string
			magnetLink = docDown.Find("div.video-info-footer a[href^='magnet:']").AttrOr("href", "")
			if magnetLink == "" {
				magnetLink = docDown.Find("div.download-container a[href^='magnet:']").AttrOr("href", "")
			}
			if magnetLink == "" {
				magnetLink = docDown.Find("a[data-magnet]").AttrOr("data-magnet", "")
			}
			if magnetLink == "" {
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}
			magnetLink = strings.TrimSpace(magnetLink)

			size := "未知大小"
			sizeText := docDown.Find("div.video-info-items:contains('影片大小') .video-info-item").Text()
			if sizeText != "" {
				size = strings.TrimSpace(sizeText)
			}

			mu.Lock()
			pageInfo.Resources = append(pageInfo.Resources, ResourceInfo{
				ResourceTitle: title,
				Magnet:        magnetLink,
				Size:          size,
			})
			mu.Unlock()
		}(downloadPath, resourceTitle)
	}

	wg.Wait()
	close(sem)

	// 汇总日志：“跳过”改为“过滤”，且不返回错误（避免错误日志）
	successCount := totalLinks - failCount - filterCount
	log.Printf("资源 %s 抓取完成：成功%d个，失败%d个，过滤%d个", resourceID, successCount, failCount, filterCount)
	return pageInfo, nil
}

func rssHandler(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	vars := mux.Vars(r)
	resourceID := vars["resource_id"]
	if resourceID == "" {
		http.Error(w, "Resource ID is required", http.StatusBadRequest)
		return
	}
	defer func() {
		log.Printf("资源 %s 请求处理完成，总耗时：%v", resourceID, time.Since(startTime))
	}()
	log.Printf("接收到请求 [BT影视], Resource ID: %s", resourceID)

	cacheKey := "bt_rss_" + resourceID
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")

	if rss, found := c.Get(cacheKey); found {
		log.Printf("缓存命中, Key: %s", cacheKey)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(rss.([]byte))
		return
	}
	log.Printf("缓存未命中, Key: %s", cacheKey)

	lockKey := "lock_" + resourceID
	lockVal, _ := cacheLock.LoadOrStore(lockKey, &sync.Mutex{})
	lock := lockVal.(*sync.Mutex)
	lock.Lock()
	defer func() {
		lock.Unlock()
		cacheLock.Delete(lockKey)
	}()

	if rss, found := c.Get(cacheKey); found {
		log.Printf("二次缓存命中, Key: %s", cacheKey)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(rss.([]byte))
		return
	}

	now := time.Now()
	feed := &feeds.Feed{
		Title:       fmt.Sprintf("%s RSS Feed - %s", siteName, resourceID),
		Link:        &feeds.Link{Href: baseURL},
		Description: "自动生成的BT影视资源RSS（已过滤网盘链接）",
		Author:      &feeds.Author{Name: "Go RSS Generator"},
		Created:     now,
	}

	type result struct {
		pageInfo *PageInfo
		err      error
	}
	resultChan := make(chan result, 1)
	go func() {
		pageInfo, err := ScrapeBtMovie(resourceID)
		resultChan <- result{pageInfo, err}
	}()

	var rssBytes []byte
	select {
	case res := <-resultChan:
		pageInfo, err := res.pageInfo, res.err
		// 只处理“详情页请求/解析失败”的错误，去掉“所有链接过滤”的错误日志
		if err != nil {
			log.Printf("抓取或解析过程中发生错误: %v", err)
			item := &feeds.Item{
				Id:          fmt.Sprintf(detailURL, resourceID),
				Title:       fmt.Sprintf("[%s] %s (抓取失败)", siteName, pageInfo.Title),
				Link:        &feeds.Link{Href: fmt.Sprintf(detailURL, resourceID)},
				Description: fmt.Sprintf("错误原因: %v | 资源类型: %s | 年份: %s", err, pageInfo.Type, pageInfo.Year),
				Created:     now,
			}
			feed.Items = append(feed.Items, item)
		} else {
			log.Printf("成功抓取 %d 个资源，生成RSS条目", len(pageInfo.Resources))
			for _, resource := range pageInfo.Resources {
				item := &feeds.Item{
					Id:          resource.Magnet,
					Title:       fmt.Sprintf("[%s] %s - %s", siteName, pageInfo.Title, resource.ResourceTitle),
					Link:        &feeds.Link{Href: pageInfo.DetailURL},
					Description: fmt.Sprintf("资源大小: %s | 磁力链接: %s", resource.Size, resource.Magnet),
					Created:     now,
					Enclosure: &feeds.Enclosure{
						Url:    resource.Magnet,
						Type:   "application/x-bittorrent",
						Length: "0",
					},
				}
				feed.Items = append(feed.Items, item)
			}
		}
		rssStr, _ := feed.ToRss()
		rssBytes = []byte(rssStr)
		// 失败/成功结果均存入缓存，统一逻辑
		c.Set(cacheKey, rssBytes, func() time.Duration {
			if err != nil {
				return 5 * time.Minute // 错误结果缓存5分钟
			}
			return cache.DefaultExpiration // 成功结果用默认缓存时间
		}())
		log.Printf("已将结果存入缓存, Key: %s", cacheKey)
	case <-time.After(50 * time.Second):
		log.Printf("资源 %s 抓取超时（50秒）", resourceID)
		item := &feeds.Item{
			Id:          fmt.Sprintf(detailURL, resourceID),
			Title:       fmt.Sprintf("[%s] 资源 %s 抓取超时", siteName, resourceID),
			Link:        &feeds.Link{Href: fmt.Sprintf(detailURL, resourceID)},
			Description: "错误: 抓取超时，请5分钟后重试",
			Created:     now,
		}
		feed.Items = append(feed.Items, item)
		rssStr, _ := feed.ToRss()
		rssBytes = []byte(rssStr)
		c.Set(cacheKey, rssBytes, 5*time.Minute)
		log.Printf("已将超时结果存入缓存, Key: %s", cacheKey)
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rssBytes)
	log.Printf("成功响应请求 [BT影视], Resource ID: %s", resourceID)
}

func main() {
	initHttpClient()

	defaultExpirationMinutes := getEnvInt("CACHE_EXPIRATION_MINUTES", 15)
	expirationDuration := time.Duration(defaultExpirationMinutes) * time.Minute
	cleanupInterval := expirationDuration * 2
	c = cache.New(expirationDuration, cleanupInterval)
	log.Printf("缓存服务初始化成功，缓存时间：%d分钟，清理间隔：%d分钟", defaultExpirationMinutes, cleanupInterval/time.Minute)

	r := mux.NewRouter()
	r.HandleFunc("/rss/btmovie/{resource_id}", rssHandler)
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("BT影视RSS服务运行中 | 使用方式：/rss/btmovie/[资源ID]"))
	})

	port := getEnvStr("PORT", "8888")
	log.Printf("服务启动，监听端口：%s | 测试地址：http://localhost:%s/rss/btmovie/44851494", port, port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}
