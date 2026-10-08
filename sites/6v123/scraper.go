package sixv123

import (
	"context"
	"fmt"
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

var (
	// 磁力链接中的精确大小（字节），如 &xl=2881938518
	magnetSizeRegex = regexp.MustCompile(`[?&]xl=(\d+)`)
	// 磁力 infohash，用于给每条资源生成唯一链接锚点
	magnetHashRegex = regexp.MustCompile(`urn:btih:([0-9A-Za-z]+)`)
	// 单个详情页最多保留的磁力条数（长篇连载一页可达数百条，保留最新的）
	maxMagnetsPerPage = 20
)

// listItem 列表页（分类页/首页/搜索结果页，结构相同）中的单个条目
type listItem struct {
	Title     string
	DetailURL string // 相对路径，如 /donghuapian/15719.html
	Date      string // 发布日期 "2006-01-02"，可能为空
}

// magnetInfo 详情页中的单条磁力
type magnetInfo struct {
	URL   string
	Label string // 磁力链接文本，如 "01.1080pHD国语中字无水印.mp4"
	Bytes int64  // xl 参数（精确字节数），无则为 0
}

// threadDetail 单个详情页解析结果
type threadDetail struct {
	Title       string // h1 标题
	Description string // 正文 HTML（已移除磁力表格）
	PostTime    string // 发布日期 "2006-01-02"，可能为空
	Magnets     []magnetInfo
}

// movieCategories 源站的全部分类路径。影片页 URL 必须带分类段（同一 ID 换分类为 404），
// 纯数字 ID 订阅时逐个探测定位
var movieCategories = []string{
	"donghuapian", "dianshiju", "dianshiju/oumeiju", "dianshiju/guoju",
	"dianshiju/rihanju", "dianshiju/duanju", "xijupian", "dongzuopian",
	"aiqingpian", "kehuanpian", "kongbupian", "juqingpian", "zhanzhengpian", "jilupian",
}

// Scrape 抓取 6v电影网新版 (hao6v.org) 资源。
// 分类路径均为 ASCII（donghuapian、dianshiju 等）；纯数字视作影片 ID，
// 探测分类路径定位；param 含非 ASCII 字符时视作片名，搜索并订阅第一个结果。
func Scrape(ctx *shared.SiteContext, reqCtx context.Context, param string, limit int) (*shared.PageInfo, error) {
	if !isASCIIParam(param) {
		return scrapeByName(ctx, reqCtx, param)
	}
	if shared.IDRegex.MatchString(param) {
		if path := resolveMoviePath(ctx, reqCtx, param); path != "" {
			log.Printf("[6v123] ID 订阅：%s -> %s", param, path)
			return ScrapeDetail(ctx, reqCtx, path)
		}
		return nil, fmt.Errorf("未找到影片: %s", param)
	}

	path := strings.Trim(param, "/")
	targetURL := baseURL + "/" + path
	if path != "" && !strings.HasSuffix(targetURL, "/") {
		targetURL += "/"
	}

	pageInfo := &shared.PageInfo{DetailURL: targetURL}

	log.Printf("[6v123] 请求分类页：%s", targetURL)
	doc, err := getDocument(ctx, reqCtx, targetURL)
	if err != nil {
		// 分类页不存在时回退为片名搜索（兼容英文片名）
		if pi, nerr := scrapeByName(ctx, reqCtx, param); nerr == nil {
			return pi, nil
		}
		return pageInfo, fmt.Errorf("分类页请求失败: %w", err)
	}

	// feed 标题：页面 <title> 短横线前的分类名，如 "动画片-新版6v电影..."
	pageTitle := shared.CleanString(doc.Find("title").First().Text())
	categoryName := pageTitle
	if i := strings.Index(pageTitle, "-"); i > 0 {
		categoryName = shared.CleanString(pageTitle[:i])
	}
	if path == "" || categoryName == "" {
		categoryName = "最新"
	}
	pageInfo.Title = fmt.Sprintf("6v电影网 - %s", categoryName)

	items := parseList(doc)
	log.Printf("[6v123] 解析到 %d 个条目", len(items))
	if len(items) == 0 {
		return pageInfo, nil
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}

	pageInfo.Resources = fetchDetails(ctx, reqCtx, items)
	return pageInfo, nil
}

// ScrapeSearch 站内搜索，每个结果条目各取磁力构建条目。
func ScrapeSearch(ctx *shared.SiteContext, reqCtx context.Context, keyword string, limit int) (*shared.PageInfo, error) {
	pageInfo := &shared.PageInfo{
		Title:     fmt.Sprintf("搜索 %s - 6v电影网", keyword),
		DetailURL: baseURL + "/",
	}

	items, err := searchByName(ctx, reqCtx, keyword)
	if err != nil {
		return pageInfo, err
	}
	log.Printf("[6v123] 搜索到 %d 个结果", len(items))
	if len(items) == 0 {
		return pageInfo, nil
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}

	pageInfo.Resources = fetchDetails(ctx, reqCtx, items)
	return pageInfo, nil
}

// searchByName 站内搜索，返回结果列表。
// 搜索结果页由 searchid 重定向给出，客户端自动跟随即可。
func searchByName(ctx *shared.SiteContext, reqCtx context.Context, keyword string) ([]listItem, error) {
	log.Printf("[6v123] 搜索：%s", keyword)
	// 与站内搜索表单一致
	form := url.Values{
		"keyboard": {keyword},
		"show":     {"title"},
		"tempid":   {"1"},
		"tbname":   {"article"},
		"mid":      {"1"},
		"dopost":   {"search"},
	}
	resp, err := shared.HTTPPostWithRetry(reqCtx, ctx.Client, searchURL, "application/x-www-form-urlencoded", []byte(form.Encode()),
		ctx.UserAgents, ctx.RetryMax, ctx.RetryInterval)
	if err != nil {
		return nil, fmt.Errorf("搜索请求失败: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("解析搜索结果失败: %w", err)
	}
	return parseList(doc), nil
}

// scrapeByName 按片名搜索并订阅第一个结果
func scrapeByName(ctx *shared.SiteContext, reqCtx context.Context, keyword string) (*shared.PageInfo, error) {
	items, err := searchByName(ctx, reqCtx, keyword)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("未找到: %s", keyword)
	}
	first := items[0]
	log.Printf("[6v123] 名称订阅：%s -> %s", keyword, first.DetailURL)
	return ScrapeDetail(ctx, reqCtx, first.DetailURL)
}

// isASCIIParam 判断参数是否全为 ASCII 字符（分类路径均为 ASCII）
func isASCIIParam(s string) bool {
	for _, r := range s {
		if r >= 128 {
			return false
		}
	}
	return true
}

// resolveMoviePath 用纯数字 ID 探测影片页路径：并发尝试各分类，命中即返回
func resolveMoviePath(ctx *shared.SiteContext, reqCtx context.Context, id string) string {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)
	result := make(chan string, len(movieCategories))

	for _, cat := range movieCategories {
		wg.Add(1)
		go func(cat string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			path := fmt.Sprintf("/%s/%s.html", cat, id)
			resp, err := shared.HTTPGetWithRetry(reqCtx, ctx.Client, baseURL+path,
				ctx.UserAgents, ctx.RetryMax, ctx.RetryInterval)
			if err == nil {
				resp.Body.Close()
				result <- path
			}
		}(cat)
	}

	wg.Wait()
	close(result)
	for p := range result {
		return p
	}
	return ""
}

// ScrapeDetail 订阅单个详情页。path 为影片页路径，
// 形如 donghuapian/15719.html（可省略开头的 / 和结尾的 .html）。
func ScrapeDetail(ctx *shared.SiteContext, reqCtx context.Context, path string) (*shared.PageInfo, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, ".html") {
		path = path + ".html"
	}

	pageInfo := &shared.PageInfo{
		Title:     "6v电影网",
		DetailURL: baseURL + path,
	}

	detail := scrapeDetail(ctx, reqCtx, pageInfo.DetailURL)
	if detail == nil {
		return pageInfo, fmt.Errorf("详情页获取失败: %s", path)
	}

	title := detail.Title
	if title == "" {
		title = strings.TrimSuffix(strings.TrimPrefix(path, "/"), ".html")
	}
	pageInfo.Title = title

	if len(detail.Magnets) == 0 {
		return pageInfo, nil // 无磁力，返回空 feed
	}

	seedTime := time.Now()
	if detail.PostTime != "" {
		if t, err := time.ParseInLocation("2006-01-02", detail.PostTime, ctx.CSTZone); err == nil {
			seedTime = t
		}
	}

	pageInfo.Resources = buildResources(detail, listItem{DetailURL: path}, seedTime)
	return pageInfo, nil
}

// parseList 解析列表页条目（首页/分类页/搜索结果页结构相同）：
// ul#post_container > li.post，标题与链接在 h2 a，日期在 .info_date
func parseList(doc *goquery.Document) []listItem {
	var items []listItem
	seen := map[string]bool{}
	doc.Find("li.post").Each(func(i int, s *goquery.Selection) {
		link := s.Find("h2 a").First()
		href := link.AttrOr("href", "")
		if href == "" || seen[href] {
			return
		}
		seen[href] = true
		items = append(items, listItem{
			Title:     shared.CleanString(link.Text()),
			DetailURL: href,
			Date:      shared.CleanString(s.Find(".info_date").First().Text()),
		})
	})
	return items
}

// fetchDetails 并发抓取各详情页，提取磁力并构建资源（结果按 items 顺序存放，保证条目顺序稳定）。
// 并发数限制得较温和，避免触发源站限速。
func fetchDetails(ctx *shared.SiteContext, reqCtx context.Context, items []listItem) []shared.ResourceInfo {
	var wg sync.WaitGroup
	var mu sync.Mutex
	semMax := ctx.MaxConcurrency
	if semMax > 5 {
		semMax = 5
	}
	sem := make(chan struct{}, semMax)
	results := make([][]shared.ResourceInfo, len(items))

	for idx, item := range items {
		wg.Add(1)
		go func(item listItem, idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			detail := scrapeDetail(ctx, reqCtx, resolveURL(baseURL, item.DetailURL))
			if detail == nil || len(detail.Magnets) == 0 {
				return // 无磁力，跳过
			}
			// 分类/搜索流程按页封顶：页面按集数升序，保留最后的（最新），
			// 避免长篇连载把列表 feed 撑爆（单片订阅不截断）
			if len(detail.Magnets) > maxMagnetsPerPage {
				detail.Magnets = detail.Magnets[len(detail.Magnets)-maxMagnetsPerPage:]
			}

			seedTime := time.Now()
			if t, err := time.ParseInLocation("2006-01-02", item.Date, ctx.CSTZone); err == nil {
				seedTime = t
			}

			resources := buildResources(detail, item, seedTime)

			mu.Lock()
			results[idx] = resources
			mu.Unlock()
		}(item, idx)
	}

	wg.Wait()
	var collected []shared.ResourceInfo
	for _, rs := range results {
		collected = append(collected, rs...)
	}
	return collected
}

// buildResources 由详情页与发布时间构建该页全部资源条目：
// 每条磁力一个条目，单条时标题用详情页标题，多条时标题附加磁力标签
func buildResources(detail *threadDetail, item listItem, seedTime time.Time) []shared.ResourceInfo {
	title := detail.Title
	if title == "" {
		title = item.Title
	}
	resources := make([]shared.ResourceInfo, 0, len(detail.Magnets))
	for _, mg := range detail.Magnets {
		itemTitle := title
		if len(detail.Magnets) > 1 && mg.Label != "" {
			itemTitle = fmt.Sprintf("%s %s", title, mg.Label)
		}
		resources = append(resources, buildResource(itemTitle, detail, mg, seedTime, item.DetailURL))
	}
	return resources
}

// scrapeDetail 请求并解析详情页
func scrapeDetail(ctx *shared.SiteContext, reqCtx context.Context, detailURL string) *threadDetail {
	resp, err := shared.HTTPGetWithRetry(reqCtx, ctx.Client, detailURL, ctx.UserAgents, ctx.RetryMax, ctx.RetryInterval)
	if err != nil {
		log.Printf("[6v123] 详情页请求失败：%s: %v", detailURL, err)
		return nil
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		log.Printf("[6v123] 解析详情页失败：%s: %v", detailURL, err)
		return nil
	}

	// 标题：文章容器内的 h1（页面头部 logo 也有一个 h1，不能取错）
	titleSel := doc.Find("div.article_container h1").First()
	if titleSel.Length() == 0 {
		titleSel = doc.Find("div.mainleft h1").First()
	}
	detail := &threadDetail{
		Title:    shared.CleanString(titleSel.Text()),
		PostTime: shared.CleanString(doc.Find(".info_date").First().Text()),
	}

	// 提取全部磁力链接（条数封顶由调用方处理）
	doc.Find(`td a[href^="magnet"]`).Each(func(i int, s *goquery.Selection) {
		href := s.AttrOr("href", "")
		if href == "" {
			return
		}
		mg := magnetInfo{
			URL:   href,
			Label: shared.CleanString(s.Text()),
		}
		if m := magnetSizeRegex.FindStringSubmatch(href); len(m) >= 2 {
			mg.Bytes, _ = strconv.ParseInt(m[1], 10, 64)
		}
		detail.Magnets = append(detail.Magnets, mg)
	})

	// 正文取 div#post_content
	endText := doc.Find("div#post_content").First()
	if endText.Length() > 0 {
		// 移除磁力表格：连载页磁力表可达数百行且体积大，磁力已由 enclosure 提供
		endText.Find("table").Each(func(i int, s *goquery.Selection) {
			if s.Find(`a[href^="magnet"]`).Length() > 0 {
				s.Remove()
			}
		})
		// 相对图片链接转绝对链接，便于阅读器展示
		endText.Find("img[src]").Each(func(i int, s *goquery.Selection) {
			if src, ok := s.Attr("src"); ok && !strings.HasPrefix(src, "http") {
				s.SetAttr("src", resolveURL(detailURL, src))
			}
		})
		if desc, err := endText.Html(); err == nil {
			detail.Description = desc
		}
	}

	return detail
}

// getDocument GET 请求并解析为 goquery 文档
func getDocument(ctx *shared.SiteContext, reqCtx context.Context, targetURL string) (*goquery.Document, error) {
	resp, err := shared.HTTPGetWithRetry(reqCtx, ctx.Client, targetURL, ctx.UserAgents, ctx.RetryMax, ctx.RetryInterval)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return goquery.NewDocumentFromReader(resp.Body)
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

// buildResource 由详情页与单条磁力构建资源条目
func buildResource(title string, detail *threadDetail, mg magnetInfo, seedTime time.Time, detailPath string) shared.ResourceInfo {
	// 同一影片页的多条磁力若共用页面链接，阅读器会按链接去重把它们合并成一条，
	// 因此用磁力哈希（或标签）作锚点保证每条资源链接唯一，锚点不影响页面打开
	if m := magnetHashRegex.FindStringSubmatch(mg.URL); len(m) >= 2 {
		detailPath += "#" + m[1]
	} else if mg.Label != "" {
		detailPath += "#" + url.QueryEscape(mg.Label)
	}
	// 磁力链接的 xl 参数为精确字节数；缺失时从标题解析
	sizeStr := ""
	bytes := mg.Bytes
	if bytes > 0 {
		sizeStr = shared.FormatBytes(bytes)
	} else if m := shared.SizeExtractRegex.FindString(title); m != "" {
		sizeStr = shared.CleanString(m)
		bytes = shared.ParseSizeToBytes(sizeStr)
	}

	rType, fullEp, rStart, singleEp := shared.ExtractResourceType(title)
	rEnd := 0
	if rType == shared.ResTypeRange {
		if matches := shared.EpisodeRangeRegex.FindStringSubmatch(title); len(matches) >= 3 {
			rEnd, _ = strconv.Atoi(matches[2])
		}
	}

	return shared.ResourceInfo{
		ResourceTitle: title,
		Magnet:        mg.URL,
		Size:          sizeStr,
		Description:   detail.Description,
		Bytes:         bytes,
		ResType:       rType,
		FullEpCount:   fullEp,
		RangeStart:    rStart,
		RangeEnd:      rEnd,
		SingleEp:      singleEp,
		TitleRaw:      title,
		SeedTime:      seedTime,
		DetailPath:    detailPath,
	}
}
