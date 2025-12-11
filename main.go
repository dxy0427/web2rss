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

// 全局变量
var (
	c                *cache.Cache
	httpClient       *http.Client
	maxConcurrency   int
	retryMax         int
	retryInterval    time.Duration
	cacheLock        sync.Map
	userAgents       []string
	
	// 正则表达式：用于提取大小和集数
	// 匹配字符串末尾的 [12.3GB] 这种格式，用于清理标题
	sizeRegex        = regexp.MustCompile(`\s*\[[\d\.]+(?:GB|MB|TB|KB)\]$`)
	// 用于计算字节数
	sizeExtractRegex = regexp.MustCompile(`(?i)(\d+(\.\d+)?)\s*([GMK]B)`)
	
	// 集数匹配正则（优化括号匹配）
	episodeFullRegex   = regexp.MustCompile(`\[\s*全(\d+)集\s*\]`)
	episodeRangeRegex  = regexp.MustCompile(`\[\s*第(\d+)\s*-\s*(\d+)\s*集\s*\]`)
	episodeSingleRegex = regexp.MustCompile(`\[\s*第(\d+)\s*集\s*\]`)
	
	// 时间格式
	timeLayout = "2006-01-02 15:04:05"
)

// 环境变量获取工具
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

// 初始化 HTTP 客户端
func initHttpClient() {
	retryMax = getEnvInt("RETRY_MAX", 2)
	retryInterval = time.Duration(getEnvInt("RETRY_INTERVAL_SEC", 1)) * time.Second
	maxConcurrency = getEnvInt("MAX_CONCURRENCY", 10) // 默认并发 10

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

// 带重试的 HTTP GET
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
		// 添加 Referer 避免防盗链
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

// 资源类型常量，用于排序优先级
const (
	resTypeFull   = 0 // 全集最优先
	resTypeRange  = 1 // 合集次之
	resTypeSingle = 2 // 单集最后
	resTypeOther  = 9 // 其他
)

type PageInfo struct {
	Title     string
	DetailURL string
	Resources []ResourceInfo
}

type ResourceInfo struct {
	ResourceTitle string    // 原始标题（用于显示）
	Magnet        string    // 磁力链接
	Size          string    // 字符串大小 (12.11GB)
	Bytes         int64     // 字节大小 (用于 RSS length)
	resType       int       // 类型 (0, 1, 2)
	fullEpCount   int       // 全集数量
	rangeStart    int       // 合集开始
	rangeEnd      int       // 合集结束
	singleEp      int       // 单集集数
	titleRaw      string    // 原始标题备份
	SeedTime      time.Time // 发布时间
	DetailPath    string    // 详情页路径
}

// 将 12.3GB 转换为字节数
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

// 解析资源类型（全集/合集/单集）
func extractResourceType(title string) (int, int, int, int) {
	// 匹配 [全8集]
	fullMatches := episodeFullRegex.FindStringSubmatch(title)
	if len(fullMatches) >= 2 {
		epCount, _ := strconv.Atoi(fullMatches[1])
		return resTypeFull, epCount, 0, 0
	}
	// 匹配 [第01-02集]
	rangeMatches := episodeRangeRegex.FindStringSubmatch(title)
	if len(rangeMatches) >= 3 {
		start, _ := strconv.Atoi(rangeMatches[1])
		end, _ := strconv.Atoi(rangeMatches[2])
		return resTypeRange, 0, start, end
	}
	// 匹配 [第01集]
	singleMatches := episodeSingleRegex.FindStringSubmatch(title)
	if len(singleMatches) >= 2 {
		ep, _ := strconv.Atoi(singleMatches[1])
		return resTypeSingle, 0, 0, ep
	}
	return resTypeOther, 0, 0, 0
}

// 核心排序逻辑
func sortResources(resources []ResourceInfo) []ResourceInfo {
	if len(resources) <= 1 {
		return resources
	}
	sorted := make([]ResourceInfo, len(resources))
	copy(sorted, resources)

	sort.Slice(sorted, func(i, j int) bool {
		// 1. 先按类型排：全集 < 合集 < 单集
		if sorted[i].resType != sorted[j].resType {
			return sorted[i].resType < sorted[j].resType
		}
		// 2. 如果都是全集，集数多的在前面 (降序)
		if sorted[i].resType == resTypeFull {
			return sorted[i].fullEpCount > sorted[j].fullEpCount
		}
		// 3. 如果都是合集，按起始集升序，起始相同按结束集升序
		if sorted[i].resType == resTypeRange {
			if sorted[i].rangeStart != sorted[j].rangeStart {
				return sorted[i].rangeStart < sorted[j].rangeStart
			}
			return sorted[i].rangeEnd < sorted[j].rangeEnd
		}
		// 4. 如果都是单集，按集数升序
		if sorted[i].resType == resTypeSingle {
			return sorted[i].singleEp < sorted[j].singleEp
		}
		// 5. 其他情况按标题字符串排序
		return sorted[i].titleRaw < sorted[j].titleRaw
	})
	return sorted
}

// 爬虫主逻辑
func ScrapeBtMovie(ctx context.Context, resourceID string) (*PageInfo, error) {
	pageInfo := &PageInfo{
		DetailURL: fmt.Sprintf(detailURL, resourceID),
	}
	
	// 1. 获取主列表页
	resp, err := httpGetWithRetry(ctx, pageInfo.DetailURL)
	if err != nil {
		return pageInfo, err // 返回已初始化的对象，避免外部空指针
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return pageInfo, err
	}

	pageInfo.Title = strings.TrimSpace(doc.Find("h1.page-title").First().Text())
	links := doc.Find("a.module-row-text.copy")
	
	if links.Length() == 0 {
		return pageInfo, nil
	}

	// 2. 并发抓取下载页
	var wg sync.WaitGroup
	var mu sync.Mutex
	// 限制并发数
	sem := make(chan struct{}, maxConcurrency)
	
	for i := 0; i < links.Length(); i++ {
		if ctx.Err() != nil {
			break
		}
		s := links.Eq(i)
		path, exists := s.Attr("href")
		if !exists || strings.Contains(path, "/pdown/") {
			continue // 跳过网盘
		}

		rawTitle := strings.TrimSpace(s.Find("h4").Text())
		// 清理标题后面的 [XXGB] 用于判断类型，但保留 rawTitle 用于输出
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
			
			// 获取信号量
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			// 请求下载页
			downResp, err := httpGetWithRetry(ctx, baseURL+downPath)
			if err != nil {
				return
			}
			defer downResp.Body.Close()

			downDoc, err := goquery.NewDocumentFromReader(downResp.Body)
			if err != nil {
				return
			}

			// 提取磁力
			magnet := downDoc.Find("a[href^='magnet:']").First().AttrOr("href", "")
			if magnet == "" {
				magnet = downDoc.Find("a[data-magnet]").AttrOr("data-magnet", "")
			}
			if magnet == "" {
				return
			}

			// 提取大小 (文本可能在多个位置)
			sizeStr := "0B"
			sizeText := downDoc.Find(".video-info-item").FilterFunction(func(i int, s *goquery.Selection) bool {
				return strings.Contains(s.Text(), "影片大小") || strings.Contains(s.Prev().Text(), "影片大小")
			}).Text()
			// 清理提取到的文本
			if sizeText != "" {
				// 有时候文本是 "影片大小：12.3GB"，需要处理
				parts := strings.Split(sizeText, "：")
				if len(parts) > 1 {
					sizeStr = strings.TrimSpace(parts[1])
				} else {
					sizeStr = strings.TrimSpace(sizeText)
				}
			}

			// 提取时间
			timeStr := downDoc.Find(".video-info-item").FilterFunction(func(i int, s *goquery.Selection) bool {
				return strings.Contains(s.Text(), "种子时间") || strings.Contains(s.Prev().Text(), "种子时间")
			}).Text()
			
			seedTime := time.Now()
			if timeStr != "" {
				parts := strings.Split(timeStr, "：")
				if len(parts) > 1 {
					t, err := time.Parse(timeLayout, strings.TrimSpace(parts[1]))
					if err == nil {
						seedTime = t
					}
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
	// 排序
	pageInfo.Resources = sortResources(pageInfo.Resources)
	return pageInfo, nil
}

// RSS 处理句柄
func rssHandler(w http.ResponseWriter, r *http.Request) {
	timeoutSec := getEnvInt("SCRAPE_TIMEOUT_SEC", 60)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	// 1. 获取路径参数 (修复了之前的BUG)
	vars := mux.Vars(r)
	resourceID := vars["resource_id"]
	if resourceID == "" {
		http.Error(w, "Resource ID is required", http.StatusBadRequest)
		return
	}

	cacheKey := "bt_rss_v2_" + resourceID
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")

	// 2. 检查缓存
	if rss, found := c.Get(cacheKey); found {
		w.WriteHeader(http.StatusOK)
		w.Write(rss.([]byte))
		return
	}

	// 3. 防击穿锁
	lockKey := "lock_" + resourceID
	lockVal, _ := cacheLock.LoadOrStore(lockKey, &sync.Mutex{})
	lock := lockVal.(*sync.Mutex)
	lock.Lock()
	defer func() {
		lock.Unlock()
		cacheLock.Delete(lockKey)
	}()

	// 二次检查缓存
	if rss, found := c.Get(cacheKey); found {
		w.WriteHeader(http.StatusOK)
		w.Write(rss.([]byte))
		return
	}

	// 4. 执行抓取
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

	// 5. 构建 RSS (安全构建，防止 panic)
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
		// 错误处理项
		feed.Items = append(feed.Items, &feeds.Item{
			Title:       fmt.Sprintf("抓取失败: %s", feedTitle),
			Description: fmt.Sprintf("错误信息: %v", err),
			Link:        &feeds.Link{Href: pageInfo.DetailURL},
			Created:     time.Now(),
		})
	} else {
		// 成功添加项
		for _, res := range pageInfo.Resources {
			// 严格按照你要求的 XML 格式
			// GUID = 标题 (isPermaLink=false)
			// Description = 标题 [大小]
			item := &feeds.Item{
				Title:       res.titleRaw,
				Link:        &feeds.Link{Href: baseURL + res.DetailPath},
				Description: fmt.Sprintf("%s [%s]", res.titleRaw, res.Size),
				Id:          res.titleRaw, // 设置 GUID 为标题
				Created:     res.SeedTime, // 这里的 Created 会被转换为 pubDate
				Enclosure: &feeds.Enclosure{
					Url:    res.Magnet,
					Type:   "application/x-bittorrent",
					Length: strconv.FormatInt(res.Bytes, 10),
				},
			}
			feed.Items = append(feed.Items, item)
		}
	}

	// 6. 生成 XML
	rssStr, rssErr := feed.ToRss()
	if rssErr != nil {
		http.Error(w, "RSS Generation Error", http.StatusInternalServerError)
		return
	}

	// 移除 feeds 库默认带的 content 命名空间引用，保持 XML 简洁 (与你样例一致)
	rssStr = strings.Replace(rssStr, ` xmlns:content="http://purl.org/rss/1.0/modules/content/"`, "", 1)
	
	// 手动替换 GUID 的属性，确保 isPermaLink="false" (feeds 库默认可能是 true)
	// 这是一个简单的字符串替换 hack，因为 gorilla/feeds 默认行为较难更改
	rssStr = strings.ReplaceAll(rssStr, `<guid>`, `<guid isPermaLink="false">`)

	rssBytes := []byte(rssStr)
	
	// 写入缓存 (成功缓存长，失败缓存短)
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
	// 修复：必须包含 {resource_id} 变量
	r.HandleFunc("/rss/btmovie/{resource_id}", rssHandler)
	
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("BT影视RSS服务运行中\n示例：/rss/btmovie/44851494"))
	})

	port := getEnvStr("PORT", "8888")
	log.Printf("服务启动在 :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}