package cachetool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	agenttools "agent-platform/internal/agent/tools"

	"github.com/redis/go-redis/v9"
)

// newExecutor 构造带 cache 工具的执行器。
func newExecutor(t *testing.T, svc *Service) *agenttools.Executor {
	t.Helper()
	reg := agenttools.NewRegistry()
	for _, tool := range svc.Tools() {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("注册工具: %v", err)
		}
	}
	return agenttools.NewExecutor(reg, agenttools.Options{
		ToolConcurrency:          3,
		ToolTimeout:              5 * time.Second,
		ToolRetry:                2,
		ToolCircuitFailThreshold: 3,
		ToolCircuitPause:         30 * time.Second,
	})
}

// TestCacheTools 本地 Redis 上的 cache_set / cache_get 端到端。
func TestCacheTools(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", PoolSize: 4})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("本地 Redis 不可用,跳过")
	}
	defer rdb.Close()
	defer rdb.Del(ctx, "cachetool-test:k1")

	prefix := "cachetool-test:"
	svc := New(rdb, prefix)
	ex := newExecutor(t, svc)

	// cache_set
	out, err := ex.Execute(ctx, "cache_set", mustArgs(t, map[string]any{"key": "k1", "value": "hello"}))
	if err != nil || !strings.Contains(out, "已写入") {
		t.Fatalf("cache_set 失败: out=%q err=%v", out, err)
	}
	// cache_get 命中
	out, err = ex.Execute(ctx, "cache_get", mustArgs(t, map[string]any{"key": "k1"}))
	if err != nil || !strings.Contains(out, "hello") {
		t.Fatalf("cache_get 应命中: out=%q err=%v", out, err)
	}
	// cache_get 未命中
	out, err = ex.Execute(ctx, "cache_get", mustArgs(t, map[string]any{"key": "missing"}))
	if err != nil || !strings.Contains(out, "未命中") {
		t.Fatalf("cache_get 未命中应提示: out=%q err=%v", out, err)
	}
	// 工具数量
	if n := len(svc.Tools()); n != 2 {
		t.Fatalf("应有 2 个缓存工具,实际 %d", n)
	}
}

func mustArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
