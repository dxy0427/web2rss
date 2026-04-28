package shared

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/feeds"
)

// HandleRSS 通用 RSS 处理逻辑：缓存 + 并发锁 + feed 构建
func HandleRSS(w http.ResponseWriter, r *http.Request, ctx *SiteContext,
	cacheKeyPrefix, resourceID string, scraper ScraperFunc, siteBaseURL string) {

	start := time.Now()
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")

	cacheKey := cacheKeyPrefix + resourceID

	// 第一次缓存检查
	if cached, found := ctx.Cache.Get(cacheKey); found {
		w.Write(cached.([]byte))
		log.Printf("响应完成（缓存），耗时：%s", time.Since(start))
		return
	}

	// 并发锁
	lockKey := cacheKey + "_lock"
	lockVal, _ := ctx.CacheLock.LoadOrStore(lockKey, &sync.Mutex{})
	lock := lockVal.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	// 第二次缓存检查（锁内）
	if cached, found := ctx.Cache.Get(cacheKey); found {
		w.Write(cached.([]byte))
		log.Printf("响应完成（缓存），耗时：%s", time.Since(start))
		return
	}

	// 抓取
	pageInfo, err := scraper(ctx, r.Context(), resourceID)
	if pageInfo == nil {
		pageInfo = &PageInfo{Title: "未知资源"}
	}

	rssBytes := buildFeed(pageInfo, err, siteBaseURL)
	ctx.Cache.Set(cacheKey, rssBytes, ctx.CacheExpiration)
	w.Write(rssBytes)
	log.Printf("响应完成，耗时：%s", time.Since(start))
}

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
			feed.Items = append(feed.Items, &feeds.Item{
				Title:       res.TitleRaw,
				Link:        &feeds.Link{Href: siteBaseURL + res.DetailPath},
				Description: fmt.Sprintf("%s [%s]", res.TitleRaw, res.Size),
				Created:     res.SeedTime,
				Enclosure: &feeds.Enclosure{
					Url:    res.Magnet,
					Type:   "application/x-bittorrent",
					Length: strconv.FormatInt(res.Bytes, 10),
				},
			})
		}
	}

	rssStr, _ := feed.ToRss()
	return []byte(rssStr)
}
