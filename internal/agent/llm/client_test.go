package llm

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

// newMockClient 构造指向 mock 服务地址的 client;默认不启用缓存,避免干扰非缓存用例。
func newMockClient(serverURL string) *Client {
	return &Client{
		apiKey:          "test-key",
		baseURL:         serverURL,
		model:           "test-model",
		http:            &http.Client{Timeout: 5 * time.Second},
		timeoutTotal:    60 * time.Second,
		timeoutPerChunk: 5 * time.Second,
		retryMax:        2,
		cb:              newCircuitBreaker(5, 30*time.Second, 1),
		cache:           nil,
		cacheTTL:        time.Hour,
	}
}

// memCache 测试用内存缓存,实现 Cache 接口。
type memCache struct {
	mu sync.Mutex
	m  map[string]memEntry
}

type memEntry struct {
	data []byte
	exp  time.Time
}

func newMemCache() *memCache { return &memCache{m: map[string]memEntry{}} }

func (m *memCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.m[key]
	if !ok || time.Now().After(e.exp) {
		return nil, false, nil
	}
	return e.data, true, nil
}

func (m *memCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.m[key] = memEntry{data: value, exp: time.Now().Add(ttl)}
	return nil
}

// echoResp 便捷构造 mock 响应体。
func echoResp(content string) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": RoleAssistant, "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	}
}

// TestChatSuccess 验证非流式成功调用与 usage 解析。
func TestChatSuccess(t *testing.T) {
	var got wireChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(echoResp("你好"))
	}))
	defer srv.Close()

	resp, err := newMockClient(srv.URL).Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if resp.Message.Content != "你好" {
		t.Fatalf("content 错误: %q", resp.Message.Content)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 {
		t.Fatalf("usage 错误: %+v", resp.Usage)
	}
	if got.Model != "test-model" || got.Stream {
		t.Fatalf("请求体错误: model=%q stream=%v", got.Model, got.Stream)
	}
}

// TestChatToolCalls 验证 tool_calls 从响应透传。
func TestChatToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    RoleAssistant,
					"content": "",
					"tool_calls": []any{map[string]any{
						"id": "call_1", "type": "function",
						"function": map[string]any{"name": "search_kb", "arguments": `{"q":"x"}`},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{},
		})
	}))
	defer srv.Close()

	resp, err := newMockClient(srv.URL).Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "查一下"}},
		Tools:    []ToolSchema{{Type: "function", Function: ToolFunction{Name: "search_kb", Parameters: json.RawMessage(`{"type":"object"}`)}}},
	})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Function.Name != "search_kb" {
		t.Fatalf("tool_calls 透传错误: %+v", resp.Message.ToolCalls)
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason 错误: %q", resp.FinishReason)
	}
}

// TestChatRetryThenSuccess 前两次 500,第三次成功,验证重试后恢复。
func TestChatRetryThenSuccess(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) <= 2 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(echoResp("ok"))
	}))
	defer srv.Close()

	if _, err := newMockClient(srv.URL).Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("应重试 2 次(共 3 次请求),实际 %d", got)
	}
}

// TestChat4xxNotRetried 401 不重试,只请求一次。
func TestChat4xxNotRetried(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "invalid key", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := newMockClient(srv.URL).Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("期望 401 错误,得到 nil")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("4xx 不应重试,实际 %d 次请求", got)
	}
}

// TestChatTimeout 服务端慢于 client 超时,应返回错误。
func TestChatTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		json.NewEncoder(w).Encode(echoResp("slow"))
	}))
	defer srv.Close()

	c := newMockClient(srv.URL)
	c.http.Timeout = 300 * time.Millisecond
	c.retryMax = 0
	_, err := c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("期望超时错误,得到 nil")
	}
}

// TestChatCache 相同消息第二次命中缓存,服务端只请求一次。
func TestChatCache(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		json.NewEncoder(w).Encode(echoResp("cached"))
	}))
	defer srv.Close()

	c := newMockClient(srv.URL)
	c.cache = newMemCache()
	req := ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}
	if _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatalf("第一次 Chat 失败: %v", err)
	}
	if _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatalf("第二次 Chat 失败: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("第二次应命中缓存,服务端仅请求 1 次,实际 %d", got)
	}
}

// TestChatCacheKeyDiff 不同消息不命中缓存。
func TestChatCacheKeyDiff(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		json.NewEncoder(w).Encode(echoResp("x"))
	}))
	defer srv.Close()

	c := newMockClient(srv.URL)
	c.cache = newMemCache()
	c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "a"}}})
	c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "b"}}})
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("不同消息应各请求一次,实际 %d", got)
	}
}
