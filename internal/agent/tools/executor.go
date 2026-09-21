package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Executor 工具执行器:全局并发信号量 + 单工具超时 + 幂等重试 + 连续失败熔断。
type Executor struct {
	registry *Registry
	sem      chan struct{} // 全局信号量,容量=最大并发
	timeout  time.Duration // 单工具超时
	retryMax int           // 幂等工具额外重试次数
	pauseTh  int           // 连续失败阈值
	pauseDur time.Duration // 熔断暂停时长

	mu        sync.Mutex
	circuits  map[string]*toolCircuit
	validator *validator
}

// toolCircuit 单个工具的熔断状态。
type toolCircuit struct {
	failCount   int
	pausedUntil time.Time
}

// Options 执行器配置。
type Options struct {
	ToolConcurrency          int           // 全局最大并发
	ToolTimeout              time.Duration // 单工具超时
	ToolRetry                int           // 幂等工具重试次数
	ToolCircuitFailThreshold int           // 连续失败 N 次熔断
	ToolCircuitPause         time.Duration // 熔断暂停时长
}

// NewExecutor 构造执行器。
func NewExecutor(registry *Registry, opts Options) *Executor {
	return &Executor{
		registry:  registry,
		sem:       make(chan struct{}, opts.ToolConcurrency),
		timeout:   opts.ToolTimeout,
		retryMax:  opts.ToolRetry,
		pauseTh:   opts.ToolCircuitFailThreshold,
		pauseDur:  opts.ToolCircuitPause,
		circuits:  map[string]*toolCircuit{},
		validator: newValidator(),
	}
}

// Execute 执行工具:参数校验 -> 熔断检查 -> (信号量+超时) -> 幂等重试。
func (x *Executor) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	tool, ok := x.registry.Get(name)
	if !ok {
		return "", fmt.Errorf("工具不存在: %s", name)
	}
	// 参数校验失败是调用方问题,不计入工具失败熔断
	if err := x.validator.validate(tool.Parameters, args); err != nil {
		return "", err
	}
	// 熔断检查放在获取信号量之前,避免暂停中的工具占满并发槽拖垮其他工具
	if err := x.checkCircuit(name); err != nil {
		return "", err
	}

	attempts := 1
	if tool.IsIdempotent {
		attempts += x.retryMax
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if !sleep(ctx, backoff(i)) {
				return "", ctx.Err()
			}
		}
		out, err := x.runOnce(ctx, tool, args)
		if err == nil {
			x.circuitSuccess(name)
			return out, nil
		}
		lastErr = err
	}
	// 一次调用整体失败计一次失败,重试次数不重复累计
	x.circuitFailure(name)
	return "", fmt.Errorf("工具 %s 执行失败: %w", name, lastErr)
}

// runOnce 单次执行:信号量限流 + 单工具超时(超时后释放信号量,工具仍在后台跑完)。
func (x *Executor) runOnce(ctx context.Context, tool *Tool, args json.RawMessage) (string, error) {
	select {
	case x.sem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-x.sem }()

	runCtx, cancel := context.WithTimeout(ctx, x.timeout)
	defer cancel()

	type res struct {
		out string
		err error
	}
	resCh := make(chan res, 1)
	// 为什么放 goroutine:真正执行中的工具无法被中断,超时只能"提前返回",
	// 通过 goroutine + select 实现超时 kill,同时及时释放信号量。
	go func() {
		var r res
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					r = res{err: fmt.Errorf("工具 panic: %v", rec)}
				}
			}()
			r.out, r.err = tool.Execute(runCtx, args)
		}()
		resCh <- r
	}()

	select {
	case <-runCtx.Done():
		if ctx.Err() != nil {
			return "", ctx.Err() // 父上下文取消,非工具超时
		}
		return "", fmt.Errorf("工具执行超时(>%s)", x.timeout)
	case r := <-resCh:
		return r.out, r.err
	}
}

// checkCircuit 熔断检查:暂停期间直接拒绝;仅当一次暂停确实到期后才重置计数,给工具新的机会。
func (x *Executor) checkCircuit(name string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	c, ok := x.circuits[name]
	if !ok {
		x.circuits[name] = &toolCircuit{}
		return nil
	}
	if c.pausedUntil.After(time.Now()) {
		return fmt.Errorf("工具 %s 已暂停(连续失败熔断),%s 后恢复",
			name, time.Until(c.pausedUntil).Round(time.Second))
	}
	// 暂停到期:重置失败计数(从未暂停过的工具,计数器保持累计)
	if !c.pausedUntil.IsZero() {
		c.failCount = 0
		c.pausedUntil = time.Time{}
	}
	return nil
}

func (x *Executor) circuitSuccess(name string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if c, ok := x.circuits[name]; ok {
		c.failCount = 0
	}
}

func (x *Executor) circuitFailure(name string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	c, ok := x.circuits[name]
	if !ok {
		c = &toolCircuit{}
		x.circuits[name] = c
	}
	c.failCount++
	if c.failCount >= x.pauseTh {
		c.pausedUntil = time.Now().Add(x.pauseDur)
	}
}
