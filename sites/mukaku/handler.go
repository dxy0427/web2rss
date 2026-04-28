package mukaku

import (
	"log"
	"net/http"

	"web2rss/shared"

	"github.com/gorilla/mux"
)

// RegisterRoutes 注册 mukaku 路由
func RegisterRoutes(r *mux.Router, ctx *shared.SiteContext) {
	r.HandleFunc("/rss/mukaku/{resource_id}", func(w http.ResponseWriter, req *http.Request) {
		resourceID := mux.Vars(req)["resource_id"]
		log.Printf("收到请求：/rss/mukaku/%s", resourceID)
		shared.HandleRSS(w, req, ctx, "mukaku_rss_v1_", resourceID, Scrape, baseURL)
	})
}
