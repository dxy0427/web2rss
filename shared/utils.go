package shared

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	IDRegex           = regexp.MustCompile(`^\d+$`)
	SizeExtractRegex  = regexp.MustCompile(`(?i)(\d+(\.\d+)?)\s*([GMK]B)`)
	WhitespaceRegex   = regexp.MustCompile(`[\s\t\n\r]+`)
	EpisodeFullRegex  = regexp.MustCompile(`\[\s*全(\d+)集\s*\]`)
	EpisodeRangeRegex = regexp.MustCompile(`\[\s*第(\d+)\s*-\s*(\d+)\s*集\s*\]`)
	EpisodeSingleRegex = regexp.MustCompile(`\[\s*第(\d+)\s*集\s*\]`)
	TimeLayout        = "2006-01-02 15:04:05"
)

func CleanString(str string) string {
	str = WhitespaceRegex.ReplaceAllString(str, " ")
	return strings.TrimSpace(str)
}

func ParseSizeToBytes(sizeStr string) int64 {
	matches := SizeExtractRegex.FindStringSubmatch(strings.ToUpper(sizeStr))
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

func ExtractResourceType(title string) (int, int, int, int) {
	fullMatches := EpisodeFullRegex.FindStringSubmatch(title)
	if len(fullMatches) >= 2 {
		epCount, _ := strconv.Atoi(fullMatches[1])
		return ResTypeFull, epCount, 0, 0
	}
	rangeMatches := EpisodeRangeRegex.FindStringSubmatch(title)
	if len(rangeMatches) >= 3 {
		start, _ := strconv.Atoi(rangeMatches[1])
		end, _ := strconv.Atoi(rangeMatches[2])
		return ResTypeRange, 0, start, end
	}
	singleMatches := EpisodeSingleRegex.FindStringSubmatch(title)
	if len(singleMatches) >= 2 {
		ep, _ := strconv.Atoi(singleMatches[1])
		return ResTypeSingle, 0, 0, ep
	}
	return ResTypeOther, 0, 0, 0
}

func SortResources(resources []ResourceInfo) []ResourceInfo {
	if len(resources) <= 1 {
		return resources
	}
	sorted := make([]ResourceInfo, len(resources))
	copy(sorted, resources)

	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].ResType != sorted[j].ResType {
			return sorted[i].ResType < sorted[j].ResType
		}
		return sorted[i].TitleRaw < sorted[j].TitleRaw
	})
	return sorted
}
