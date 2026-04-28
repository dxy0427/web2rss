package main

import "time"

const (
	resTypeFull   = 0
	resTypeRange  = 1
	resTypeSingle = 2
	resTypeOther  = 9
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
	resType       int
	fullEpCount   int
	rangeStart    int
	rangeEnd      int
	singleEp      int
	titleRaw      string
	SeedTime      time.Time
	DetailPath    string
}

// Mukaku API 响应结构
type MukakuResponse struct {
	Code    int          `json:"code"`
	Success bool         `json:"success"`
	Message string       `json:"message"`
	Data    *MukakuMovie `json:"data"`
}

type MukakuMovie struct {
	ID       int                     `json:"id"`
	IDCode   string                  `json:"idcode"`
	Title    string                  `json:"title"`
	Image    string                  `json:"image"`
	Years    string                  `json:"years"`
	Alias    string                  `json:"alias"`
	Abstract string                  `json:"abstract"`
	Ecca     map[string][]MukakuSeed `json:"ecca"`
	Arrare   []string                `json:"arrare"`
}

type MukakuSeed struct {
	ID              int    `json:"id"`
	ZName           string `json:"zname"`
	ZSize           string `json:"zsize"`
	ZLink           string `json:"zlink"`
	Down            string `json:"down"`
	ZQXD            string `json:"zqxd"`
	EZT             string `json:"ezt"`
	DefinitionGroup string `json:"definition_group"`
	New             int    `json:"new"`
}
