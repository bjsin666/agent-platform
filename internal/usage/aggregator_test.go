package usage

import (
	"context"
	"os"
	"testing"
	"time"

	"agent-platform/internal/agent/llm"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestP95 95 分位计算。
func TestP95(t *testing.T) {
	if p95(nil) != 0 {
		t.Fatal("空样本 p95 应为 0")
	}
	ms := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	// 排序后 idx = 9(9.5 取整),值 100
	if got := p95(ms); got != 100 {
		t.Fatalf("p95 错误: %d", got)
	}
	// 单元素
	if p95([]int64{42}) != 42 {
		t.Fatal("单元素 p95 错误")
	}
}

// TestAggregatorRecord 聚合累计正确。
func TestAggregatorRecord(t *testing.T) {
	a := NewAggregator(nil, time.Hour)
	now := time.Now().Format("2006-01-02")
	a.Record(context.Background(), "t1", llm.Usage{PromptTokens: 10, CompletionTokens: 5}, 100*time.Millisecond)
	a.Record(context.Background(), "t1", llm.Usage{PromptTokens: 20, CompletionTokens: 10}, 200*time.Millisecond)

	a.mu.Lock()
	b, ok := a.buckets["t1|"+now]
	a.mu.Unlock()
	if !ok {
		t.Fatal("应有聚合桶")
	}
	if b.calls != 2 || b.promptTokens != 30 || b.completionTok != 15 {
		t.Fatalf("聚合累计错误: %+v", b)
	}
	if len(b.latencies) != 2 {
		t.Fatalf("延迟样本错误: %v", b.latencies)
	}
}

// TestAggregatorFlush 落库到真实 MySQL(env-guarded)。验证 upsert 累加。
func TestAggregatorFlush(t *testing.T) {
	if os.Getenv("KB_TEST_INTEGRATION") != "1" {
		t.Skip("设置 KB_TEST_INTEGRATION=1 运行用量落库集成测试")
	}
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		t.Skip("未设置 MYSQL_DSN")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接 MySQL: %v", err)
	}
	ctx := context.Background()
	a := NewAggregator(db, time.Hour)
	date := time.Now().Format("2006-01-02")
	a.Record(ctx, "test-tenant", llm.Usage{PromptTokens: 50, CompletionTokens: 25}, 80*time.Millisecond)
	a.Flush(ctx)

	var calls int64
	if err := db.Raw(
		"SELECT llm_calls FROM usage_stats WHERE tenant_id='test-tenant' AND date=?",
		date,
	).Scan(&calls).Error; err != nil || calls != 1 {
		t.Fatalf("usage_stats 未写入: calls=%d err=%v", calls, err)
	}
	db.Exec("DELETE FROM usage_stats WHERE tenant_id='test-tenant' AND date=?", date)
}
