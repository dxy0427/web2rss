package mukaku

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
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
// param 可以是 idcode (纯数字) 或名称 (中文/英文)
func Scrape(ctx *shared.SiteContext, reqCtx context.Context, param string) (*shared.PageInfo, error) {
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

	apiURL := fmt.Sprintf("%s?id=%s&app_id=%s&identity=%s", detailAPI, idCode, appID, identity)

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

	log.Printf("[mukaku] 解析标题：%s, 分类数: %d", movie.Title, len(movie.Arrare))

	for _, seeds := range movie.Ecca {
		for _, seed := range seeds {
			seedTime := time.Now()
			if seed.EZT != "" {
				if t, err := time.ParseInLocation("2006-01-02", seed.EZT, ctx.CSTZone); err == nil {
					seedTime = t
				}
			}

			rType, fullEp, rStart, singleEp := shared.ExtractResourceType(seed.ZName)
			rEnd := 0
			if rType == shared.ResTypeRange {
				matches := shared.EpisodeRangeRegex.FindStringSubmatch(seed.ZName)
				if len(matches) >= 3 {
					rEnd, _ = strconv.Atoi(matches[2])
				}
			}

			magnet := seed.ZLink
			if magnet == "" && seed.Down != "" {
				magnet = baseURL + seed.Down
			}

			pageInfo.Resources = append(pageInfo.Resources, shared.ResourceInfo{
				ResourceTitle: seed.ZName,
				Magnet:        magnet,
				Size:          seed.ZSize,
				Bytes:         shared.ParseSizeToBytes(seed.ZSize),
				ResType:       rType,
				FullEpCount:   fullEp,
				RangeStart:    rStart,
				RangeEnd:      rEnd,
				SingleEp:      singleEp,
				TitleRaw:      seed.ZName,
				SeedTime:      seedTime,
				DetailPath:    fmt.Sprintf("/tr/%d.html", seed.ID),
			})
		}
	}

	pageInfo.Resources = shared.SortResources(pageInfo.Resources)
	return pageInfo, nil
}
