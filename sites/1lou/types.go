package onelou

// SearchResponse 搜索 API（search.php）响应
type SearchResponse struct {
	OK   bool       `json:"ok"`
	Data SearchData `json:"data"`
}

type SearchData struct {
	Query string `json:"query"`
	Total int    `json:"total"`
	Hits  []Hit  `json:"hits"`
}

// Hit 搜索结果中的单个帖子
type Hit struct {
	TID                int    `json:"tid"`
	FID                int    `json:"fid"`
	Subject            string `json:"subject"`
	HighlightedSubject string `json:"highlighted_subject"`
	Username           string `json:"username"`
	CreateDate         int64  `json:"create_date"`
	Views              int    `json:"views"`
	Posts              int    `json:"posts"`
	Files              int    `json:"files"`
	ThreadURL          string `json:"thread_url"`
}
