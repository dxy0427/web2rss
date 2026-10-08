package mukaku

// API 通用响应
type ApiResponse struct {
	Code    int    `json:"code"`
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// 影视详情响应
type DetailResponse struct {
	ApiResponse
	Data *Movie `json:"data"`
}

// 搜索响应
type SearchResponse struct {
	ApiResponse
	Data *SearchData `json:"data"`
}

type SearchData struct {
	Total int            `json:"total"`
	Data  []SearchResult `json:"data"`
}

type SearchResult struct {
	ID     int    `json:"id"`
	IDCode string `json:"idcode"`
	Title  string `json:"title"`
	Years  string `json:"years"`
	DoubID int    `json:"doub_id"`
}

type Movie struct {
	ID            int               `json:"id"`
	IDCode        string            `json:"idcode"`
	Title         string            `json:"title"`
	Image         string            `json:"image"`
	Years         string            `json:"years"`
	Alias         string            `json:"alias"`
	Abstract      string            `json:"abstract"`
	Ecca          map[string][]Seed `json:"ecca"`
	Arrare        []string          `json:"arrare"`
	Znum          int               `json:"znum"`            // 种子总数
	Type          int               `json:"type"`            // 影片类型（1 电影，其余为剧集等）
	SeedUpdatedAt string            `json:"seed_updated_at"` // 最近种子更新时间 "2006-01-02 15:04:05"
}

// Seed ecca 中的种子
type Seed struct {
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

// TListResponse 公开种子流响应（getTList）
type TListResponse struct {
	ApiResponse
	Data TListData `json:"data"`
}

type TListData struct {
	List  []TSeed `json:"list"`
	Total int     `json:"total"`
}

// TSeed 公开种子流中的单条种子
type TSeed struct {
	ID    int    `json:"id"`
	AURL1 string `json:"aurl1"` // 所属影片页路径 /mv/<idcode>
	ZName string `json:"zname"`
	ZSize string `json:"zsize"`
	ZLink string `json:"zlink"` // 磁力链接
	Down  string `json:"down"`  // 种子下载直链
	EZT   int64  `json:"ezt"`   // 种子时间（Unix 秒）
}
