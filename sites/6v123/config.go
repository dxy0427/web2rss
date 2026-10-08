// Go 包名不能以数字开头，目录名 6v123 对应包名 sixv123。
package sixv123

const (
	baseURL   = "https://www.hao6v.org"      // 6v电影网新版（旧版 66 影视）
	searchURL = baseURL + "/e/search/so.php" // 站内搜索（POST 表单，结果页由 searchid 重定向给出）
)
