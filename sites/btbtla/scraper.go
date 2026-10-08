package btbtla

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"web2rss/shared"

	"github.com/PuerkitoBio/goquery"
)

func searchNameToDetailPath(ctx *shared.SiteContext, reqCtx context.Context, name string) string {
	searchURL := baseURL + "/search/" + url.PathEscape(name)
	log.Printf("[btbtla] 开始搜索：%s", searchURL)
	resp, err := shared.HTTPGetWithRetry(reqCtx, ctx.Client, searchURL, ctx.UserAgents, ctx.RetryMax, ctx.RetryInterval)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	doc, _ := goquery.NewDocumentFromReader(resp.Body)
	link := doc.Find(fmt.Sprintf(`.module-items .module-item .module-item-titlebox a[title="%s"]`, name)).AttrOr("href", "")
	if link != "" {
		log.Printf("[btbtla] 搜索成功：%s -> %s", name, link)
	}
	return link
}

// btDownPacing 下载页请求间隔：源站对速率敏感（约 2~3 请求/秒以内稳定，
// 更快会大量失败甚至临时拒绝），配合小并发使用
const btDownPacing = 400 * time.Millisecond

// btMaxResourcesPerPage 单个影片页最多抓取的资源条数。
// 页面按最新在前排序；超长连载页可达数百条，全量抓取会触发源站限速，
// 保留最新一批即可满足追更需求
const btMaxResourcesPerPage = 50

// Scrape 抓取 btbtla.com 影视资源
func Scrape(ctx *shared.SiteContext, reqCtx context.Context, param string) (*shared.PageInfo, error) {
	pageInfo := &shared.PageInfo{}

	if shared.IDRegex.MatchString(param) {
		pageInfo.DetailURL = fmt.Sprintf(detailURL, param)
	} else {
		path := searchNameToDetailPath(ctx, reqCtx, param)
		if path == "" {
			return nil, fmt.Errorf("not found")
		}
		pageInfo.DetailURL = baseURL + path
	}

	resp, err := shared.HTTPGetWithRetry(reqCtx, ctx.Client, pageInfo.DetailURL, ctx.UserAgents, ctx.RetryMax, ctx.RetryInterval)
	if err != nil {
		return pageInfo, err
	}
	defer resp.Body.Close()

	doc, _ := goquery.NewDocumentFromReader(resp.Body)
	title := shared.CleanString(doc.Find("h1.page-title").First().Text())
	pageInfo.Title = title
	log.Printf("[btbtla] 解析标题：%s", title)

	links := doc.Find("div[name=download-list] .module-downlist.selected .module-row-one.active .module-row-info")
	log.Printf("[btbtla] 找到 %d 个资源", links.Length())
	if links.Length() > btMaxResourcesPerPage {
		links = links.Slice(0, btMaxResourcesPerPage)
		log.Printf("[btbtla] 超过单页上限，只抓取最新 %d 条", btMaxResourcesPerPage)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var failCount int
	semMax := ctx.MaxConcurrency
	if semMax > 2 {
		semMax = 2
	}
	sem := make(chan struct{}, semMax)

	links.Each(func(i int, s *goquery.Selection) {
		downPath := s.Find(".module-row-text").AttrOr("href", "")
		rawTitle := shared.CleanString(s.Find(".module-row-title h4").Text())

		// 只保留 /tdown/ 种子下载页；网盘（/pdown/）等其他下载方式跳过
		if downPath == "" || !strings.HasPrefix(downPath, "/tdown/") {
			return
		}

		rType, fullEp, rStart, singleEp := shared.ExtractResourceType(rawTitle)
		rEnd := 0
		if rType == shared.ResTypeRange {
			matches := shared.EpisodeRangeRegex.FindStringSubmatch(rawTitle)
			if len(matches) >= 3 {
				rEnd, _ = strconv.Atoi(matches[2])
			}
		}

		wg.Add(1)
		go func(downPath, titleStr string, rt, fEp, rs, re, se int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			time.Sleep(btDownPacing)

			downResp, err := shared.HTTPGetWithRetry(reqCtx, ctx.Client, baseURL+downPath, ctx.UserAgents, ctx.RetryMax, ctx.RetryInterval)
			if downResp == nil || err != nil {
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}
			defer downResp.Body.Close()
			downDoc, _ := goquery.NewDocumentFromReader(downResp.Body)

			magnet := downDoc.Find(".btn-important").AttrOr("href", "")
			if magnet == "" {
				return // 页面无磁力，跳过
			}

			sizeStr := shared.CleanString(downDoc.Find(".video-info-items:contains('影片大小') .video-info-item").Text())
			timeText := shared.CleanString(downDoc.Find(".video-info-items:contains('种子时间') .video-info-item").Text())
			seedTime := time.Now()
			// 实际格式形如 "2026-05-19 20:30:40 +0800 CST"；先尝试带时区的完整 layout，
			// 失败再退回到裁剪时区后缀的旧 layout，最后才 fallback 到 time.Now()。
			if t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", timeText); err == nil {
				seedTime = t
			} else {
				trimmed := timeText
				if i := strings.Index(trimmed, " +"); i > 0 {
					trimmed = trimmed[:i]
				} else if i := strings.Index(trimmed, " -"); i > 0 {
					trimmed = trimmed[:i]
				}
				if t, err := time.ParseInLocation(shared.TimeLayout, trimmed, ctx.CSTZone); err == nil {
					seedTime = t
				}
			}

			mu.Lock()
			pageInfo.Resources = append(pageInfo.Resources, shared.ResourceInfo{
				ResourceTitle: titleStr,
				Magnet:        magnet,
				Size:          sizeStr,
				Bytes:         shared.ParseSizeToBytes(sizeStr),
				ResType:       rt,
				FullEpCount:   fEp,
				RangeStart:    rs,
				RangeEnd:      re,
				SingleEp:      se,
				TitleRaw:      titleStr,
				SeedTime:      seedTime,
				DetailPath:    downPath,
			})
			mu.Unlock()
		}(downPath, rawTitle, rType, fullEp, rStart, rEnd, singleEp)
	})

	wg.Wait()
	if failCount > 0 {
		log.Printf("[btbtla] 抓取失败：%d 个资源获取失败", failCount)
	}
	pageInfo.Resources = shared.SortResources(pageInfo.Resources)
	return pageInfo, nil
}
