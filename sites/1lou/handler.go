package onelou

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
	defaultLimit = 20
	maxLimit     = 50
)

// RegisterRoutes 注册 1lou 路由。
// /rss/1lou/{resource_id}：resource_id 为搜索关键词或帖子 ID（纯数字），支持 ?limit= 限制结果数。
func RegisterRoutes(r *mux.Router, ctx *shared.SiteContext) {
	r.HandleFunc("/rss/1lou/{resource_id}", func(w http.ResponseWriter, req *http.Request) {
		resourceID := mux.Vars(req)["resource_id"]

		limit := defaultLimit
		if v := req.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		if limit > maxLimit {
			limit = maxLimit
		}

		log.Printf("收到请求：/rss/1lou/%s（limit=%d）", resourceID, limit)

		// limit 参与缓存键，不同 limit 各自缓存
		cacheKeyPrefix := fmt.Sprintf("1lou_rss_v1_limit%d_", limit)
		scraper := func(c *shared.SiteContext, reqCtx context.Context, param string) (*shared.PageInfo, error) {
			return Scrape(c, reqCtx, param, limit)
		}
		shared.HandleRSS(w, req, ctx, cacheKeyPrefix, resourceID, scraper, baseURL)
	})
}
