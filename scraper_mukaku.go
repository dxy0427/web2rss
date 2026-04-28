package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strconv"
	"time"
)

// ScrapeMukaku 抓取 web5.mukaku.com 影视资源
func ScrapeMukaku(ctx context.Context, idCode string) (*PageInfo, error) {
	apiURL := fmt.Sprintf("%s?id=%s&app_id=%s&identity=%s",
		mukakuDetailAPI, idCode, mukakuAppID, mukakuIdentity)

	log.Printf("请求 Mukaku API：%s", apiURL)
	resp, err := httpGetWithRetry(ctx, apiURL)
	if err != nil {
		return nil, fmt.Errorf("请求 Mukaku API 失败: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	var apiResp MukakuResponse
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("解析 JSON 失败: %w", err)
	}

	if !apiResp.Success || apiResp.Data == nil {
		return nil, fmt.Errorf("API 返回失败: %s (code=%d)", apiResp.Message, apiResp.Code)
	}

	movie := apiResp.Data
	pageInfo := &PageInfo{
		Title:     movie.Title,
		DetailURL: fmt.Sprintf("%s/mv/%s", mukakuBaseURL, idCode),
	}

	log.Printf("解析标题：%s, 分类数: %d", movie.Title, len(movie.Arrare))

	for _, seeds := range movie.Ecca {
		for _, seed := range seeds {
			seedTime := time.Now()
			if seed.EZT != "" {
				if t, err := time.ParseInLocation("2006-01-02", seed.EZT, cstZone); err == nil {
					seedTime = t
				}
			}

			rType, fullEp, rStart, singleEp := extractResourceType(seed.ZName)
			rEnd := 0
			if rType == resTypeRange {
				matches := episodeRangeRegex.FindStringSubmatch(seed.ZName)
				if len(matches) >= 3 {
					rEnd, _ = strconv.Atoi(matches[2])
				}
			}

			magnet := seed.ZLink
			if magnet == "" && seed.Down != "" {
				magnet = mukakuBaseURL + seed.Down
			}

			pageInfo.Resources = append(pageInfo.Resources, ResourceInfo{
				ResourceTitle: seed.ZName,
				Magnet:        magnet,
				Size:          seed.ZSize,
				Bytes:         parseSizeToBytes(seed.ZSize),
				resType:       rType,
				fullEpCount:   fullEp,
				rangeStart:    rStart,
				rangeEnd:      rEnd,
				singleEp:      singleEp,
				titleRaw:      seed.ZName,
				SeedTime:      seedTime,
				DetailPath:    fmt.Sprintf("/tr/%d.html", seed.ID),
			})
		}
	}

	pageInfo.Resources = sortResources(pageInfo.Resources)
	return pageInfo, nil
}
