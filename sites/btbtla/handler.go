package btbtla

import (
	"log"
	"net/http"

	"web2rss/shared"

	"github.com/gorilla/mux"
)

// RegisterRoutes 注册 btbtla 路由
func RegisterRoutes(r *mux.Router, ctx *shared.SiteContext) {
	r.HandleFunc("/rss/btbtla/{resource_id}", func(w http.ResponseWriter, req *http.Request) {
		resourceID := mux.Vars(req)["resource_id"]
		log.Printf("收到请求：/rss/btbtla/%s", resourceID)
		shared.HandleRSS(w, req, ctx, "bt_rss_v5_", resourceID, Scrape, baseURL)
	})
}
