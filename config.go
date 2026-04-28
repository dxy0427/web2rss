package main

import (
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
)

// ========== btbtla.com 配置 ==========
const (
	baseURL   = "https://www.btbtla.com"
	detailURL = baseURL + "/detail/%s.html"
)

// ========== Mukaku (web5.mukaku.com) 配置 ==========
const (
	mukakuBaseURL   = "https://web5.mukaku.com"
	mukakuDetailAPI = mukakuBaseURL + "/prod/api/v1/getVideoDetail"
	mukakuAppID     = "83768d9ad4"
	mukakuIdentity  = "23734adac0301bccdcb107c4aa21f96c"
)

// ========== 全局变量 ==========
var (
	c              *cache.Cache
	httpClient     *http.Client
	maxConcurrency int
	retryMax       int
	retryInterval  time.Duration
	cacheLock      sync.Map
	userAgents     []string
	cstZone        *time.Location
)

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

func initHttpClient() {
	retryMax = getEnvInt("RETRY_MAX", 2)
	retryInterval = time.Duration(getEnvInt("RETRY_INTERVAL_SEC", 1)) * time.Second
	maxConcurrency = getEnvInt("MAX_CONCURRENCY", math.MaxInt32)

	var err error
	cstZone, err = time.LoadLocation("Asia/Shanghai")
	if err != nil {
		cstZone = time.FixedZone("CST", 8*3600)
	}

	httpClient = &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:       10,
			IdleConnTimeout:    30 * time.Second,
			DisableCompression: false,
		},
	}

	userAgents = []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
	}
}
