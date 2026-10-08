package sixv123

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"web2rss/shared"

	"github.com/gorilla/mux"
)

const (
	defaultLimit = 25
	maxLimit     = 50
)

// RegisterRoutes 注册 6v123 路由。
// /rss/6v123/{category}：分类路径如 donghuapian（动画片）、dianshiju（电视剧），支持 ?limit=。
// /rss/6v123/search/{keyword}：站内搜索。
// /rss/6v123/detail/{path}：单个影片页订阅，路径取自 hao6v.org 影片页 URL。
func RegisterRoutes(r *mux.Router, ctx *shared.SiteContext) {
	// 读取并校验 ?limit=
	readLimit := func(req *http.Request) int {
		limit := defaultLimit
		if v := req.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		if limit > maxLimit {
			limit = maxLimit
		}
		return limit
	}

	// 站内搜索路由（需注册在分类 catch-all 之前，否则会被其吞掉）
	r.HandleFunc("/rss/6v123/search/{keyword:.+}", func(w http.ResponseWriter, req *http.Request) {
		keyword := mux.Vars(req)["keyword"]
		limit := readLimit(req)
		log.Printf("收到请求：/rss/6v123/search/%s（limit=%d）", keyword, limit)

		// limit 参与缓存键，不同 limit 各自缓存
		cacheKeyPrefix := fmt.Sprintf("6v123_search_v1_limit%d_", limit)
		scraper := func(c *shared.SiteContext, reqCtx context.Context, param string) (*shared.PageInfo, error) {
			return ScrapeSearch(c, reqCtx, param, limit)
		}
		shared.HandleRSS(w, req, ctx, cacheKeyPrefix, keyword, scraper, baseURL)
	})

	// 单个详情页订阅路由（需注册在分类 catch-all 之前，否则会被其吞掉）
	r.HandleFunc("/rss/6v123/detail/{path:.+}", func(w http.ResponseWriter, req *http.Request) {
		path := mux.Vars(req)["path"]
		log.Printf("收到请求：/rss/6v123/detail/%s", path)

		cacheKeyPrefix := "6v123_detail_v1_"
		scraper := func(c *shared.SiteContext, reqCtx context.Context, param string) (*shared.PageInfo, error) {
			return ScrapeDetail(c, reqCtx, param)
		}
		shared.HandleRSS(w, req, ctx, cacheKeyPrefix, path, scraper, baseURL)
	})

	handleCategory := func(category string) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			limit := readLimit(req)
			log.Printf("收到请求：/rss/6v123/%s（limit=%d）", category, limit)

			cacheKeyPrefix := fmt.Sprintf("6v123_rss_v1_limit%d_", limit)
			scraper := func(c *shared.SiteContext, reqCtx context.Context, param string) (*shared.PageInfo, error) {
				return Scrape(c, reqCtx, param, limit)
			}
			shared.HandleRSS(w, req, ctx, cacheKeyPrefix, category, scraper, baseURL)
		}
	}

	// 默认路由（首页最新）；分类可包含子路径（如 dianshiju/oumeiju），用 catch-all 匹配
	r.HandleFunc("/rss/6v123", handleCategory(""))
	r.HandleFunc("/rss/6v123/{category:.+}", func(w http.ResponseWriter, req *http.Request) {
		handleCategory(mux.Vars(req)["category"])(w, req)
	})
}
