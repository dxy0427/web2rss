package shared

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
)

// SiteContext 站点共享上下文，传递给各站点的 Scraper
type SiteContext struct {
	Client         *http.Client
	Cache          *cache.Cache
	CacheLock      *sync.Map
	CacheExpiration time.Duration
	UserAgents     []string
	RetryMax       int
	RetryInterval  time.Duration
	MaxConcurrency int
	CSTZone        *time.Location
}

// ScraperFunc 爬虫函数签名：接收请求 context 和资源 ID
type ScraperFunc func(ctx *SiteContext, reqCtx context.Context, id string) (*PageInfo, error)
