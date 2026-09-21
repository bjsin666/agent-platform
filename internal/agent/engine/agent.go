package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent-platform/internal/agent/llm"
	"agent-platform/internal/trace"
)

// maxIterFallback 未配置时的迭代上限。
const maxIterFallback = 10

// AgentRun 执行 ReAct 循环(非流式):加载历史 -> 追加用户消息 -> 循环调用 LLM。
// 无 tool_calls 返回最终回答;达到最大迭代返回提示并附现有文本。
// 为什么持久化用户消息与最终回答、工具交换仅存内存:消息表无 tool_call_id 字段
// (§6),工具轮次无法从历史完整重建,故每轮内保持内存态,跨轮只保留 user/assistant。
func (e *Engine) AgentRun(ctx context.Context, sessionID uint64, userMsg string) (string, error) {
	messages, err := e.prepareMessages(ctx, sessionID, userMsg)
	if err != nil {
		return "", err
	}
	runID, traceID := e.newRunIDs(ctx)

	for iter := 1; iter <= e.maxIter(); iter++ {
		llmStart := time.Now()
		resp, err := e.LLM.Chat(ctx, llm.ChatRequest{Messages: messages, Tools: e.Registry.Schemas()})
		e.recordEvent(runID, traceID, trace.Event{
			EventType: "llm", Name: "chat", Status: statusOf(err),
			LatencyMS: time.Since(llmStart).Milliseconds(),
			Input:     fmt.Sprintf("iteration=%d", iter),
			Output:    llmRespPreview(resp),
		})
		if err != nil {
			return "", fmt.Errorf("第 %d 轮 LLM 调用失败: %w", iter, err)
		}
		e.recordUsage(ctx, messages, resp, time.Since(llmStart))

		if len(resp.Message.ToolCalls) == 0 {
			return e.persistAnswer(ctx, sessionID, resp.Message.Content)
		}
		messages = append(messages, resp.Message)
		messages = e.runToolCalls(ctx, runID, traceID, messages, resp.Message.ToolCalls)
	}

	return e.persistAnswer(ctx, sessionID, maxIterMessage(e.maxIter(), messages))
}

// AgentRunStream 与 AgentRun 相同,但 LLM 调用使用流式:内容增量实时通过 onDelta 吐出,
// 同时聚合 tool_calls 判断下一轮。适用 SSE 聊天接口。
// 注意:若模型在工具轮前输出文本,该文本会先被吐出(DeepSeek 工具轮通常 content 为空)。
func (e *Engine) AgentRunStream(ctx context.Context, sessionID uint64, userMsg string, onDelta func(string)) (string, error) {
	messages, err := e.prepareMessages(ctx, sessionID, userMsg)
	if err != nil {
		return "", err
	}
	runID, traceID := e.newRunIDs(ctx)

	for iter := 1; iter <= e.maxIter(); iter++ {
		llmStart := time.Now()
		resp, err := e.LLM.ChatStreamFull(ctx, llm.ChatRequest{Messages: messages, Tools: e.Registry.Schemas()}, onDelta)
		e.recordEvent(runID, traceID, trace.Event{
			EventType: "llm", Name: "chat_stream", Status: statusOf(err),
			LatencyMS: time.Since(llmStart).Milliseconds(),
			Input:     fmt.Sprintf("iteration=%d", iter),
			Output:    llmRespPreview(resp),
		})
		if err != nil {
			return "", fmt.Errorf("第 %d 轮 LLM 流式调用失败: %w", iter, err)
		}
		e.recordUsage(ctx, messages, resp, time.Since(llmStart))

		if len(resp.Message.ToolCalls) == 0 {
			return e.persistAnswer(ctx, sessionID, resp.Message.Content)
		}
		messages = append(messages, resp.Message)
		messages = e.runToolCalls(ctx, runID, traceID, messages, resp.Message.ToolCalls)
	}

	return e.persistAnswer(ctx, sessionID, maxIterMessage(e.maxIter(), messages))
}

// prepareMessages 加载历史 -> 追加并持久化用户消息 -> 应用上下文窗口与压缩(可选)。
func (e *Engine) prepareMessages(ctx context.Context, sessionID uint64, userMsg string) ([]llm.Message, error) {
	messages, err := e.Messages.LoadMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("加载会话历史: %w", err)
	}
	if strings.TrimSpace(userMsg) == "" {
		return nil, errors.New("用户消息不能为空")
	}
	userMessage := llm.Message{Role: llm.RoleUser, Content: userMsg}
	if err := e.Messages.AppendMessage(ctx, sessionID, userMessage); err != nil {
		return nil, fmt.Errorf("持久化用户消息: %w", err)
	}
	messages = append(messages, userMessage)

	if e.Context != nil {
		messages = e.Context.BuildMessages(messages)
		if e.Context.OverLimit(messages) {
			messages = e.Context.Compact(ctx, messages)
		}
	}
	return messages, nil
}

// runToolCalls 逐个执行工具调用,把结果回填为 tool 角色消息。工具失败也回填错误文本,让 LLM 自行处理。
func (e *Engine) runToolCalls(ctx context.Context, runID, traceID string, messages []llm.Message, calls []llm.ToolCall) []llm.Message {
	for _, tc := range calls {
		start := time.Now()
		out, err := e.Executor.Execute(ctx, tc.Function.Name, json.RawMessage(tc.Function.Arguments))
		status := "success"
		output := out
		if err != nil {
			status = "error"
			output = fmt.Sprintf(`{"tool_error":%q}`, err.Error())
		}
		e.recordEvent(runID, traceID, trace.Event{
			EventType: "tool", Name: tc.Function.Name, Status: status,
			LatencyMS: time.Since(start).Milliseconds(),
			Input:     tc.Function.Arguments,
			Output:    output,
		})
		messages = append(messages, llm.Message{Role: llm.RoleTool, Content: output, ToolCallID: tc.ID})
	}
	return messages
}

// persistAnswer 持久化最终回答并返回。
func (e *Engine) persistAnswer(ctx context.Context, sessionID uint64, content string) (string, error) {
	final := llm.Message{Role: llm.RoleAssistant, Content: content}
	if err := e.Messages.AppendMessage(ctx, sessionID, final); err != nil {
		return "", fmt.Errorf("持久化回答: %w", err)
	}
	return content, nil
}

// newRunIDs 生成 run/trace ID(优先用 ctx 注入的 run ID,便于按 run_id 查 trace)。
func (e *Engine) newRunIDs(ctx context.Context) (runID, traceID string) {
	runID = runIDFrom(ctx)
	if runID == "" {
		runID = genID()
	}
	return runID, genID()
}

func (e *Engine) maxIter() int {
	if e.MaxIter <= 0 {
		return maxIterFallback
	}
	return e.MaxIter
}

// recordUsage 上报 LLM 用量(Phase 8)。租户统一用 "default"(单租户)。
func (e *Engine) recordUsage(ctx context.Context, sent []llm.Message, resp *llm.ChatResponse, latency time.Duration) {
	if e.Usage != nil && resp != nil {
		e.Usage.Record(ctx, "default", resp.Usage, latency)
	}
	// 用服务端返回的真实 prompt_tokens 校准本地 token 估算系数。
	// 属于"顺手采集":不额外发起请求,估算偏差会被真实数据逐步修正。
	if e.Context != nil && resp != nil && resp.Usage.PromptTokens > 0 {
		e.Context.ObservePromptUsage(sent, resp.Usage.PromptTokens)
	}
}

// recordEvent 记 trace,统一补充 RunID/TraceID/SpanID。
func (e *Engine) recordEvent(runID, traceID string, ev trace.Event) {
	if e.Trace == nil {
		return
	}
	ev.RunID = runID
	ev.TraceID = traceID
	ev.SpanID = genID()
	e.Trace.Record(ev)
}

// maxIterMessage 迭代耗尽时的兜底回复。
func maxIterMessage(maxIter int, messages []llm.Message) string {
	return fmt.Sprintf("已达到最大迭代次数(%d 轮),基于现有信息给出结论:\n%s", maxIter, lastAssistantText(messages))
}

// llmRespPreview 截取 LLM 响应预览,避免超大日志。
func llmRespPreview(resp *llm.ChatResponse) string {
	if resp == nil {
		return ""
	}
	if len(resp.Message.ToolCalls) > 0 {
		return fmt.Sprintf("tool_calls=%d", len(resp.Message.ToolCalls))
	}
	s := resp.Message.Content
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "..." // 按字符截断,避免切断多字节 UTF-8
	}
	return s
}

// lastAssistantText 取现有消息中最后一条 assistant 文本,用于迭代耗尽时的兜底。
func lastAssistantText(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llm.RoleAssistant && messages[i].Content != "" {
			return messages[i].Content
		}
	}
	return "(无)"
}

// statusOf 错误转 trace 状态。
func statusOf(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}
