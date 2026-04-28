package shared

import "time"

const (
	ResTypeFull   = 0
	ResTypeRange  = 1
	ResTypeSingle = 2
	ResTypeOther  = 9
)

// PageInfo 页面抓取结果
type PageInfo struct {
	Title     string
	DetailURL string
	Resources []ResourceInfo
}

// ResourceInfo 单个资源（种子/网盘）
type ResourceInfo struct {
	ResourceTitle string
	Magnet        string
	Size          string
	Bytes         int64
	ResType       int
	FullEpCount   int
	RangeStart    int
	RangeEnd      int
	SingleEp      int
	TitleRaw      string
	SeedTime      time.Time
	DetailPath    string
}
