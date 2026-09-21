package embedding

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestEmbedReal 直连本机 embedding 服务做端到端验证。
// 默认跳过(不影响 go test ./...);设置 EMBED_TEST_REAL=1 且本机 8001 服务运行时执行。
func TestEmbedReal(t *testing.T) {
	if os.Getenv("EMBED_TEST_REAL") != "1" {
		t.Skip("设置 EMBED_TEST_REAL=1 运行真实服务集成测试")
	}
	c := NewClient("http://127.0.0.1:8001", 32, 10*time.Second, 2)
	vecs, err := c.Embed(context.Background(), []string{"你好", "Agent 平台测试"})
	if err != nil {
		t.Fatalf("Embed 真实服务失败: %v", err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 512 {
		t.Fatalf("期望 2 条 512 维,实际 %d 条 %d 维", len(vecs), len(vecs[0]))
	}
}
