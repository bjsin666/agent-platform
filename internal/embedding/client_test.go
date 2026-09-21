package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient 构造带默认测试参数的 client,便于各用例覆盖边界。
func newTestClient(baseURL string) *Client {
	return &Client{
		baseURL:   baseURL,
		http:      &http.Client{Timeout: 5 * time.Second},
		batchSize: 32,
		retryMax:  2,
	}
}

// TestEmbedSuccess 验证正常解析与批量拼接。
func TestEmbedSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Texts []string `json:"texts"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		vectors := make([][]float64, len(req.Texts))
		for i := range req.Texts {
			vectors[i] = []float64{1.0, 0.5, -0.25}
		}
		json.NewEncoder(w).Encode(map[string]any{"vectors": vectors, "dim": 3})
	}))
	defer srv.Close()

	vecs, err := newTestClient(srv.URL).Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed 失败: %v", err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 3 {
		t.Fatalf("向量形状错误: %v", vecs)
	}
	if vecs[0][1] != 0.5 {
		t.Fatalf("向量值错误: %v", vecs[0])
	}
}

// TestEmbedEmptyInput 空输入直接返回 nil,不发请求。
func TestEmbedEmptyInput(t *testing.T) {
	vecs, err := newTestClient("http://127.0.0.1:1").Embed(context.Background(), nil)
	if err != nil || vecs != nil {
		t.Fatalf("空输入应返回 nil,nil;got vecs=%v err=%v", vecs, err)
	}
}

// TestEmbedBatchSplit 40 条文本按 batchSize=32 切成 32+8 两批。
func TestEmbedBatchSplit(t *testing.T) {
	var mu sync.Mutex
	got := []int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Texts []string `json:"texts"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		got = append(got, len(req.Texts))
		mu.Unlock()
		vectors := make([][]float64, len(req.Texts))
		for i := range req.Texts {
			vectors[i] = []float64{1, 2, 3}
		}
		json.NewEncoder(w).Encode(map[string]any{"vectors": vectors, "dim": 3})
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.batchSize = 32
	vecs, err := c.Embed(context.Background(), make([]string, 40))
	if err != nil {
		t.Fatalf("Embed 失败: %v", err)
	}
	if len(vecs) != 40 {
		t.Fatalf("返回条数应为 40,得到 %d", len(vecs))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != 32 || got[1] != 8 {
		t.Fatalf("批次切分错误,got=%v", got)
	}
}

// TestEmbedRetryThenSuccess 前两次 500,第三次成功,验证重试后恢复。
func TestEmbedRetryThenSuccess(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) <= 2 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"vectors": [][]float64{{1, 2, 3}}, "dim": 3})
	}))
	defer srv.Close()

	vecs, err := newTestClient(srv.URL).Embed(context.Background(), []string{"x"})
	if err != nil {
		t.Fatalf("Embed 失败: %v", err)
	}
	if len(vecs) != 1 {
		t.Fatalf("返回条数错误")
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("应重试 2 次(共 3 次请求),实际 %d 次", got)
	}
}

// TestEmbedRetriesExhausted 始终 500,重试耗尽后返回错误。
func TestEmbedRetriesExhausted(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL).Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("期望错误,得到 nil")
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("应尝试 3 次,实际 %d", got)
	}
}

// TestEmbed400NotRetried 4xx 不重试,只请求一次。
func TestEmbed400NotRetried(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "bad text", http.StatusBadRequest)
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL).Embed(context.Background(), []string{"   "})
	if err == nil {
		t.Fatal("期望 400 错误,得到 nil")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("400 不应重试,实际 %d 次请求", got)
	}
}

// TestEmbedTimeout 服务端慢于 client 超时,应返回超时错误。关闭重试聚焦超时路径。
func TestEmbedTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		json.NewEncoder(w).Encode(map[string]any{"vectors": [][]float64{{1}}, "dim": 1})
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.http.Timeout = 300 * time.Millisecond
	c.retryMax = 0
	_, err := c.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("期望超时错误,得到 nil")
	}
}
