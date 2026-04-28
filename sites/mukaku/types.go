package mukaku

// API 响应结构
type ApiResponse struct {
	Code    int    `json:"code"`
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    *Movie `json:"data"`
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
