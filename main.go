package main

import (
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gorilla/feeds"
	"github.com/gorilla/mux"
	"github.com/patrickmn/go-cache" // 1. 引入缓存库
)

// 2. 定义一个全局的缓存实例
// 缓存项默认5分钟过期，每10分钟清理一次过期项
var c = cache.New(5*time.Minute, 10*time.Minute)

const (
	siteName  = "BT影视"
	baseURL   = "https://www.btbtla.com"
	detailURL = baseURL + "/detail/%s.html"
)

// ResourceInfo 存储单个资源的信息
type ResourceInfo struct {
	ResourceTitle string
	Magnet        string
	Size          string
}

// PageInfo 存储整个页面的共享信息和所有资源
type PageInfo struct {
	Title     string
	Type      string
	Year      string
	DetailURL string
	Resources []ResourceInfo
}

// ScrapeBtMovie 函数负责抓取和解析
func ScrapeBtMovie(resourceID string) (*PageInfo, error) {
	pageInfo := &PageInfo{
		DetailURL: fmt.Sprintf(detailURL, resourceID),
	}
	
	log.Printf("开始抓取详情页: %s", pageInfo.DetailURL)
	
	client := &http.Client{Timeout: 20 * time.Second}
	
	resp, err := client.Get(pageInfo.DetailURL)
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
	
	doc.Find("a.module-row-text.copy").Each(func(i int, s *goquery.Selection) {
		downloadPagePath, exists := s.Attr("href")
		if !exists {
			return
		}
		
		resourceTitle := strings.TrimSpace(s.Find("h4").Text())
		re := regexp.MustCompile(`\s*\[[\d\.]+(?:GB|MB|TB)\]$`)
		resourceTitle = re.ReplaceAllString(resourceTitle, "")

		wg.Add(1)
		go func(path, title string) {
			defer wg.Done()
			
			downloadPageURL := baseURL + path
			log.Printf("开始抓取下载页: %s", downloadPageURL)
			
			respDownload, err := client.Get(downloadPageURL)
			if err != nil {
				log.Printf("错误: 请求下载页 %s 失败: %v", downloadPageURL, err)
				return
			}
			defer respDownload.Body.Close()

			if respDownload.StatusCode != http.StatusOK {
				log.Printf("错误: 下载页 %s 返回非200状态码: %d", downloadPageURL, respDownload.StatusCode)
				return
			}

			docDownload, err := goquery.NewDocumentFromReader(respDownload.Body)
			if err != nil {
				log.Printf("错误: 解析下载页 %s HTML失败: %v", downloadPageURL, err)
				return
			}
			
			magnetContainer := docDownload.Find("div.video-info-footer").First()
			magnetLink, magnetExists := magnetContainer.Find("a[href^='magnet:']").Attr("href")
			if !magnetExists {
				log.Printf("警告: 在下载页 %s 未找到磁力链接", downloadPageURL)
				return
			}
			
			size := "未知大小"
			sizeText := docDownload.Find("div.video-info-items:contains('影片大小') .video-info-item").Text()
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
			log.Printf("成功提取资源: %s", title)

		}(downloadPagePath, resourceTitle)
	})
	
	wg.Wait()

	if len(pageInfo.Resources) == 0 {
		return pageInfo, fmt.Errorf("未找到任何有效的磁力链接")
	}

	return pageInfo, nil
}


func rssHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	resourceID := vars["resource_id"]
	if resourceID == "" {
		http.Error(w, "Resource ID is required", http.StatusBadRequest)
		return
	}

	log.Printf("接收到请求, Resource ID: %s", resourceID)

	// 3. 在处理请求前，先检查缓存
	if rss, found := c.Get(resourceID); found {
		log.Printf("缓存命中, Resource ID: %s", resourceID)
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(rss.([]byte))
		return
	}

	log.Printf("缓存未命中, 开始实时抓取, Resource ID: %s", resourceID)
	// --- 如果缓存未命中，执行以下逻辑 ---

	now := time.Now()
	feed := &feeds.Feed{
		Title:       fmt.Sprintf("%s RSS Feed - %s", siteName, resourceID),
		Link:        &feeds.Link{Href: "https://www.btbtla.com/"},
		Description: "自动生成的BT影视资源RSS",
		Author:      &feeds.Author{Name: "Go RSS Generator"},
		Created:     now,
	}
	
	pageInfo, err := ScrapeBtMovie(resourceID)

	if err != nil {
		log.Printf("抓取或解析过程中发生错误: %v", err)
		item := &feeds.Item{
			Id:          fmt.Sprintf(detailURL, resourceID),
			Title:       fmt.Sprintf("[%s] 资源 %s 处理失败", siteName, resourceID),
			Link:        &feeds.Link{Href: fmt.Sprintf(detailURL, resourceID)},
			Description: fmt.Sprintf("错误: %v", err),
			Created:     now,
		}
		if pageInfo != nil && pageInfo.Title != "" {
			item.Title = fmt.Sprintf("[%s] %s (磁力链接获取失败)", siteName, pageInfo.Title)
			item.Description = fmt.Sprintf("类型: %s | 年份: %s | 错误: %v", pageInfo.Type, pageInfo.Year, err)
		}
		feed.Items = append(feed.Items, item)
	} else {
		log.Printf("成功抓取 %d 个资源, 开始生成RSS条目", len(pageInfo.Resources))
		for _, resource := range pageInfo.Resources {
			item := &feeds.Item{
				Title:       fmt.Sprintf("[%s] %s - %s", siteName, pageInfo.Title, resource.ResourceTitle),
				Link:        &feeds.Link{Href: resource.Magnet},
				Description: fmt.Sprintf("类型: %s | 年份: %s | 大小: %s", pageInfo.Type, pageInfo.Year, resource.Size),
				Id:          resource.Magnet, 
				Created:     now,
			}
			feed.Items = append(feed.Items, item)
		}
	}

	rssString, err := feed.ToRss()
	if err != nil {
		log.Printf("生成RSS XML失败: %v", err)
		http.Error(w, "Failed to generate RSS feed", http.StatusInternalServerError)
		return
	}

	rssBytes := []byte(rssString)
	// 4. 将新生成的结果存入缓存
	c.Set(resourceID, rssBytes, cache.DefaultExpiration)
	log.Printf("已将结果存入缓存, Resource ID: %s", resourceID)

	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rssBytes)
	log.Printf("成功响应请求, Resource ID: %s", resourceID)
}

func main() {
	r := mux.NewRouter()
	r.HandleFunc("/rss/btmovie/{resource_id}", rssHandler)
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Go RSS Service is running. Usage: /rss/btmovie/{resource_id}"))
	})

	port := "8888"
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