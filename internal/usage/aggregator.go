// Package usage 用量统计:LLM 调用量按租户/日聚合,定期落库 usage_stats(§8 Governance)。
package usage

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/agent/llm"

	"gorm.io/gorm"
)

// deepSeekCost 成本估算(人民币/百万 token,近似值):输入 1 元,输出 2 元。
// 仅作成本统计,非计费依据。
const (
	costInputPerM  = 1.0
	costOutputPerM = 2.0
)

// Aggregator 按 (tenant, date) 聚合用量,定期 flush 到 usage_stats。
type Aggregator struct {
	db            *gorm.DB
	flushInterval time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket // key: tenant|YYYY-MM-DD
}

// bucket 单日单租户的累计用量。
type bucket struct {
	calls         int
	promptTokens  int
	completionTok int
	latencies     []int64 // ms
}

// NewAggregator 构造聚合器。
func NewAggregator(db *gorm.DB, flushInterval time.Duration) *Aggregator {
	return &Aggregator{
		db:            db,
		flushInterval: flushInterval,
		buckets:       map[string]*bucket{},
	}
}

// Record 记录一次 LLM 调用用量。
func (a *Aggregator) Record(_ context.Context, tenant string, u llm.Usage, latency time.Duration) {
	key := tenant + "|" + time.Now().Format("2006-01-02")
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.buckets[key]
	if !ok {
		b = &bucket{}
		a.buckets[key] = b
	}
	b.calls++
	b.promptTokens += u.PromptTokens
	b.completionTok += u.CompletionTokens
	b.latencies = append(b.latencies, latency.Milliseconds())
}

// Run 定期 flush,直到 ctx 取消。
func (a *Aggregator) Run(ctx context.Context) {
	ticker := time.NewTicker(a.flushInterval)
	defer ticker.Stop()
	slog.Info("用量聚合器启动", "flush_interval", a.flushInterval.String())
	for {
		select {
		case <-ctx.Done():
			a.flush(ctx) // 退出前兜底一次
			return
		case <-ticker.C:
			a.flush(ctx)
		}
	}
}

// Flush 手动触发一次落库(测试用)。
func (a *Aggregator) Flush(ctx context.Context) { a.flush(ctx) }

// flush 把累计数据 upsert 进 usage_stats。
func (a *Aggregator) flush(ctx context.Context) {
	a.mu.Lock()
	pending := a.buckets
	a.buckets = map[string]*bucket{}
	a.mu.Unlock()

	for key, b := range pending {
		parts := strings.SplitN(key, "|", 2)
		if len(parts) != 2 {
			continue
		}
		tenant, date := parts[0], parts[1]
		cost := float64(b.promptTokens)/1e6*costInputPerM +
			float64(b.completionTok)/1e6*costOutputPerM
		if err := a.db.WithContext(ctx).Exec(
			`INSERT INTO usage_stats (tenant_id, date, llm_calls, prompt_tokens, completion_tokens, cost, latency_p95, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, NOW())
			 ON DUPLICATE KEY UPDATE
			   llm_calls = llm_calls + VALUES(llm_calls),
			   prompt_tokens = prompt_tokens + VALUES(prompt_tokens),
			   completion_tokens = completion_tokens + VALUES(completion_tokens),
			   cost = cost + VALUES(cost),
			   latency_p95 = VALUES(latency_p95)`,
			tenant, date, b.calls, b.promptTokens, b.completionTok, cost, p95(b.latencies),
		).Error; err != nil {
			slog.Warn("写入 usage_stats 失败", "tenant", tenant, "date", date, "err", err)
		}
	}
}

// p95 计算延迟 95 分位。
func p95(ms []int64) int64 {
	if len(ms) == 0 {
		return 0
	}
	sorted := append([]int64(nil), ms...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted)) * 0.95)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
