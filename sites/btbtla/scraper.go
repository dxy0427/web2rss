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

	var wg sync.WaitGroup
	var mu sync.Mutex
	var failCount int
	sem := make(chan struct{}, ctx.MaxConcurrency)

	links.Each(func(i int, s *goquery.Selection) {
		downPath := s.Find(".module-row-text").AttrOr("href", "")
		rawTitle := shared.CleanString(s.Find(".module-row-title h4").Text())

		if downPath == "" || strings.Contains(downPath, "/pdown/") {
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

			sizeStr := shared.CleanString(downDoc.Find(".video-info-items:contains('影片大小') .video-info-item").Text())
			timeText := shared.CleanString(downDoc.Find(".video-info-items:contains('种子时间') .video-info-item").Text())
			seedTime := time.Now()
			if t, err := time.ParseInLocation(shared.TimeLayout, timeText, ctx.CSTZone); err == nil {
				seedTime = t
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
