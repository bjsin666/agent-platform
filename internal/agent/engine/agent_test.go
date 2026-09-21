package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/agent/llm"
	"agent-platform/internal/agent/tools"
	"agent-platform/internal/trace"
)

// fakeLLM 顺序返回预设响应;记录每次收到的消息供断言工具结果回填。
type fakeLLM struct {
	mu        sync.Mutex
	responses []*llm.ChatResponse
	calls     int
	gotMsgs   [][]llm.Message
}

func (f *fakeLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotMsgs = append(f.gotMsgs, req.Messages)
	if f.calls < len(f.responses) {
		r := f.responses[f.calls]
		f.calls++
		return r, nil
	}
	f.calls++
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "默认回答"}, FinishReason: "stop"}, nil
}

func (f *fakeLLM) ChatStream(context.Context, llm.ChatRequest, func(string)) error {
	return errors.New("stream not used")
}

// ChatStreamFull 复用 Chat 的顺序响应,并把 content 逐段 onDelta。
func (f *fakeLLM) ChatStreamFull(ctx context.Context, req llm.ChatRequest, onDelta func(string)) (*llm.ChatResponse, error) {
	resp, err := f.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp != nil && resp.Message.Content != "" {
		onDelta(resp.Message.Content)
	}
	return resp, nil
}

func contentResp(s string) *llm.ChatResponse {
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: s}, FinishReason: "stop"}
}

func toolCallResp(name, args, id string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:      llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{ID: id, Type: "function", Function: llm.FunctionCall{Name: name, Arguments: args}}},
		},
		FinishReason: "tool_calls",
	}
}

// memRepo 内存版消息仓库。
type memRepo struct {
	mu   sync.Mutex
	msgs map[uint64][]llm.Message
}

func newMemRepo() *memRepo { return &memRepo{msgs: map[uint64][]llm.Message{}} }

func (m *memRepo) LoadMessages(_ context.Context, sid uint64) ([]llm.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]llm.Message(nil), m.msgs[sid]...), nil
}

func (m *memRepo) AppendMessage(_ context.Context, sid uint64, msg llm.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs[sid] = append(m.msgs[sid], msg)
	return nil
}

// newEngineDeps 构造工具注册表(含内置工具)与执行器。
func newEngineDeps(t *testing.T) (*tools.Registry, *tools.Executor) {
	t.Helper()
	reg := tools.NewRegistry()
	if err := tools.RegisterBuiltins(reg, tools.Deps{}); err != nil {
		t.Fatalf("注册内置工具失败: %v", err)
	}
	ex := tools.NewExecutor(reg, tools.Options{
		ToolConcurrency:          3,
		ToolTimeout:              5 * time.Second,
		ToolRetry:                2,
		ToolCircuitFailThreshold: 3,
		ToolCircuitPause:         30 * time.Second,
	})
	return reg, ex
}

// TestAgentRunNoTools 无工具调用时直接返回最终回答,并持久化 user + assistant。
func TestAgentRunNoTools(t *testing.T) {
	fl := &fakeLLM{responses: []*llm.ChatResponse{contentResp("你好")}}
	reg, ex := newEngineDeps(t)
	repo := newMemRepo()
	e := NewEngine(fl, reg, ex, repo, trace.NewRecorder(), 5)

	out, err := e.AgentRun(context.Background(), 1, "hi")
	if err != nil {
		t.Fatalf("AgentRun 失败: %v", err)
	}
	if out != "你好" {
		t.Fatalf("输出错误: %q", out)
	}
	msgs := repo.msgs[1]
	if len(msgs) != 2 {
		t.Fatalf("应持久化 user+assistant 两条,实际 %d", len(msgs))
	}
	if msgs[0].Role != llm.RoleUser || msgs[0].Content != "hi" {
		t.Fatalf("用户消息错误: %+v", msgs[0])
	}
	if msgs[1].Role != llm.RoleAssistant || msgs[1].Content != "你好" {
		t.Fatalf("assistant 消息错误: %+v", msgs[1])
	}
}

// TestAgentRunToolCall 工具调用 -> 结果回填 -> 最终回答。
func TestAgentRunToolCall(t *testing.T) {
	fl := &fakeLLM{responses: []*llm.ChatResponse{
		toolCallResp("echo", `{"text":"ping"}`, "call_1"),
		contentResp("收到 ping"),
	}}
	reg, ex := newEngineDeps(t)
	repo := newMemRepo()
	e := NewEngine(fl, reg, ex, repo, trace.NewRecorder(), 5)

	ctx := WithRunID(context.Background(), "run-1")
	out, err := e.AgentRun(ctx, 1, "ping me")
	if err != nil {
		t.Fatalf("AgentRun 失败: %v", err)
	}
	if out != "收到 ping" {
		t.Fatalf("输出错误: %q", out)
	}
	// 第二次 LLM 调用应能看到 tool 角色的回填结果
	if len(fl.gotMsgs) != 2 {
		t.Fatalf("应调用 LLM 2 次,实际 %d", len(fl.gotMsgs))
	}
	var found bool
	for _, m := range fl.gotMsgs[1] {
		if m.Role == llm.RoleTool && m.Content == "ping" && m.ToolCallID == "call_1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("工具结果未回填给 LLM: %+v", fl.gotMsgs[1])
	}
	// 持久化只保留 user + final assistant(工具交换在内存)
	if len(repo.msgs[1]) != 2 {
		t.Fatalf("持久化应只有 user+assistant,实际 %d 条", len(repo.msgs[1]))
	}
	// trace 可查
	if events := e.Trace.Query("run-1"); len(events) == 0 {
		t.Fatal("应记录 trace 事件")
	}
}

// TestAgentRunMaxIterations 达到最大迭代返回提示信息。
func TestAgentRunMaxIterations(t *testing.T) {
	// 始终返回工具调用,必然耗尽迭代
	fl := &fakeLLM{responses: []*llm.ChatResponse{
		toolCallResp("echo", `{"text":"1"}`, "c1"),
		toolCallResp("echo", `{"text":"2"}`, "c2"),
		toolCallResp("echo", `{"text":"3"}`, "c3"),
		toolCallResp("echo", `{"text":"4"}`, "c4"),
	}}
	reg, ex := newEngineDeps(t)
	repo := newMemRepo()
	e := NewEngine(fl, reg, ex, repo, trace.NewRecorder(), 3)

	out, err := e.AgentRun(context.Background(), 1, "go")
	if err != nil {
		t.Fatalf("AgentRun 失败: %v", err)
	}
	if !strings.Contains(out, "已达到最大迭代次数") {
		t.Fatalf("应返回迭代耗尽提示: %q", out)
	}
}

// TestAgentRunStream 流式模式:工具轮无内容,最终回答逐段吐出。
func TestAgentRunStream(t *testing.T) {
	fl := &fakeLLM{responses: []*llm.ChatResponse{
		toolCallResp("echo", `{"text":"ping"}`, "call_1"),
		contentResp("最终回答"),
	}}
	reg, ex := newEngineDeps(t)
	repo := newMemRepo()
	e := NewEngine(fl, reg, ex, repo, trace.NewRecorder(), 5)

	var deltas []string
	ctx := WithRunID(context.Background(), "run-s1")
	out, err := e.AgentRunStream(ctx, 1, "go", func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatalf("AgentRunStream 失败: %v", err)
	}
	if out != "最终回答" {
		t.Fatalf("输出错误: %q", out)
	}
	// 工具轮 content 为空,只应吐出最终回答
	if len(deltas) != 1 || deltas[0] != "最终回答" {
		t.Fatalf("流式增量错误: %v", deltas)
	}
}

// TestAgentRunContext 注入上下文管理器后,发送给 LLM 的消息经过裁剪/压缩。
func TestAgentRunContext(t *testing.T) {
	fl := &fakeLLM{responses: []*llm.ChatResponse{contentResp("ok")}}
	reg, ex := newEngineDeps(t)
	repo := newMemRepo()
	repo.msgs[1] = []llm.Message{
		{Role: llm.RoleUser, Content: "旧1"}, {Role: llm.RoleAssistant, Content: "答1"},
		{Role: llm.RoleUser, Content: "旧2"}, {Role: llm.RoleAssistant, Content: "答2"},
	}
	e := NewEngine(fl, reg, ex, repo, trace.NewRecorder(), 5)
	mgr := &fakeCtxMgr{}
	e.WithContext(mgr)

	if _, err := e.AgentRun(context.Background(), 1, "新问题"); err != nil {
		t.Fatalf("AgentRun 失败: %v", err)
	}
	if !mgr.compacted {
		t.Fatal("超过阈值应触发压缩")
	}
	// LLM 收到的消息:5 条历史+用户 被裁剪/压缩后应少于原始 5 条
	if got := len(fl.gotMsgs[0]); got >= 5 {
		t.Fatalf("上下文压缩后应少于 5 条,实际 %d", got)
	}
}

// fakeCtxMgr 模拟上下文管理器:超过 3 条触发压缩(去掉最后一条)。
type fakeCtxMgr struct {
	compacted bool
}

func (f *fakeCtxMgr) BuildMessages(msgs []llm.Message) []llm.Message { return msgs }
func (f *fakeCtxMgr) OverLimit(msgs []llm.Message) bool              { return len(msgs) > 3 }
func (f *fakeCtxMgr) Compact(_ context.Context, msgs []llm.Message) []llm.Message {
	f.compacted = true
	if len(msgs) > 1 {
		return msgs[:len(msgs)-1]
	}
	return msgs
}

// TestAgentRunToolFailure 工具失败时结果回填错误文本,LLM 继续给出回答。
func TestAgentRunToolFailure(t *testing.T) {
	fl := &fakeLLM{responses: []*llm.ChatResponse{
		toolCallResp("boom", `{}`, "c1"),
		contentResp("工具出错了,我来解释"),
	}}
	reg, ex := newEngineDeps(t)
	reg.Register(&tools.Tool{Name: "boom", Execute: func(context.Context, json.RawMessage) (string, error) {
		return "", errors.New("工具炸了")
	}})
	repo := newMemRepo()
	e := NewEngine(fl, reg, ex, repo, trace.NewRecorder(), 5)

	out, err := e.AgentRun(context.Background(), 1, "run")
	if err != nil {
		t.Fatalf("AgentRun 不应因工具失败而中断: %v", err)
	}
	if out != "工具出错了,我来解释" {
		t.Fatalf("输出错误: %q", out)
	}
	var found bool
	for _, m := range fl.gotMsgs[1] {
		if m.Role == llm.RoleTool && strings.Contains(m.Content, "tool_error") {
			found = true
		}
	}
	if !found {
		t.Fatalf("工具错误应回填给 LLM: %+v", fl.gotMsgs[1])
	}
}
