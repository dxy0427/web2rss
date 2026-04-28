package mukaku

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strconv"
	"time"

	"web2rss/shared"
)

// Scrape 抓取 web5.mukaku.com 影视资源
func Scrape(ctx *shared.SiteContext, reqCtx context.Context, idCode string) (*shared.PageInfo, error) {
	apiURL := fmt.Sprintf("%s?id=%s&app_id=%s&identity=%s", detailAPI, idCode, appID, identity)

	log.Printf("[mukaku] 请求 API：%s", apiURL)
	resp, err := shared.HTTPGetWithRetry(reqCtx, ctx.Client, apiURL, ctx.UserAgents, ctx.RetryMax, ctx.RetryInterval)
	if err != nil {
		return nil, fmt.Errorf("请求 API 失败: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	var apiResp ApiResponse
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
