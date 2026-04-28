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
	ID       int    `json:"id"`
	IDCode   string `json:"idcode"`
	Title    string `json:"title"`
	Years    string `json:"years"`
	DoubID   int    `json:"doub_id"`
}

type Movie struct {
	ID       int                `json:"id"`
	IDCode   string             `json:"idcode"`
	Title    string             `json:"title"`
	Image    string             `json:"image"`
	Years    string             `json:"years"`
	Alias    string             `json:"alias"`
	Abstract string             `json:"abstract"`
	Ecca     map[string][]Seed  `json:"ecca"`
	Arrare   []string           `json:"arrare"`
}

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
