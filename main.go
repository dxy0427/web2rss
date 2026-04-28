package main

import (
	"log"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/patrickmn/go-cache"
)

func main() {
	initHttpClient()
	exp := time.Duration(getEnvInt("CACHE_EXPIRATION_MINUTES", 15)) * time.Minute
	c = cache.New(exp, exp*2)

	r := mux.NewRouter()
	r.HandleFunc("/rss/btmovie/{resource_id}", rssHandler)
	r.HandleFunc("/rss/mukaku/{resource_id}", mukakuRssHandler)

	port := getEnvStr("PORT", "8888")
	log.Printf("web2rss 启动，端口：%s", port)
	log.Printf("  btbtla 路由: /rss/btmovie/{resource_id}")
	log.Printf("  mukaku 路由: /rss/mukaku/{idcode}")
	log.Fatal(http.ListenAndServe(":"+port, r))
}
