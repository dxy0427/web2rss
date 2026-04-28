package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

func searchNameToDetailPath(name string) string {
	searchURL := baseURL + "/search/" + url.PathEscape(name)
	log.Printf("开始搜索：%s", searchURL)
	resp, err := httpGetWithRetry(context.Background(), searchURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	doc, _ := goquery.NewDocumentFromReader(resp.Body)
	link := doc.Find(fmt.Sprintf(`.module-items .module-item .module-item-titlebox a[title="%s"]`, name)).AttrOr("href", "")
	if link != "" {
		log.Printf("搜索成功：%s -> %s", name, link)
	}
	return link
}

// ScrapeBtMovie 抓取 btbtla.com 影视资源
func ScrapeBtMovie(ctx context.Context, param string) (*PageInfo, error) {
	pageInfo := &PageInfo{}

	if idRegex.MatchString(param) {
		pageInfo.DetailURL = fmt.Sprintf(detailURL, param)
	} else {
		path := searchNameToDetailPath(param)
		if path == "" {
			return nil, fmt.Errorf("not found")
		}
		pageInfo.DetailURL = baseURL + path
	}

	resp, err := httpGetWithRetry(ctx, pageInfo.DetailURL)
	if err != nil {
		return pageInfo, err
	}
	defer resp.Body.Close()

	doc, _ := goquery.NewDocumentFromReader(resp.Body)
	pageInfo.Title = cleanString(doc.Find("h1.page-title").First().Text())
	log.Printf("解析标题：%s", pageInfo.Title)

	links := doc.Find("div[name=download-list] .module-downlist.selected .module-row-one.active .module-row-info")
	log.Printf("找到 %d 个资源", links.Length())

	var wg sync.WaitGroup
	var mu sync.Mutex
	var failCount int
	sem := make(chan struct{}, maxConcurrency)

	links.Each(func(i int, s *goquery.Selection) {
		downPath := s.Find(".module-row-text").AttrOr("href", "")
		rawTitle := cleanString(s.Find(".module-row-title h4").Text())

		if downPath == "" || strings.Contains(downPath, "/pdown/") {
			return
		}

		rType, fullEp, rStart, singleEp := extractResourceType(rawTitle)
		rEnd := 0
		if rType == resTypeRange {
			matches := episodeRangeRegex.FindStringSubmatch(rawTitle)
			if len(matches) >= 3 {
				rEnd, _ = strconv.Atoi(matches[2])
			}
		}

		wg.Add(1)
		go func(downPath, titleStr string, rt, fEp, rs, re, se int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			downResp, err := httpGetWithRetry(ctx, baseURL+downPath)
			if downResp == nil || err != nil {
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}
			defer downResp.Body.Close()
			downDoc, _ := goquery.NewDocumentFromReader(downResp.Body)

			magnet := downDoc.Find(".btn-important").AttrOr("href", "")

			sizeStr := cleanString(downDoc.Find(".video-info-items:contains('影片大小') .video-info-item").Text())
			timeText := cleanString(downDoc.Find(".video-info-items:contains('种子时间') .video-info-item").Text())
			seedTime := time.Now()
			if t, err := time.ParseInLocation(timeLayout, timeText, cstZone); err == nil {
				seedTime = t
			}

			mu.Lock()
			pageInfo.Resources = append(pageInfo.Resources, ResourceInfo{
				ResourceTitle: titleStr,
				Magnet:        magnet,
				Size:          sizeStr,
				Bytes:         parseSizeToBytes(sizeStr),
				resType:       rt,
				fullEpCount:   fEp,
				rangeStart:    rs,
				rangeEnd:      re,
				singleEp:      se,
				titleRaw:      titleStr,
				SeedTime:      seedTime,
				DetailPath:    downPath,
			})
			mu.Unlock()
		}(downPath, rawTitle, rType, fullEp, rStart, rEnd, singleEp)
	})

	wg.Wait()
	if failCount > 0 {
		log.Printf("抓取失败：%d 个资源获取失败", failCount)
	}
	pageInfo.Resources = sortResources(pageInfo.Resources)
	return pageInfo, nil
}
