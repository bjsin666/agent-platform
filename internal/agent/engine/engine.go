// Package engine 执行引擎:AgentRun(ReAct 循环)与 DAGRun(工作流)双执行模式(§8.3)。
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"agent-platform/internal/agent/llm"
	"agent-platform/internal/agent/tools"
	"agent-platform/internal/trace"
)

// LLM LLM 网关抽象,便于测试注入 mock。
type LLM interface {
	Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
	ChatStream(ctx context.Context, req llm.ChatRequest, onDelta func(string)) error
	// ChatStreamFull 流式并返回聚合结果(含 tool_calls),供 ReAct 循环判断下一轮。
	ChatStreamFull(ctx context.Context, req llm.ChatRequest, onDelta func(string)) (*llm.ChatResponse, error)
}

// MessageRepo 会话消息存取。Phase 5 提供 GORM 实现,测试用内存实现。
type MessageRepo interface {
	// LoadMessages 按会话加载历史消息(不含当前用户消息)。
	LoadMessages(ctx context.Context, sessionID uint64) ([]llm.Message, error)
	// AppendMessage 持久化一条消息。
	AppendMessage(ctx context.Context, sessionID uint64, msg llm.Message) error
}

// Engine 执行引擎,聚合 ReAct 与 DAG 所需依赖。
type Engine struct {
	LLM      LLM
	Registry *tools.Registry
	Executor *tools.Executor
	Messages MessageRepo
	Trace    *trace.Recorder
	Context  ContextManager // 上下文管理(可选;nil 时不裁剪/不压缩)
	Usage    UsageRecorder  // 用量上报(可选;Phase 8)
	MaxIter  int            // ReAct 最大迭代次数
}

// ContextManager 上下文管理抽象,由 context 包实现,避免引擎与具体实现耦合。
type ContextManager interface {
	BuildMessages(msgs []llm.Message) []llm.Message
	OverLimit(msgs []llm.Message) bool
	Compact(ctx context.Context, msgs []llm.Message) []llm.Message
}

// UsageRecorder LLM 用量上报接口(Phase 8 用量统计)。
type UsageRecorder interface {
	Record(ctx context.Context, tenant string, usage llm.Usage, latency time.Duration)
}

// NewEngine 构造引擎。
func NewEngine(llmc LLM, reg *tools.Registry, ex *tools.Executor, repo MessageRepo, recorder *trace.Recorder, maxIter int) *Engine {
	return &Engine{
		LLM:      llmc,
		Registry: reg,
		Executor: ex,
		Messages: repo,
		Trace:    recorder,
		MaxIter:  maxIter,
	}
}

// WithContext 注入上下文管理器(链式,便于组装)。
func (e *Engine) WithContext(mgr ContextManager) *Engine {
	e.Context = mgr
	return e
}

// genID 生成随机 ID,用于 RunID/TraceID/SpanID。
func genID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// WithRunID 把外部生成的 run ID 注入上下文,使 trace 可按 run_id 查询(对应 GET /v1/traces/{run_id})。
func WithRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runIDKey{}, runID)
}

type runIDKey struct{}

// runIDFrom 读取上下文中的 run ID;为空则由 AgentRun 内部生成。
func runIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(runIDKey{}).(string); ok {
		return v
	}
	return ""
}
