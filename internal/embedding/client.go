// Package embedding 提供调 Python embedding 服务的 Go client(§8.7)。
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"time"
)

// Client 调 Python embedding 服务。批量按 batchSize 自动切分,失败重试带退避+jitter。
type Client struct {
	baseURL   string
	http      *http.Client
	batchSize int
	retryMax  int
}

// NewClient 构造 client。
// 为什么自带 Transport:显式设置连接池复用参数,避免默认值对高并发 embedding 调用不利。
func NewClient(baseURL string, batchSize int, timeout time.Duration, retryMax int) *Client {
	return &Client{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		batchSize: batchSize,
		retryMax:  retryMax,
	}
}

// Embed 批量文本转向量:按 batchSize 自动切分,逐批串行调用并拼接结果。
// 返回的向量按输入顺序对齐。
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	result := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += c.batchSize {
		end := min(start+c.batchSize, len(texts))
		vecs, err := c.callWithRetry(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("embedding 批次 [%d,%d): %w", start, end, err)
		}
		result = append(result, vecs...)
	}
	return result, nil
}

// callWithRetry 单批调用:仅可重试错误(5xx/网络/超时)触发重试,4xx 直接返回。
func (c *Client) callWithRetry(ctx context.Context, texts []string) ([][]float32, error) {
	var lastErr error
	for attempt := 0; attempt <= c.retryMax; attempt++ {
		if attempt > 0 {
			if !sleep(ctx, backoff(attempt)) {
				return nil, ctx.Err()
			}
		}
		vecs, err := c.callOnce(ctx, texts)
		if err == nil {
			return vecs, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("embedding 调用失败(已重试 %d 次): %w", c.retryMax, lastErr)
}

// callOnce 单次 HTTP 调用,并校验返回条数与请求一致。
func (c *Client) callOnce(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"texts": texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 embedding 服务: %w", err) // 网络/超时,可重试
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		return nil, &httpError{status: resp.StatusCode, body: buf.String()}
	}

	var out struct {
		Vectors [][]float64 `json:"vectors"`
		Dim     int         `json:"dim"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析 embedding 响应: %w", err)
	}
	if len(out.Vectors) != len(texts) {
		return nil, fmt.Errorf("embedding 返回条数 %d != 请求 %d", len(out.Vectors), len(texts))
	}
	vecs := make([][]float32, len(out.Vectors))
	for i, v := range out.Vectors {
		f := make([]float32, len(v))
		for j, x := range v {
			f[j] = float32(x)
		}
		vecs[i] = f
	}
	return vecs, nil
}

// httpError 非 200 响应的错误,携带状态码与响应体,便于区分可重试与不可重试。
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("embedding 服务返回 %d: %s", e.status, e.body)
}

// isRetryable 判断错误是否值得重试:HTTP 5xx 可重试,4xx 不重试,网络/超时错误可重试。
func isRetryable(err error) bool {
	var he *httpError
	if errors.As(err, &he) {
		return he.status >= 500
	}
	return true
}

// backoff 指数退避 + 随机 jitter,缓解重试风暴。
func backoff(attempt int) time.Duration {
	base := 100 * time.Millisecond
	nominal := base << (attempt - 1) // 100ms,200ms,400ms...
	return nominal + time.Duration(rand.Int63n(int64(nominal)))
}

// sleep 可被 ctx 中断的睡眠,返回 false 表示 ctx 已取消。
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
