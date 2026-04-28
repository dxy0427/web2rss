package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/feeds"
	"github.com/gorilla/mux"
)

// buildFeed 通用：从 PageInfo 构建 RSS feed 字节
func buildFeed(pageInfo *PageInfo, err error, siteBaseURL string) []byte {
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
				Link:        &feeds.Link{Href: siteBaseURL + res.DetailPath},
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
	return []byte(rssStr)
}

// rssWithCache 通用的缓存 + 并发锁逻辑
func rssWithCache(w http.ResponseWriter, r *http.Request, cacheKey, lockPrefix, resourceID string,
	scraper func(ctx context.Context, id string) (*PageInfo, error), siteBaseURL string) {

	start := time.Now()
	timeoutSec := getEnvInt("SCRAPE_TIMEOUT_SEC", 60)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")

	// 第一次缓存检查
	if cached, found := c.Get(cacheKey); found {
		w.Write(cached.([]byte))
		log.Printf("响应完成（缓存），耗时：%s", time.Since(start))
		return
	}

	// 并发锁，防止相同资源重复抓取
	lockKey := lockPrefix + resourceID
	lockVal, _ := cacheLock.LoadOrStore(lockKey, &sync.Mutex{})
	lock := lockVal.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	// 第二次缓存检查（锁内）
	if cached, found := c.Get(cacheKey); found {
		w.Write(cached.([]byte))
		log.Printf("响应完成（缓存），耗时：%s", time.Since(start))
		return
	}

	// 抓取
	pageInfo, err := scraper(ctx, resourceID)
	if pageInfo == nil {
		pageInfo = &PageInfo{Title: "未知资源"}
	}

	rssBytes := buildFeed(pageInfo, err, siteBaseURL)
	c.Set(cacheKey, rssBytes, time.Duration(getEnvInt("CACHE_EXPIRATION_MINUTES", 15))*time.Minute)
	w.Write(rssBytes)
	log.Printf("响应完成，耗时：%s", time.Since(start))
}

// rssHandler btbtla.com RSS 入口
func rssHandler(w http.ResponseWriter, r *http.Request) {
	resourceID := mux.Vars(r)["resource_id"]
	log.Printf("收到请求：/rss/btbtla/%s", resourceID)
	rssWithCache(w, r, "bt_rss_v5_"+resourceID, "lock_", resourceID, ScrapeBtMovie, baseURL)
}

// mukakuRssHandler mukaku.com RSS 入口
func mukakuRssHandler(w http.ResponseWriter, r *http.Request) {
	resourceID := mux.Vars(r)["resource_id"]
	log.Printf("收到请求：/rss/mukaku/%s", resourceID)
	rssWithCache(w, r, "mukaku_rss_v1_"+resourceID, "mukaku_lock_", resourceID, ScrapeMukaku, mukakuBaseURL)
}
