package shared

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"
)

// doWithRetry 带重试的 HTTP 请求内部实现。
// 仅对网络错误和 5xx 重试；4xx（404/403/...）是客户端错误，重试也是徒劳，立刻放弃。
func doWithRetry(ctx context.Context, client *http.Client, method, url string, body []byte, contentType string,
	userAgents []string, retryMax int, retryInterval time.Duration) (*http.Response, error) {

	var resp *http.Response
	var err error

	for i := 0; i <= retryMax; i++ {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("上下文超时")
		}

		var req *http.Request
		if body != nil {
			// bytes.Reader 每次从头读取，重试时 body 可复用
			req, err = http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
		} else {
			req, err = http.NewRequestWithContext(ctx, method, url, nil)
		}
		if err != nil {
			return nil, fmt.Errorf("构造请求失败: %w", err)
		}
		req.Header.Set("User-Agent", userAgents[i%len(userAgents)])
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}

		resp, err = client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		// 3xx：重定向跟随由 client.Do 内部完成（正常情况不会浮到这里）；
		// 仅当调用方设置 ErrUseLastResponse 手动跟随重定向时，把响应原样返回
		if err == nil && resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return resp, nil
		}

		// 4xx：客户端错误，立即放弃
		if err == nil && resp.StatusCode >= 400 && resp.StatusCode < 500 {
			status := resp.StatusCode
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d: %s", status, url)
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

// HTTPGetWithRetry 带重试的 HTTP GET 请求。
// 仅对网络错误和 5xx 重试；4xx（404/403/...）是客户端错误，重试也是徒劳，立刻放弃。
func HTTPGetWithRetry(ctx context.Context, client *http.Client, url string,
	userAgents []string, retryMax int, retryInterval time.Duration) (*http.Response, error) {
	return doWithRetry(ctx, client, http.MethodGet, url, nil, "", userAgents, retryMax, retryInterval)
}

// HTTPPostWithRetry 带重试的 HTTP POST 请求（表单提交）。
// 重试策略与 HTTPGetWithRetry 相同。
func HTTPPostWithRetry(ctx context.Context, client *http.Client, url, contentType string, body []byte,
	userAgents []string, retryMax int, retryInterval time.Duration) (*http.Response, error) {
	return doWithRetry(ctx, client, http.MethodPost, url, body, contentType, userAgents, retryMax, retryInterval)
}
