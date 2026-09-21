package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestChatStreamDeltas 多块增量按序拼接,收到 [DONE] 正常结束。
func TestChatStreamDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		events := []string{
			`data: {"choices":[{"index":0,"delta":{"content":"你"},"finish_reason":null}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"好"},"finish_reason":null}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"世界"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		}
		for _, e := range events {
			io.WriteString(w, e+"\n\n")
			flusher.Flush()
		}
	}))
	defer srv.Close()

	var sb strings.Builder
	err := newMockClient(srv.URL).ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Stream:   true,
	}, func(d string) { sb.WriteString(d) })
	if err != nil {
		t.Fatalf("ChatStream 失败: %v", err)
	}
	if sb.String() != "你好世界" {
		t.Fatalf("流式拼接错误: %q", sb.String())
	}
}

// TestChatStreamToolCallsIgnored 流式 tool_calls 增量不产生 content,且正常结束。
func TestChatStreamToolCallsIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		events := []string{
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"search_kb","arguments":""}}]},"finish_reason":null}]}`,
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`data: [DONE]`,
		}
		for _, e := range events {
			io.WriteString(w, e+"\n\n")
			flusher.Flush()
		}
	}))
	defer srv.Close()

	var got []string
	err := newMockClient(srv.URL).ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Stream:   true,
	}, func(d string) { got = append(got, d) })
	if err != nil {
		t.Fatalf("ChatStream 失败: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("tool_calls 流不应产生 content,got=%v", got)
	}
}

// TestChatStreamIdleTimeout 块间空闲超过 timeoutPerChunk 应报错。
func TestChatStreamIdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}`+"\n\n")
		flusher.Flush()
		time.Sleep(2 * time.Second) // 超出块间空闲超时,再无数据
		io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := newMockClient(srv.URL)
	c.timeoutPerChunk = 300 * time.Millisecond
	err := c.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Stream:   true,
	}, func(string) {})
	if err == nil {
		t.Fatal("期望流式空闲超时错误,得到 nil")
	}
}

// TestChatStreamServerError 服务端返回 500,流式调用应报错。
func TestChatStreamServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := newMockClient(srv.URL).ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Stream:   true,
	}, func(string) {})
	if err == nil {
		t.Fatal("期望错误,得到 nil")
	}
}

// TestChatStreamFullToolCalls ChatStreamFull 能按 index 聚合流式工具调用。
func TestChatStreamFullToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		events := []string{
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"search_kb","arguments":""}}]},"finish_reason":null}]}`,
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]},"finish_reason":null}]}`,
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`,
			`data: [DONE]`,
		}
		for _, e := range events {
			io.WriteString(w, e+"\n\n")
			flusher.Flush()
		}
	}))
	defer srv.Close()

	var streamed []string
	resp, err := newMockClient(srv.URL).ChatStreamFull(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Stream:   true,
	}, func(d string) { streamed = append(streamed, d) })
	if err != nil {
		t.Fatalf("ChatStreamFull 失败: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("应聚合 1 个工具调用: %+v", resp.Message.ToolCalls)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "search_kb" || tc.Function.Arguments != `{"q":"x"}` {
		t.Fatalf("工具调用聚合错误: %+v", tc)
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason 错误: %q", resp.FinishReason)
	}
	if resp.Usage.TotalTokens != 13 {
		t.Fatalf("usage 错误: %+v", resp.Usage)
	}
	if len(streamed) != 0 {
		t.Fatalf("工具轮不应有内容增量: %v", streamed)
	}
}

// TestChatStreamFullForcesStream 回归:调用方未设 Stream 时 ChatStreamFull 仍必须流式发送。
func TestChatStreamFullForcesStream(t *testing.T) {
	var gotStream bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req wireChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		gotStream = req.Stream
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`+"\n\n")
		flusher.Flush()
		io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := newMockClient(srv.URL)
	resp, err := c.ChatStreamFull(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}}, // 故意不设 Stream
	}, func(string) {})
	if err != nil {
		t.Fatalf("ChatStreamFull 失败: %v", err)
	}
	if !gotStream {
		t.Fatal("请求必须带 stream=true")
	}
	if resp.Message.Content != "hi" {
		t.Fatalf("content 错误: %q", resp.Message.Content)
	}
}

// TestChatStreamNotCached 流式请求不参与缓存,相同消息两次调用服务端均被请求。
func TestChatStreamNotCached(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := newMockClient(srv.URL)
	c.cache = newMemCache()
	req := ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}, Stream: true}
	if err := c.ChatStream(context.Background(), req, func(string) {}); err != nil {
		t.Fatalf("第一次 ChatStream 失败: %v", err)
	}
	if err := c.ChatStream(context.Background(), req, func(string) {}); err != nil {
		t.Fatalf("第二次 ChatStream 失败: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("流式不应缓存,服务端应请求 2 次,实际 %d", got)
	}
}
