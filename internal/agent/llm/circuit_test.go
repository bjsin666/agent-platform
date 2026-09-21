package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestCircuitBreakerLifecycle 状态机:closed -> open -> half-open -> closed。
func TestCircuitBreakerLifecycle(t *testing.T) {
	cb := newCircuitBreaker(3, 50*time.Millisecond, 1)
	if !cb.allow() {
		t.Fatal("初始应 closed 放行")
	}
	cb.failure()
	cb.failure()
	cb.failure()
	if cb.allow() {
		t.Fatal("连续失败 3 次后应熔断(open)")
	}
	time.Sleep(60 * time.Millisecond) // 等待 open 到期
	if !cb.allow() {
		t.Fatal("open 到期应进入 half-open 放行探测")
	}
	if cb.allow() {
		t.Fatal("half-open 只放行 1 个探测请求")
	}
	cb.success()
	if !cb.allow() {
		t.Fatal("探测成功应恢复 closed")
	}
}

// TestCircuitBreakerProbeFailReopens 探测失败应重新熔断。
func TestCircuitBreakerProbeFailReopens(t *testing.T) {
	cb := newCircuitBreaker(2, 50*time.Millisecond, 1)
	cb.failure()
	cb.failure() // open
	time.Sleep(60 * time.Millisecond)
	if !cb.allow() {
		t.Fatal("应进入 half-open")
	}
	cb.failure() // 探测失败
	if cb.allow() {
		t.Fatal("探测失败应重新 open")
	}
}

// TestChatCircuitOpen 熔断打开后请求不再打到服务端。
func TestChatCircuitOpen(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newMockClient(srv.URL)
	c.retryMax = 0
	c.cb = newCircuitBreaker(2, time.Minute, 1)
	req := ChatRequest{Messages: []Message{{Role: RoleUser, Content: "a"}}}

	if _, err := c.Chat(context.Background(), req); err == nil {
		t.Fatal("第一次应失败")
	}
	if _, err := c.Chat(context.Background(), req); err == nil {
		t.Fatal("第二次应失败并触发熔断")
	}
	before := atomic.LoadInt32(&attempts) // 应为 2
	if _, err := c.Chat(context.Background(), req); err == nil {
		t.Fatal("熔断打开后应直接拒绝")
	}
	if got := atomic.LoadInt32(&attempts); got != before {
		t.Fatalf("熔断期间不应请求服务端,before=%d after=%d", before, got)
	}
}

// TestChatCircuitHalfOpenRecovery open 到期后 half-open 探测成功则恢复。
func TestChatCircuitHalfOpenRecovery(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&attempts) < 2 {
			atomic.AddInt32(&attempts, 1)
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		atomic.AddInt32(&attempts, 1)
		json.NewEncoder(w).Encode(echoResp("recovered"))
	}))
	defer srv.Close()

	c := newMockClient(srv.URL)
	c.retryMax = 0
	c.cb = newCircuitBreaker(2, 100*time.Millisecond, 1)
	req := ChatRequest{Messages: []Message{{Role: RoleUser, Content: "a"}}}

	if _, err := c.Chat(context.Background(), req); err == nil {
		t.Fatal("第一次应失败")
	}
	if _, err := c.Chat(context.Background(), req); err == nil {
		t.Fatal("第二次应失败并熔断")
	}
	time.Sleep(150 * time.Millisecond) // 等待 open 到期

	resp, err := c.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("half-open 探测应成功: %v", err)
	}
	if resp.Message.Content != "recovered" {
		t.Fatalf("content 错误: %q", resp.Message.Content)
	}
	if _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatalf("恢复 closed 后应正常: %v", err)
	}
}
