package main

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	idRegex           = regexp.MustCompile(`^\d+$`)
	sizeRegex         = regexp.MustCompile(`\s*\[[\d\.]+(?:GB|MB|TB|KB)\]$`)
	sizeExtractRegex  = regexp.MustCompile(`(?i)(\d+(\.\d+)?)\s*([GMK]B)`)
	whitespaceRegex   = regexp.MustCompile(`[\s\t\n\r]+`)
	episodeFullRegex  = regexp.MustCompile(`\[\s*全(\d+)集\s*\]`)
	episodeRangeRegex = regexp.MustCompile(`\[\s*第(\d+)\s*-\s*(\d+)\s*集\s*\]`)
	episodeSingleRegex = regexp.MustCompile(`\[\s*第(\d+)\s*集\s*\]`)
	timeLayout        = "2006-01-02 15:04:05"
)

func cleanString(str string) string {
	str = whitespaceRegex.ReplaceAllString(str, " ")
	return strings.TrimSpace(str)
}

func parseSizeToBytes(sizeStr string) int64 {
	matches := sizeExtractRegex.FindStringSubmatch(strings.ToUpper(sizeStr))
	if len(matches) < 4 {
		return 0
	}
	val, _ := strconv.ParseFloat(matches[1], 64)
	switch matches[3] {
	case "TB":
		return int64(val * 1024 * 1024 * 1024 * 1024)
	case "GB":
		return int64(val * 1024 * 1024 * 1024)
	case "MB":
		return int64(val * 1024 * 1024)
	case "KB":
		return int64(val * 1024)
	}
	return 0
}

func extractResourceType(title string) (int, int, int, int) {
	fullMatches := episodeFullRegex.FindStringSubmatch(title)
	if len(fullMatches) >= 2 {
		epCount, _ := strconv.Atoi(fullMatches[1])
		return resTypeFull, epCount, 0, 0
	}
	rangeMatches := episodeRangeRegex.FindStringSubmatch(title)
	if len(rangeMatches) >= 3 {
		start, _ := strconv.Atoi(rangeMatches[1])
		end, _ := strconv.Atoi(rangeMatches[2])
		return resTypeRange, 0, start, end
	}
	singleMatches := episodeSingleRegex.FindStringSubmatch(title)
	if len(singleMatches) >= 2 {
		ep, _ := strconv.Atoi(singleMatches[1])
		return resTypeSingle, 0, 0, ep
	}
	return resTypeOther, 0, 0, 0
}

func sortResources(resources []ResourceInfo) []ResourceInfo {
	if len(resources) <= 1 {
		return resources
	}
	sorted := make([]ResourceInfo, len(resources))
	copy(sorted, resources)

	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].resType != sorted[j].resType {
			return sorted[i].resType < sorted[j].resType
		}
		return sorted[i].titleRaw < sorted[j].titleRaw
	})
	return sorted
}
