package mukaku

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"web2rss/shared"

	"github.com/gorilla/mux"
)

// RegisterRoutes 注册 mukaku 路由。
// /rss/mukaku/{resource_id}?eps=N：resource_id 为 idcode 或名称，
// eps 为期望覆盖的最近集数（默认 5，最大 10），不同 eps 各自缓存。
func RegisterRoutes(r *mux.Router, ctx *shared.SiteContext) {
	r.HandleFunc("/rss/mukaku/{resource_id}", func(w http.ResponseWriter, req *http.Request) {
		resourceID := mux.Vars(req)["resource_id"]

		eps := defaultEps
		if v := req.URL.Query().Get("eps"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 1 {
				eps = n
			}
		}
		if eps > maxEps {
			eps = maxEps
		}

		log.Printf("收到请求：/rss/mukaku/%s（eps=%d）", resourceID, eps)

		// eps 参与缓存键，不同 eps 各自缓存
		cacheKeyPrefix := fmt.Sprintf("mukaku_rss_v2_eps%d_", eps)
		scraper := func(c *shared.SiteContext, reqCtx context.Context, param string) (*shared.PageInfo, error) {
			return Scrape(c, reqCtx, param, eps)
		}
		shared.HandleRSS(w, req, ctx, cacheKeyPrefix, resourceID, scraper, baseURL)
	})
}
