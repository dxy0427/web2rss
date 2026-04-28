package shared

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// HTTPGetWithRetry 带重试的 HTTP GET 请求
func HTTPGetWithRetry(ctx context.Context, client *http.Client, url string,
	userAgents []string, retryMax int, retryInterval time.Duration) (*http.Response, error) {

	var resp *http.Response
	var err error

	for i := 0; i <= retryMax; i++ {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("上下文超时")
		}

		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		req.Header.Set("User-Agent", userAgents[i%len(userAgents)])
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

		resp, err = client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			return resp, nil
		}
		if resp != nil {
			resp.Body.Close()
		}

		if i < retryMax {
			time.Sleep(retryInterval * time.Duration(i+1))
		}
	}
	return nil, fmt.Errorf("HTTP 请求失败: %s, 最后错误: %v", url, err)
}
