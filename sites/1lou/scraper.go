package onelou

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"web2rss/shared"

	"github.com/PuerkitoBio/goquery"
)

// magnetRegex 正文中的磁力链接（部分帖子不挂种子附件，只在正文贴磁力）
var magnetRegex = regexp.MustCompile(`magnet:\?xt=urn:btih:[0-9A-Za-z]+[^"'\s<>]*`)

// threadDetail 单个帖子详情页解析结果
type threadDetail struct {
	Title       string // 帖子标题（h4.break-all 最后一个文本节点）
	Description string // 原帖正文 HTML
	TorrentURL  string // 种子附件绝对链接（无种子时为空）
	Magnet      string // 正文磁力链接（兜底，无则为空）
	PostTime    string // 帖子发布时间 "2006-01-02 15:04"
}

// resolveURL 相对链接转绝对链接
func resolveURL(base, href string) string {
	if href == "" {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if u.IsAbs() {
		return u.String()
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return b.ResolveReference(u).String()
}

// Scrape 抓取 BT之家 1LOU (1lou.me) 资源。
// param 为纯数字时视作帖子 ID（thread-<id>.htm），否则作为搜索关键词。
func Scrape(ctx *shared.SiteContext, reqCtx context.Context, param string, limit int) (*shared.PageInfo, error) {
	if shared.IDRegex.MatchString(param) {
		return scrapeThread(ctx, reqCtx, "/thread-"+param+".htm", "1LOU 帖子 "+param)
	}
	return search(ctx, reqCtx, param, limit)
}

// search 按关键词搜索，每个结果帖子各取种子构建条目
func search(ctx *shared.SiteContext, reqCtx context.Context, keyword string, limit int) (*shared.PageInfo, error) {
	apiParams := url.Values{
		"q":       {keyword},
		"fid":     {"0"},
		"page":    {"1"},
		"sort":    {"newest"},
		"scope":   {"全部"},
		"type":    {"全部"},
		"year":    {"全部"},
		"quality": {"全部"},
		"source":  {"全部"},
		"track":   {"1"},
	}
	apiURL := searchAPI + "?" + apiParams.Encode()

	log.Printf("[1lou] 搜索：%s", apiURL)
	resp, err := shared.HTTPGetWithRetry(reqCtx, ctx.Client, apiURL, ctx.UserAgents, ctx.RetryMax, ctx.RetryInterval)
	if err != nil {
		return nil, fmt.Errorf("搜索请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取搜索结果失败: %w", err)
	}

	var searchResp SearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return nil, fmt.Errorf("解析搜索结果失败: %w", err)
	}

	pageInfo := &shared.PageInfo{
		Title:     fmt.Sprintf("搜索 %s - BT 之家 1LOU 站", keyword),
		DetailURL: baseURL + "/search?q=" + url.QueryEscape(keyword),
	}

	hits := searchResp.Data.Hits
	// 按 tid 去重，避免重复帖子生成重复条目
	seenTID := map[int]bool{}
	deduped := hits[:0]
	for _, h := range hits {
		if seenTID[h.TID] {
			continue
		}
		seenTID[h.TID] = true
		deduped = append(deduped, h)
	}
	hits = deduped
	log.Printf("[1lou] 搜索到 %d 个结果（总 %d）", len(hits), searchResp.Data.Total)
	if len(hits) == 0 {
		return pageInfo, nil
	}
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}

	// 并发抓取各帖子详情页（并发数限制得较温和，避免触发源站限速）；
	// 结果按搜索结果顺序存放，保证条目顺序稳定
	var wg sync.WaitGroup
	var mu sync.Mutex
	semMax := ctx.MaxConcurrency
	if semMax > 5 {
		semMax = 5
	}
	sem := make(chan struct{}, semMax)
	results := make([]shared.ResourceInfo, len(hits))

	for idx, hit := range hits {
		threadURL := hit.ThreadURL
		if threadURL == "" {
			threadURL = fmt.Sprintf("/thread-%d.htm", hit.TID)
		}
		wg.Add(1)
		go func(hit Hit, threadURL string, idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			detail := scrapeThreadDetail(ctx, reqCtx, resolveURL(baseURL, threadURL))
			if detail == nil {
				return
			}
			if detail.TorrentURL == "" && detail.Magnet == "" {
				return // 无种子也无磁力，跳过
			}

			title := detail.Title
			if title == "" {
				title = hit.Subject
			}
			// 发布时间用搜索接口的 create_date（正文里的日期可能是评论日期）
			seedTime := time.Unix(hit.CreateDate, 0).In(ctx.CSTZone)

			mu.Lock()
			results[idx] = buildResource(title, detail, seedTime, threadURL)
			mu.Unlock()
		}(hit, threadURL, idx)
	}

	wg.Wait()
	for _, res := range results {
		if res.TitleRaw != "" {
			pageInfo.Resources = append(pageInfo.Resources, res)
		}
	}
	return pageInfo, nil
}

// scrapeThread 抓取单个帖子（纯数字 ID 模式）
func scrapeThread(ctx *shared.SiteContext, reqCtx context.Context, threadURL, fallbackTitle string) (*shared.PageInfo, error) {
	pageInfo := &shared.PageInfo{
		Title:     fallbackTitle,
		DetailURL: resolveURL(baseURL, threadURL),
	}

	detail := scrapeThreadDetail(ctx, reqCtx, pageInfo.DetailURL)
	if detail == nil {
		return pageInfo, fmt.Errorf("详情页获取失败: %s", threadURL)
	}

	title := detail.Title
	if title == "" {
		title = fallbackTitle
	}
	pageInfo.Title = title

	if detail.TorrentURL != "" || detail.Magnet != "" {
		seedTime := time.Now()
		if t, err := time.ParseInLocation(shared.TimeLayout, detail.PostTime, ctx.CSTZone); err == nil {
			seedTime = t
		}
		pageInfo.Resources = append(pageInfo.Resources, buildResource(title, detail, seedTime, threadURL))
	}

	return pageInfo, nil
}

// scrapeThreadDetail 请求并解析帖子详情页
func scrapeThreadDetail(ctx *shared.SiteContext, reqCtx context.Context, detailURL string) *threadDetail {
	resp, err := shared.HTTPGetWithRetry(reqCtx, ctx.Client, detailURL, ctx.UserAgents, ctx.RetryMax, ctx.RetryInterval)
	if err != nil {
		log.Printf("[1lou] 详情页请求失败：%s: %v", detailURL, err)
		return nil
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		log.Printf("[1lou] 解析详情页失败：%s: %v", detailURL, err)
		return nil
	}

	return parseThreadDetail(doc)
}

// parseThreadDetail 解析帖子详情页 HTML
func parseThreadDetail(doc *goquery.Document) *threadDetail {
	detail := &threadDetail{
		// 标题是 h4.break-all 的最后一个文本节点（前面的文本节点是分类徽章）
		Title: shared.CleanString(doc.Find("h4.break-all").Contents().Last().Text()),
	}

	// 原帖正文取第一个 div.message.break-all（后续是回复）
	if msg := doc.Find("div.message.break-all").First(); msg.Length() > 0 {
		// 正文中的相对图片链接转绝对链接，便于阅读器展示
		msg.Find("img[src]").Each(func(i int, s *goquery.Selection) {
			if src, ok := s.Attr("src"); ok && !strings.HasPrefix(src, "http") {
				s.SetAttr("src", resolveURL(baseURL, src))
			}
		})
		if desc, err := msg.Html(); err == nil {
			detail.Description = desc
			detail.Magnet = magnetRegex.FindString(desc)
		}
	}

	// 种子附件：优先取 .torrent 附件，找不到时退回第一个附件
	var firstAttach, torrentAttach string
	doc.Find("ul.attachlist li a").Each(func(i int, s *goquery.Selection) {
		href := s.AttrOr("href", "")
		if href == "" {
			return
		}
		if firstAttach == "" {
			firstAttach = resolveURL(baseURL, href)
		}
		if torrentAttach == "" {
			text := strings.ToLower(shared.CleanString(s.Text()))
			icon, _ := s.Find("i").Attr("class")
			if strings.HasSuffix(text, ".torrent") || strings.Contains(icon, "torrent") {
				torrentAttach = resolveURL(baseURL, href)
			}
		}
	})
	detail.TorrentURL = torrentAttach
	if detail.TorrentURL == "" {
		detail.TorrentURL = firstAttach
	}

	detail.PostTime = shared.CleanString(doc.Find("span.date").First().Text())
	return detail
}

// buildResource 由帖子详情构建资源条目
func buildResource(title string, detail *threadDetail, seedTime time.Time, threadURL string) shared.ResourceInfo {
	// 种子附件下载链接优先；无附件时用正文磁力链接
	enclosure := detail.TorrentURL
	if enclosure == "" {
		enclosure = detail.Magnet
	}

	sizeStr := ""
	if m := shared.SizeExtractRegex.FindString(title); m != "" {
		sizeStr = shared.CleanString(m)
	}

	rType, fullEp, rStart, singleEp := shared.ExtractResourceType(title)
	rEnd := 0
	if rType == shared.ResTypeRange {
		if matches := shared.EpisodeRangeRegex.FindStringSubmatch(title); len(matches) >= 3 {
			// 已在 ExtractResourceType 中匹配过，这里取结束集数
			rEnd, _ = strconv.Atoi(matches[2])
		}
	}

	return shared.ResourceInfo{
		ResourceTitle: title,
		Magnet:        enclosure,
		Size:          sizeStr,
		Description:   detail.Description,
		Bytes:         shared.ParseSizeToBytes(sizeStr),
		ResType:       rType,
		FullEpCount:   fullEp,
		RangeStart:    rStart,
		RangeEnd:      rEnd,
		SingleEp:      singleEp,
		TitleRaw:      title,
		SeedTime:      seedTime,
		DetailPath:    threadURL,
	}
}
