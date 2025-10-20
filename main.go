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

// 全局变量：新增【排除关键词】配置（对应Python的resources_exclude_keywords）
var (
	c              *cache.Cache
	httpClient     *http.Client
	maxConcurrency int
	retryMax       int
	retryInterval  time.Duration
	cacheLock      sync.Map
	userAgents     []string
	excludeKeywords []string // 新增：存储需要排除的关键词列表
)

// 工具函数：读取环境变量（不变）
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

// 初始化HTTP客户端：新增【读取排除关键词】（从环境变量读取，逗号分割）
func initHttpClient() {
	retryMax = getEnvInt("RETRY_MAX", 2)
	retryInterval = time.Duration(getEnvInt("RETRY_INTERVAL_SEC", 1)) * time.Second
	maxConcurrency = getEnvInt("MAX_CONCURRENCY", 3)

	// 新增：读取排除关键词（环境变量格式：KEYWORD1,KEYWORD2,KEYWORD3）
	excludeKeywordsStr := getEnvStr("RESOURCES_EXCLUDE_KEYWORDS", "")
	if excludeKeywordsStr != "" {
		excludeKeywords = strings.Split(excludeKeywordsStr, ",")
		// 清理关键词前后空格
		for i, kw := range excludeKeywords {
			excludeKeywords[i] = strings.TrimSpace(kw)
		}
		log.Printf("加载排除关键词：%v", excludeKeywords)
	}

	// HTTP客户端配置（不变）
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

// 带重试的HTTP请求（不变）
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

// 常量和结构体（不变）
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

// ScrapeBtMovie：核心修改处——添加2个过滤逻辑
func ScrapeBtMovie(resourceID string) (*PageInfo, error) {
	pageInfo := &PageInfo{DetailURL: fmt.Sprintf(detailURL, resourceID)}
	log.Printf("开始抓取详情页: %s", pageInfo.DetailURL)

	// 1. 抓取详情页（不变）
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

	// 提取基础信息（不变）
	pageInfo.Title = strings.TrimSpace(doc.Find("h1.page-title").First().Text())
	pageInfo.Year = strings.TrimSpace(doc.Find("div.video-info-aux a.tag-link").Last().Text())

	var types []string
	doc.Find("div.video-info-aux div.tag-link a[href*='/']").Each(func(i int, s *goquery.Selection) {
		types = append(types, strings.TrimSpace(s.Text()))
	})
	pageInfo.Type = strings.Join(types, " / ")

	// 2. 抓取下载页：核心过滤逻辑在这里补充
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, maxConcurrency)
	failCount := 0
	totalLinks := doc.Find("a.module-row-text.copy").Length()

	log.Printf("资源 %s 共找到 %d 个下载链接，并发数限制为 %d", resourceID, totalLinks, maxConcurrency)
	if totalLinks == 0 {
		return pageInfo, fmt.Errorf("未找到任何下载链接")
	}

	doc.Find("a.module-row-text.copy").Each(func(i int, s *goquery.Selection) {
		downloadPath, exists := s.Attr("href")
		if !exists {
			return
		}

		// 提取并清理资源标题（不变）
		resourceTitle := strings.TrimSpace(s.Find("h4").Text())
		re := regexp.MustCompile(`\s*\[[\d\.]+(?:GB|MB|TB)\]$`)
		resourceTitle = re.ReplaceAllString(resourceTitle, "")
		if resourceTitle == "" {
			resourceTitle = fmt.Sprintf("未知版本（%d）", i+1)
		}

		// 【过滤逻辑1：排除含关键词的资源】（对应Python的exclude_keywords）
		if len(excludeKeywords) > 0 {
			hasExcludeKw := false
			for _, kw := range excludeKeywords {
				if kw != "" && strings.Contains(resourceTitle, kw) {
					hasExcludeKw = true
					break
				}
			}
			if hasExcludeKw {
				log.Printf("资源 %s 第%d个链接含排除关键词，跳过：%s", resourceID, i+1, resourceTitle)
				return // 直接跳过这个资源，不启动goroutine
			}
		}

		wg.Add(1)
		go func(path, title string, index int) {
			sem <- struct{}{}
			defer func() {
				wg.Done()
				<-sem
			}()

			downloadURL := baseURL + path
			log.Printf("开始抓取资源 %s 第%d个下载页：%s", resourceID, index, downloadURL)

			// 抓取下载页（不变）
			respDown, err := httpGetWithRetry(downloadURL)
			if err != nil {
				log.Printf("资源 %s 第%d个下载页抓取失败：%v", resourceID, index, err)
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}
			defer respDown.Body.Close()

			if respDown.StatusCode != http.StatusOK {
				log.Printf("资源 %s 第%d个下载页返回非200状态码：%d", resourceID, index, respDown.StatusCode)
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}

			docDown, err := goquery.NewDocumentFromReader(respDown.Body)
			if err != nil {
				log.Printf("资源 %s 第%d个下载页解析失败：%v", resourceID, index, err)
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}

			// 【过滤逻辑2：排除网盘资源】（对应Python的“网盘”tab过滤）
			// 检查当前下载页是否属于“网盘”类型（通过页面标签或标题判断）
			// 方式1：检查页面是否有“网盘”标签
			hasPanTag := false
			docDown.Find(".module-tab-item.downtab-item span[data-dropdown-value]").Each(func(j int, tabSpan *goquery.Selection) {
				tabLabel := strings.TrimSpace(tabSpan.AttrOr("data-dropdown-value", ""))
				if strings.Contains(tabLabel, "网盘") {
					hasPanTag = true
				}
			})
			// 方式2：检查资源标题是否含“网盘”（双重保险）
			if hasPanTag || strings.Contains(title, "网盘") {
				log.Printf("资源 %s 第%d个下载页是网盘资源，跳过：%s", resourceID, index, downloadURL)
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}

			// 提取磁力链接（不变）
			var magnetLink string
			magnetLink = docDown.Find("div.video-info-footer a[href^='magnet:']").AttrOr("href", "")
			if magnetLink == "" {
				magnetLink = docDown.Find("div.download-container a[href^='magnet:']").AttrOr("href", "")
			}
			if magnetLink == "" {
				magnetLink = docDown.Find("a[data-magnet]").AttrOr("data-magnet", "")
			}

			if magnetLink == "" {
				log.Printf("警告：资源 %s 第%d个下载页未找到磁力链接", resourceID, index)
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}
			magnetLink = strings.TrimSpace(magnetLink)

			// 提取大小（不变）
			size := "未知大小"
			sizeText := docDown.Find("div.video-info-items:contains('影片大小') .video-info-item").Text()
			if sizeText != "" {
				size = strings.TrimSpace(sizeText)
			}

			// 追加资源（不变）
			mu.Lock()
			pageInfo.Resources = append(pageInfo.Resources, ResourceInfo{
				ResourceTitle: title,
				Magnet:        magnetLink,
				Size:          size,
			})
			mu.Unlock()
			log.Printf("资源 %s 第%d个下载页抓取成功（磁力链接：%s...）", resourceID, index, magnetLink[:50])
		}(downloadPath, resourceTitle, i+1)
	})

	wg.Wait()
	close(sem)

	successCount := totalLinks - failCount
	log.Printf("资源 %s 抓取完成：成功%d个，失败%d个", resourceID, successCount, failCount)
	if successCount == 0 {
		return pageInfo, fmt.Errorf("所有下载页抓取失败（共%d个）", totalLinks)
	}
	return pageInfo, nil
}

// rssHandler（不变，仅调用ScrapeBtMovie）
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

	// 读缓存（不变）
	if rss, found := c.Get(cacheKey); found {
		log.Printf("缓存命中, Key: %s", cacheKey)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(rss.([]byte))
		return
	}
	log.Printf("缓存未命中, Key: %s", cacheKey)

	// 防缓存击穿锁（不变）
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

	// 调用抓取函数（已含过滤逻辑）
	now := time.Now()
	feed := &feeds.Feed{
		Title:       fmt.Sprintf("%s RSS Feed - %s", siteName, resourceID),
		Link:        &feeds.Link{Href: "https://www.btbtla.com/"},
		Description: "自动生成的BT影视资源RSS（已过滤网盘和排除关键词）", // 新增：说明过滤功能
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

	select {
	case res := <-resultChan:
		pageInfo, err := res.pageInfo, res.err
		if err != nil {
			log.Printf("抓取或解析过程中发生错误: %v", err)
			item := &feeds.Item{
				Id:          fmt.Sprintf(detailURL, resourceID),
				Title:       fmt.Sprintf("[%s] %s (部分磁力链接获取失败)", siteName, pageInfo.Title),
				Link:        &feeds.Link{Href: fmt.Sprintf(detailURL, resourceID)},
				Description: fmt.Sprintf("类型: %s | 年份: %s | 错误: %v", pageInfo.Type, pageInfo.Year, err),
				Created:     now,
			}
			feed.Items = append(feed.Items, item)
		} else {
			log.Printf("成功抓取 %d 个资源（已过滤）, 开始生成RSS条目", len(pageInfo.Resources))
			for _, resource := range pageInfo.Resources {
				item := &feeds.Item{
					Title:       fmt.Sprintf("[%s] %s - %s", siteName, pageInfo.Title, resource.ResourceTitle),
					Link:        &feeds.Link{Href: pageInfo.DetailURL},
					Description: fmt.Sprintf("类型: %s | 年份: %s | 大小: %s", pageInfo.Type, pageInfo.Year, resource.Size),
					Id:          resource.Magnet,
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
	case <-time.After(50 * time.Second):
		log.Printf("资源 %s 抓取超时（50秒）", resourceID)
		item := &feeds.Item{
			Id:          fmt.Sprintf(detailURL, resourceID),
			Title:       fmt.Sprintf("[%s] 资源 %s 抓取超时", siteName, resourceID),
			Link:        &feeds.Link{Href: fmt.Sprintf(detailURL, resourceID)},
			Description: "错误: 抓取超时，请稍后重试",
			Created:     now,
		}
		feed.Items = append(feed.Items, item)
	}

	// 生成并缓存RSS（不变）
	rssString, err := feed.ToRss()
	if err != nil {
		log.Printf("生成RSS XML失败: %v", err)
		http.Error(w, "Failed to generate RSS feed", http.StatusInternalServerError)
		return
	}

	rssBytes := []byte(rssString)
	c.Set(cacheKey, rssBytes, cache.DefaultExpiration)
	log.Printf("已将结果存入缓存, Key: %s", cacheKey)

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rssBytes)
	log.Printf("成功响应请求 [BT影视], Resource ID: %s", resourceID)
}

// main（不变，仅初始化）
func main() {
	initHttpClient()

	defaultExpirationMinutes := getEnvInt("CACHE_EXPIRATION_MINUTES", 5)
	expirationDuration := time.Duration(defaultExpirationMinutes) * time.Minute
	cleanupInterval := expirationDuration * 2
	c = cache.New(expirationDuration, cleanupInterval)
	log.Printf("缓存服务初始化成功，缓存时间设置为: %d 分钟", defaultExpirationMinutes)
	log.Printf("HTTP配置：并发数=%d，重试次数=%d，重试间隔=%v", maxConcurrency, retryMax, retryInterval)

	r := mux.NewRouter()
	r.HandleFunc("/rss/btmovie/{resource_id}", rssHandler)
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Go RSS Service is running. Usage: /rss/btmovie/{resource_id}"))
	})

	port := getEnvStr("PORT", "8888")
	log.Printf("服务启动，监听端口: %s", port)
	log.Printf("请访问 http://localhost:%s/rss/btmovie/44851494 进行测试", port)

	srv := &http.Server{
		Handler:      r,
		Addr:         ":" + port,
		WriteTimeout: 60 * time.Second,
		ReadTimeout:  60 * time.Second,
	}

	log.Fatal(srv.ListenAndServe())
}