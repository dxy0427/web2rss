package mukaku

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"sort"
	"strconv"
	"time"

	"web2rss/shared"
)

var idCodeRegex = shared.IDRegex

// searchByName 通过名称搜索，返回第一个结果的 idcode
func searchByName(ctx *shared.SiteContext, reqCtx context.Context, name string) (string, error) {
	searchURL := fmt.Sprintf("%s/getVideoList?sb=%s&page=1&limit=5&app_id=%s&identity=%s",
		baseURL+"/prod/api/v1", url.QueryEscape(name), appID, identity)

	log.Printf("[mukaku] 搜索：%s", searchURL)
	resp, err := shared.HTTPGetWithRetry(reqCtx, ctx.Client, searchURL, ctx.UserAgents, ctx.RetryMax, ctx.RetryInterval)
	if err != nil {
		return "", fmt.Errorf("搜索请求失败: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取搜索结果失败: %w", err)
	}

	var searchResp SearchResponse
	if err := json.Unmarshal(bodyBytes, &searchResp); err != nil {
		return "", fmt.Errorf("解析搜索结果失败: %w", err)
	}

	if !searchResp.Success || searchResp.Data == nil || len(searchResp.Data.Data) == 0 {
		return "", fmt.Errorf("未找到: %s", name)
	}

	result := searchResp.Data.Data[0]
	log.Printf("[mukaku] 搜索到: %s (idcode=%s, doub_id=%d)", result.Title, result.IDCode, result.DoubID)

	// 优先用 idcode，其次用 doub_id
	if result.IDCode != "" {
		return result.IDCode, nil
	}
	if result.DoubID > 0 {
		return strconv.Itoa(result.DoubID), nil
	}
	return "", fmt.Errorf("搜索结果无有效 ID")
}

// Scrape 抓取 web5.mukaku.com 影视资源
// param 可以是 idcode (纯数字) 或名称 (中文/英文)；eps 为期望覆盖的最近集数
func Scrape(ctx *shared.SiteContext, reqCtx context.Context, param string, eps int) (*shared.PageInfo, error) {
	// 判断是 ID 还是名称
	idCode := param
	if !idCodeRegex.MatchString(param) {
		// 名称搜索
		found, err := searchByName(ctx, reqCtx, param)
		if err != nil {
			return nil, err
		}
		idCode = found
	}

	// 详情接口的种子列表（ecca）已 VIP 化，游客恒为空；
	// 有 token 时走 ecca，否则回退到公开种子流扫描
	token := ""
	if ctx.AccessToken != "" {
		token = "&access_token=" + url.QueryEscape(ctx.AccessToken)
	}

	apiURL := fmt.Sprintf("%s?id=%s&app_id=%s&identity=%s%s", detailAPI, idCode, appID, identity, token)

	log.Printf("[mukaku] 请求详情 API：%s", apiURL)
	resp, err := shared.HTTPGetWithRetry(reqCtx, ctx.Client, apiURL, ctx.UserAgents, ctx.RetryMax, ctx.RetryInterval)
	if err != nil {
		return nil, fmt.Errorf("请求详情 API 失败: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	var apiResp DetailResponse
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("解析 JSON 失败: %w", err)
	}

	if !apiResp.Success || apiResp.Data == nil {
		return nil, fmt.Errorf("API 返回失败: %s (code=%d)", apiResp.Message, apiResp.Code)
	}

	movie := apiResp.Data
	pageInfo := &shared.PageInfo{
		Title:     movie.Title,
		DetailURL: fmt.Sprintf("%s/mv/%s", baseURL, idCode),
	}

	log.Printf("[mukaku] 解析标题：%s, 分类数: %d, 种子数: %d", movie.Title, len(movie.Arrare), movie.Znum)

	// 详情接口的种子列表（ecca）已 VIP 化，游客恒为空；
	// 有 token 时走 ecca，否则回退到公开种子流 getTList 扫描
	var seedEntries []seedEntry
	for _, seeds := range movie.Ecca {
		for _, seed := range seeds {
			seedTime := time.Now()
			if seed.EZT != "" {
				if t, err := time.ParseInLocation("2006-01-02 15:04:05", seed.EZT, ctx.CSTZone); err == nil {
					seedTime = t
				} else if t, err := time.ParseInLocation("2006-01-02", seed.EZT, ctx.CSTZone); err == nil {
					seedTime = t
				}
			}
			seedEntries = append(seedEntries, seedEntry{
				ID: seed.ID, ZName: seed.ZName, ZSize: seed.ZSize,
				ZLink: seed.ZLink, Down: seed.Down, SeedTime: seedTime,
			})
		}
	}
	scanAborted := false
	if len(seedEntries) == 0 && movie.Znum > 0 {
		var entries []seedEntry
		entries, scanAborted = fetchSeedsFromTList(ctx, reqCtx, idCode, movie.Znum, movie.SeedUpdatedAt, movie.Type, eps)
		seedEntries = entries
		log.Printf("[mukaku] 公开种子流匹配到 %d/%d 条种子", len(seedEntries), movie.Znum)
	}

	for _, e := range seedEntries {
		pageInfo.Resources = append(pageInfo.Resources, buildSeedResource(ctx, e))
	}

	// 扫描中断时结果不完整，返回错误走 30 秒短缓存，避免残缺结果被缓存 15 分钟
	if reqCtx.Err() != nil || scanAborted {
		return pageInfo, fmt.Errorf("扫描被中断，本次抓取结果不全")
	}

	pageInfo.Resources = shared.SortResources(pageInfo.Resources)
	return pageInfo, nil
}

// seedEntry 统一种子条目（ecca 与公开种子流）
type seedEntry struct {
	ID       int
	ZName    string
	ZSize    string
	ZLink    string
	Down     string
	SeedTime time.Time
}

// buildSeedResource 由单条种子构建资源条目
func buildSeedResource(ctx *shared.SiteContext, e seedEntry) shared.ResourceInfo {
	seedTime := e.SeedTime
	if seedTime.IsZero() {
		seedTime = time.Now()
	}

	rType, fullEp, rStart, singleEp := shared.ExtractResourceType(e.ZName)
	rEnd := 0
	if rType == shared.ResTypeRange {
		if matches := shared.EpisodeRangeRegex.FindStringSubmatch(e.ZName); len(matches) >= 3 {
			rEnd, _ = strconv.Atoi(matches[2])
		}
	}

	magnet := e.ZLink
	if magnet == "" && e.Down != "" {
		magnet = baseURL + e.Down
	}

	return shared.ResourceInfo{
		ResourceTitle: e.ZName,
		Magnet:        magnet,
		Size:          e.ZSize,
		Bytes:         shared.ParseSizeToBytes(e.ZSize),
		ResType:       rType,
		FullEpCount:   fullEp,
		RangeStart:    rStart,
		RangeEnd:      rEnd,
		SingleEp:      singleEp,
		TitleRaw:      e.ZName,
		SeedTime:      seedTime,
		DetailPath:    fmt.Sprintf("/tr/%d.html", e.ID),
	}
}

const (
	tListPageLimit   = 20    // 每页条数（源站上限 20）
	tListScanCap     = 40    // 最新区顺序扫描的页数上限
	tListDeepCap     = 150   // 按集数深扫的总页数上限（每周一集的剧集约 15 页/集）
	tListJumpBack    = 5     // 二分定位后向前回扫页数
	tListJumpForward = 15    // 二分定位后向后扫页数
	tListMaxPage     = 20000 // 二分定位的页数上限
	defaultEps       = 5     // 默认抓取最近 5 集
	maxEps           = 10    // ?eps= 上限
)

// getTListPage 抓取种子流的一页（带节流；页面缓存供多部影片共享，减少重复请求）
func getTListPage(ctx *shared.SiteContext, reqCtx context.Context, sc, page int) ([]TSeed, error) {
	cacheKey := fmt.Sprintf("mukaku_tlist_v1_%d_%d", sc, page)
	if cached, found := ctx.Cache.Get(cacheKey); found {
		return cached.([]TSeed), nil
	}

	time.Sleep(tListPacing)
	apiURL := fmt.Sprintf("%s?sc=%d&page=%d&limit=%d&app_id=%s&identity=%s",
		tListAPI, sc, page, tListPageLimit, appID, identity)
	resp, err := shared.HTTPGetWithRetry(reqCtx, ctx.Client, apiURL, ctx.UserAgents, ctx.RetryMax, ctx.RetryInterval)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var listResp TListResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, err
	}
	if !listResp.Success {
		return nil, fmt.Errorf("getTList 返回失败: %s", listResp.Message)
	}

	ctx.Cache.Set(cacheKey, listResp.Data.List, tListPageCacheTTL)
	return listResp.Data.List, nil
}

// mukakuScrapeSem 种子流扫描并发上限。源站对速率敏感，3 路并发即可能被
// 重置连接，故限 2 路，并配合 tListPacing 节流。
var mukakuScrapeSem = make(chan struct{}, 2)

// tListPacing 种子流请求间隔，与并发上限一起把请求速率控制在安全范围内
const tListPacing = 150 * time.Millisecond

// tListPageCacheTTL 种子流页面缓存时长：多部影片扫同一条流时共享页面
const tListPageCacheTTL = 5 * time.Minute

// fetchSeedsFromTList 扫描公开种子流 getTList，按影片页路径匹配该影片的种子。
// 种子流按影片类型分区（sc=0 电影 / sc=2 剧集），按时间降序全量：
// 先顺序扫最新若干页，未找齐时按最近更新时间二分定位再做窗口扫描。
// eps 为期望覆盖的最近集数；返回 aborted 表示扫描因源站限速中断，结果不完整。
func fetchSeedsFromTList(ctx *shared.SiteContext, reqCtx context.Context, idcode string, znum int, seedUpdatedAt string, movieType int, eps int) ([]seedEntry, bool) {
	// 排队等待扫描名额（客户端断开时直接放弃）
	select {
	case mukakuScrapeSem <- struct{}{}:
		defer func() { <-mukakuScrapeSem }()
	case <-reqCtx.Done():
		return nil, true
	}

	targetUnix := int64(0)
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", seedUpdatedAt, ctx.CSTZone); err == nil {
		targetUnix = t.Unix()
	}

	// 影片类型对应种子流：电影（type=1）在 sc=0，其余（剧集等）在 sc=2
	sc := 2
	if movieType == 1 {
		sc = 0
	}
	found, aborted := scanTListStream(ctx, reqCtx, sc, "/mv/"+idcode, znum, targetUnix, eps)

	// 一个种子都没找到时尝试另一个流（类型映射可能不符）
	if len(found) == 0 && znum > 0 {
		other := 0
		if sc == 0 {
			other = 2
		}
		found, aborted = scanTListStream(ctx, reqCtx, other, "/mv/"+idcode, znum, targetUnix, eps)
	}

	// 按种子时间倒序（最新在前）
	sort.Slice(found, func(i, j int) bool { return found[i].SeedTime.After(found[j].SeedTime) })
	return found, aborted
}

// scanTListStream 扫描单个种子流，收集属于目标影片的种子。
// 种子名带 [第N集] 时按不重复集数收手（多扫 2 页补全当前集的各版本）；
// 无集数标记的影片（电影）只取最近一批。
func scanTListStream(ctx *shared.SiteContext, reqCtx context.Context, sc int, target string, znum int, targetUnix int64, eps int) ([]seedEntry, bool) {
	seenSeed := map[string]bool{} // 按磁力链接去重
	seenAnchor := map[int]bool{}  // 已扫到的集数（含合集标记 -1）
	var found []seedEntry
	aborted := false

	collect := func(items []TSeed) {
		for _, it := range items {
			if it.AURL1 != target || it.ZLink == "" || seenSeed[it.ZLink] {
				continue
			}
			seenSeed[it.ZLink] = true
			found = append(found, seedEntry{
				ID:       it.ID,
				ZName:    it.ZName,
				ZSize:    it.ZSize,
				ZLink:    it.ZLink,
				Down:     it.Down,
				SeedTime: time.Unix(it.EZT, 0).In(ctx.CSTZone),
			})
			if a, ok := episodeAnchor(it.ZName); ok {
				seenAnchor[a] = true
			}
		}
	}
	enough := func() bool { return znum > 0 && len(found) >= znum }
	// 剧集且已见集数标记 → 按集数收手；电影/eps=1 不启用
	epsReached := func() bool {
		return eps > 1 && len(seenAnchor) > 0 && len(seenAnchor) >= eps
	}

	// 1. 顺序扫描最新若干页：找齐 / 空页 / 越过影片最近更新时间 / 页数上限 即停
	scanned := 0
	reachedTarget := false
	for page := 1; page <= tListScanCap; page++ {
		if reqCtx.Err() != nil {
			return found, true
		}
		items, err := getTListPage(ctx, reqCtx, sc, page)
		if err != nil {
			log.Printf("[mukaku] 种子流第 %d 页请求失败，中止本段扫描：%v", page, err)
			aborted = true
			break
		}
		if len(items) == 0 {
			break
		}
		collect(items)
		scanned = page
		if enough() || epsReached() {
			return found, aborted
		}
		// 本页最旧的种子已早于影片最近更新时间 → 最新一批已扫过
		if targetUnix > 0 && items[len(items)-1].EZT <= targetUnix {
			reachedTarget = true
			break
		}
	}

	// 2. 扫到页数上限仍未越过目标时间（影片很久没更新）→ 二分定位目标页 + 窗口扫描
	if !enough() && !reachedTarget && targetUnix > 0 {
		p, locateFailed := locateTListPage(ctx, reqCtx, sc, targetUnix)
		if locateFailed {
			aborted = true
		}
		if p > 0 {
			from, to := p-tListJumpBack, p+tListJumpForward
			if from < 1 {
				from = 1
			}
			for pg := from; pg <= to; pg++ {
				if reqCtx.Err() != nil {
					aborted = true
					break
				}
				items, err := getTListPage(ctx, reqCtx, sc, pg)
				if err != nil {
					log.Printf("[mukaku] 窗口扫描第 %d 页请求失败：%v", pg, err)
					aborted = true
					continue
				}
				if len(items) == 0 {
					continue
				}
				collect(items)
				if pg > scanned {
					scanned = pg
				}
				if enough() || epsReached() {
					break
				}
			}
		}
	}

	// 3. 剧集且集数不够 → 继续向深处扫描，按集数收手；
	//    每周更新的剧集在流中约 15 页/集，故页数上限设得较深
	if !enough() && !epsReached() && len(seenAnchor) > 0 {
		margin := 0 // 够数后再多扫几页，补全当前集的各版本
		for page := scanned + 1; page <= tListDeepCap; page++ {
			if reqCtx.Err() != nil {
				aborted = true
				break
			}
			items, err := getTListPage(ctx, reqCtx, sc, page)
			if err != nil {
				log.Printf("[mukaku] 深扫第 %d 页请求失败，中止深扫（结果可能不全）：%v", page, err)
				aborted = true
				break
			}
			if len(items) == 0 {
				break
			}
			collect(items)
			if enough() {
				break
			}
			if epsReached() {
				margin++
				if margin >= 2 {
					break
				}
			}
		}
	}

	return found, aborted
}

// episodeAnchor 从种子名提取集数锚点（用于统计已覆盖的集数）
func episodeAnchor(zname string) (int, bool) {
	if m := shared.EpisodeSingleRegex.FindStringSubmatch(zname); len(m) >= 2 {
		n, _ := strconv.Atoi(m[1])
		return n, true
	}
	if m := shared.EpisodeRangeRegex.FindStringSubmatch(zname); len(m) >= 2 {
		n, _ := strconv.Atoi(m[1])
		return n, true
	}
	if m := shared.EpisodeFullRegex.FindStringSubmatch(zname); len(m) >= 2 {
		return -1, true // 合集
	}
	return 0, false
}

// locateTListPage 在时间降序的种子流中二分定位目标时间所在页。
// 返回 locateFailed 表示定位过程中有请求失败，定位结果不可靠。
func locateTListPage(ctx *shared.SiteContext, reqCtx context.Context, sc int, targetUnix int64) (int, bool) {
	lo, hi, best := 1, tListMaxPage, 0
	failed := false
	for lo <= hi {
		if reqCtx.Err() != nil {
			return 0, true
		}
		mid := (lo + hi) / 2
		items, err := getTListPage(ctx, reqCtx, sc, mid)
		if err != nil {
			failed = true
			hi = mid - 1 // 超出范围或失败，往浅处找
			continue
		}
		if len(items) == 0 {
			hi = mid - 1 // 超出范围，往浅处找
			continue
		}
		if items[0].EZT > targetUnix {
			lo = mid + 1 // 页面顶部比目标新，目标在更深处
		} else {
			best = mid // 页面顶部已早于目标，目标就在这页或更浅
			hi = mid - 1
		}
	}
	return best, failed
}
