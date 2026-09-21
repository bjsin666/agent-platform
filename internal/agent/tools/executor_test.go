package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testExecutor 构造测试执行器。
func testExecutor(opts Options) (*Executor, *Registry) {
	reg := NewRegistry()
	if opts.ToolConcurrency == 0 {
		opts.ToolConcurrency = 3
	}
	if opts.ToolTimeout == 0 {
		opts.ToolTimeout = 10 * time.Second
	}
	if opts.ToolRetry == 0 {
		opts.ToolRetry = 2
	}
	if opts.ToolCircuitFailThreshold == 0 {
		opts.ToolCircuitFailThreshold = 3
	}
	if opts.ToolCircuitPause == 0 {
		opts.ToolCircuitPause = 30 * time.Second
	}
	return NewExecutor(reg, opts), reg
}

func mustArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return b
}

// TestExecutorConcurrencyLimit 100 个任务并发压测,全局并发不超过信号量容量。
func TestExecutorConcurrencyLimit(t *testing.T) {
	x, reg := testExecutor(Options{ToolConcurrency: 3})
	var cur, max int32
	reg.Register(&Tool{
		Name: "count",
		Execute: func(context.Context, json.RawMessage) (string, error) {
			n := atomic.AddInt32(&cur, 1)
			for {
				m := atomic.LoadInt32(&max)
				if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			atomic.AddInt32(&cur, -1)
			return "ok", nil
		},
	})

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := x.Execute(ctx, "count", nil); err != nil {
				t.Errorf("Execute 失败: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&max); got != 3 {
		t.Fatalf("100 任务应被压到最大并发 3,实际 %d", got)
	}
	if cur := atomic.LoadInt32(&cur); cur != 0 {
		t.Fatalf("结束后并发应为 0,实际 %d", cur)
	}
}

// TestExecutorTimeoutKill 慢工具超过单工具超时应报超时,且信号量立即释放。
func TestExecutorTimeoutKill(t *testing.T) {
	x, reg := testExecutor(Options{ToolConcurrency: 1, ToolTimeout: 100 * time.Millisecond})
	reg.Register(&Tool{Name: "slow", Execute: func(context.Context, json.RawMessage) (string, error) {
		time.Sleep(2 * time.Second) // 不感知 ctx,模拟失控工具
		return "done", nil
	}})
	reg.Register(&Tool{Name: "fast", Execute: func(context.Context, json.RawMessage) (string, error) {
		return "fast-ok", nil
	}})

	start := time.Now()
	_, err := x.Execute(context.Background(), "slow", nil)
	if err == nil {
		t.Fatal("期望超时错误")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("错误应含超时提示: %v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("超时后应立即返回,实际耗时 %v", time.Since(start))
	}

	// 信号量已释放:并发槽为 1 时,fast 工具应能立刻执行
	if _, err = x.Execute(context.Background(), "fast", nil); err != nil {
		t.Fatalf("超时后信号量未释放,fast 执行失败: %v", err)
	}
}

// TestExecutorRetryIdempotent 幂等工具失败 2 次后成功,共 3 次尝试。
func TestExecutorRetryIdempotent(t *testing.T) {
	x, reg := testExecutor(Options{ToolRetry: 2})
	var attempts int32
	reg.Register(&Tool{
		Name: "flaky", IsIdempotent: true,
		Execute: func(context.Context, json.RawMessage) (string, error) {
			if atomic.AddInt32(&attempts, 1) <= 2 {
				return "", errors.New("boom")
			}
			return "ok", nil
		},
	})

	out, err := x.Execute(context.Background(), "flaky", nil)
	if err != nil {
		t.Fatalf("幂等重试后应成功: %v", err)
	}
	if out != "ok" {
		t.Fatalf("输出错误: %q", out)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("应尝试 3 次,实际 %d", got)
	}
}

// TestExecutorNoRetryNonIdempotent 非幂等工具只执行一次。
func TestExecutorNoRetryNonIdempotent(t *testing.T) {
	x, reg := testExecutor(Options{ToolRetry: 2})
	var attempts int32
	reg.Register(&Tool{
		Name: "nonidem", IsIdempotent: false,
		Execute: func(context.Context, json.RawMessage) (string, error) {
			atomic.AddInt32(&attempts, 1)
			return "", errors.New("boom")
		},
	})

	_, err := x.Execute(context.Background(), "nonidem", nil)
	if err == nil {
		t.Fatal("期望失败")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("非幂等不应重试,实际 %d 次", got)
	}
}

// TestExecutorCircuitBreak 连续失败达到阈值后暂停,期间不再调用工具。
func TestExecutorCircuitBreak(t *testing.T) {
	x, reg := testExecutor(Options{ToolConcurrency: 1, ToolRetry: 0, ToolCircuitFailThreshold: 3, ToolCircuitPause: time.Minute})
	var calls int32
	reg.Register(&Tool{Name: "broken", Execute: func(context.Context, json.RawMessage) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", errors.New("always fail")
	}})

	for i := 0; i < 3; i++ {
		if _, err := x.Execute(context.Background(), "broken", nil); err == nil {
			t.Fatal("应失败")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("前 3 次应调用工具,实际 %d 次", got)
	}

	// 第 4 次被熔断拦截,不再调用工具
	_, err := x.Execute(context.Background(), "broken", nil)
	if err == nil || !strings.Contains(err.Error(), "已暂停") {
		t.Fatalf("熔断后应返回暂停错误: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("熔断期间不应调用工具,实际 %d 次", got)
	}
}

// TestExecutorCircuitRecover 暂停到期后工具恢复可用。
func TestExecutorCircuitRecover(t *testing.T) {
	x, reg := testExecutor(Options{ToolConcurrency: 1, ToolRetry: 0, ToolCircuitFailThreshold: 2, ToolCircuitPause: 60 * time.Millisecond})
	var calls int32
	reg.Register(&Tool{Name: "recover", Execute: func(context.Context, json.RawMessage) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", errors.New("fail")
	}})

	x.Execute(context.Background(), "recover", nil)
	x.Execute(context.Background(), "recover", nil) // 达到阈值 -> 暂停
	if _, err := x.Execute(context.Background(), "recover", nil); err == nil {
		t.Fatal("应被熔断拦截")
	}

	time.Sleep(80 * time.Millisecond) // 等待暂停到期
	if _, err := x.Execute(context.Background(), "recover", nil); err == nil {
		t.Fatal("恢复后应再次执行并失败(而非被拦截)")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("恢复后应再次调用工具,实际 %d 次", got)
	}
}

// TestExecutorValidation 参数校验失败时工具不被调用。
func TestExecutorValidation(t *testing.T) {
	x, reg := testExecutor(Options{})
	var calls int32
	reg.Register(&Tool{
		Name:       "needs_text",
		Parameters: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Execute: func(context.Context, json.RawMessage) (string, error) {
			atomic.AddInt32(&calls, 1)
			return "ok", nil
		},
	})

	// 缺 text -> 校验失败,工具不执行
	if _, err := x.Execute(context.Background(), "needs_text", mustArgs(t, map[string]any{"foo": 1})); err == nil {
		t.Fatal("缺必填字段应校验失败")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("校验失败不应调用工具,实际 %d 次", got)
	}
	// 类型错误
	if _, err := x.Execute(context.Background(), "needs_text", mustArgs(t, map[string]any{"text": 123})); err == nil {
		t.Fatal("类型错误应校验失败")
	}
	// 合法参数 -> 成功
	if out, err := x.Execute(context.Background(), "needs_text", mustArgs(t, map[string]any{"text": "hi"})); err != nil || out != "ok" {
		t.Fatalf("合法参数应成功: out=%q err=%v", out, err)
	}
	// 非法 JSON -> 校验失败
	if _, err := x.Execute(context.Background(), "needs_text", json.RawMessage(`{bad`)); err == nil {
		t.Fatal("非法 JSON 应失败")
	}
}

// TestExecutorMissingTool 工具不存在时报错。
func TestExecutorMissingTool(t *testing.T) {
	x, _ := testExecutor(Options{})
	if _, err := x.Execute(context.Background(), "nope", nil); err == nil {
		t.Fatal("不存在的工具应报错")
	}
}

// TestExecutorPanic 工具 panic 不应击穿执行器,返回错误。
func TestExecutorPanic(t *testing.T) {
	x, reg := testExecutor(Options{ToolRetry: 0})
	reg.Register(&Tool{Name: "panic", Execute: func(context.Context, json.RawMessage) (string, error) {
		panic("boom")
	}})
	_, err := x.Execute(context.Background(), "panic", nil)
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("panic 应转为错误: %v", err)
	}
	// 执行器仍可用
	reg.Register(&Tool{Name: "ok", Execute: func(context.Context, json.RawMessage) (string, error) { return "ok", nil }})
	if _, err := x.Execute(context.Background(), "ok", nil); err != nil {
		t.Fatalf("panic 后执行器应可用: %v", err)
	}
}
