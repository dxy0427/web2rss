package main

import (
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"web2rss/shared"
	"web2rss/sites/btbtla"
	"web2rss/sites/mukaku"

	"github.com/gorilla/mux"
	"github.com/patrickmn/go-cache"
)

func main() {
	// 初始化 HTTP 客户端
	retryMax := getEnvInt("RETRY_MAX", 2)
	retryInterval := time.Duration(getEnvInt("RETRY_INTERVAL_SEC", 1)) * time.Second
	maxConcurrency := getEnvInt("MAX_CONCURRENCY", math.MaxInt32)

	cstZone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		cstZone = time.FixedZone("CST", 8*3600)
	}

	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:       10,
			IdleConnTimeout:    30 * time.Second,
			DisableCompression: false,
		},
	}

	userAgents := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
	}

	// 初始化缓存
	exp := time.Duration(getEnvInt("CACHE_EXPIRATION_MINUTES", 15)) * time.Minute
	c := cache.New(exp, exp*2)

	// 构建共享上下文
	ctx := &shared.SiteContext{
		Client:         client,
		Cache:          c,
		CacheLock:      &sync.Map{},
		CacheExpiration: exp,
		UserAgents:     userAgents,
		RetryMax:       retryMax,
		RetryInterval:  retryInterval,
		MaxConcurrency: maxConcurrency,
		CSTZone:        cstZone,
	}

	// 注册路由
	r := mux.NewRouter()
	btbtla.RegisterRoutes(r, ctx)
	mukaku.RegisterRoutes(r, ctx)

	port := getEnvStr("PORT", "8888")
	log.Printf("web2rss 启动，端口：%s", port)
	log.Printf("  btbtla 路由: /rss/btbtla/{resource_id}")
	log.Printf("  mukaku 路由: /rss/mukaku/{idcode}")
	log.Fatal(http.ListenAndServe(":"+port, r))
}

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
