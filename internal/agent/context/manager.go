package context

import (
	"context"
	"fmt"
	"strings"

	"agent-platform/internal/agent/llm"
)

// Summarizer LLM 摘要接口(智能压缩模式)。*llm.Client 天然实现;为 nil 时退化为机械压缩。
type Summarizer interface {
	Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
}

// Manager 上下文管理器:构建消息窗口与超阈值压缩。
type Manager struct {
	estimator         TokenEstimator
	systemPrompt      string // 全局系统提示(可选)
	maxTokens         int    // 超过该阈值触发压缩
	recentRounds      int    // BuildMessages 保留的最近轮数
	compactKeepRounds int    // Compact 保留的最近轮数
	summarizer        Summarizer
}

// Options 上下文管理器配置。
type Options struct {
	MaxTokens         int
	RecentRounds      int
	CompactKeepRounds int
	SystemPrompt      string
	Summarizer        Summarizer
}

// defaultSystemPrompt 默认系统提示词:约束回答风格,避免满屏 Markdown 的"AI 味"。
// 面试可讲:模型默认会输出 Markdown 标题/加粗/列表,对 C 端聊天观感差,因此用系统提示词规范输出。
const defaultSystemPrompt = `你是知识库问答助手,回答请遵循以下规则:
1. 只用知识库检索到的信息回答;知识库中没有的信息,直接说明"知识库中没有相关内容",绝不编造。
2. 用自然、简洁、口语化的中文回答,像人和人聊天一样,用短段落组织。
3. 不要使用 Markdown 标题(#、##)、加粗(**)或列表符号(-、*)等排版符号,除非确实需要强调;
   需要列举时用"第一、第二"或"1. 2. 3."这类简单编号。
4. 开头不要重复"根据知识库检索的结果"这类套话,直接给出答案。
5. 如果参考了某篇文档,在回答末尾用一句话注明来源,如:(来源:产品说明文档)。`

// NewManager 构造上下文管理器。
func NewManager(estimator TokenEstimator, opts Options) *Manager {
	if opts.SystemPrompt == "" {
		opts.SystemPrompt = defaultSystemPrompt // 未显式配置时使用默认风格约束
	}
	return &Manager{
		estimator:         estimator,
		systemPrompt:      opts.SystemPrompt,
		maxTokens:         opts.MaxTokens,
		recentRounds:      opts.RecentRounds,
		compactKeepRounds: opts.CompactKeepRounds,
		summarizer:        opts.Summarizer,
	}
}

// BuildMessages 构建发送给 LLM 的消息窗口:
// 全局系统提示 + 历史中的 system 消息(如摘要) + 最近 recentRounds 轮对话。
func (m *Manager) BuildMessages(msgs []llm.Message) []llm.Message {
	system, rest := splitSystem(msgs)
	base := []llm.Message{}
	if m.systemPrompt != "" {
		base = append(base, llm.Message{Role: llm.RoleSystem, Content: m.systemPrompt})
	}
	system = append(base, system...)

	keepFrom := 0
	if idx := lastUserIdx(rest, m.recentRounds); idx > 0 {
		keepFrom = idx
	}
	out := append([]llm.Message{}, system...)
	out = append(out, rest[keepFrom:]...)
	return out
}

// OverLimit 判断消息总量是否超过阈值。
func (m *Manager) OverLimit(msgs []llm.Message) bool {
	return m.estimator.CountMessages(msgs) > m.maxTokens
}

// Compact 超阈值时压缩:保留 system + 最近 compactKeepRounds 轮,更早内容压缩为摘要注入。
// 智能模式优先(Summarizer),失败或未配置则退化为机械裁剪工具调用日志。
func (m *Manager) Compact(ctx context.Context, msgs []llm.Message) []llm.Message {
	system, rest := splitSystem(msgs)
	keepFrom := 0
	if idx := lastUserIdx(rest, m.compactKeepRounds); idx > 0 {
		keepFrom = idx
	}
	recent := rest[keepFrom:]
	old := rest[:keepFrom]
	if len(old) == 0 {
		return msgs // 无可压缩内容
	}
	summary := m.summarize(ctx, old)

	out := append([]llm.Message{}, system...)
	out = append(out, llm.Message{Role: llm.RoleSystem, Content: "[compressed-summary]\n" + summary})
	out = append(out, recent...)
	return out
}

// summarize 生成旧消息摘要。
func (m *Manager) summarize(ctx context.Context, old []llm.Message) string {
	if m.summarizer != nil {
		prompt := "请把以下对话压缩为结构化摘要,保留关键结论与未决问题,用中文输出。\n\n" + renderMessages(old)
		resp, err := m.summarizer.Chat(ctx, llm.ChatRequest{
			Messages: []llm.Message{{Role: llm.RoleSystem, Content: prompt}},
		})
		if err == nil && strings.TrimSpace(resp.Message.Content) != "" {
			return resp.Message.Content
		}
	}
	// 机械模式:裁剪工具调用日志(tool 消息),截断超长内容
	return mechanicalSummarize(old)
}

// mechanicalSummarize 机械摘要:丢弃 tool 消息,角色+截断内容拼接。
func mechanicalSummarize(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Role == llm.RoleTool {
			continue // 裁剪工具调用日志
		}
		content := m.Content
		if r := []rune(content); len(r) > 200 {
			content = string(r[:200]) + "..." // 按字符截断,避免切断多字节 UTF-8
		}
		if content == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", m.Role, content)
	}
	return strings.TrimSpace(b.String())
}

// splitSystem 把 system 消息与其余消息分开(保持各自相对顺序)。
func splitSystem(msgs []llm.Message) (system, rest []llm.Message) {
	for _, m := range msgs {
		if m.Role == llm.RoleSystem {
			system = append(system, m)
		} else {
			rest = append(rest, m)
		}
	}
	return system, rest
}

// lastUserIdx 返回应从哪开始保留:保留最近 n 个 user 轮次,返回其起始下标。
// n<=0 表示不保留对话(返回 len);不足 n 轮则全保留(返回 0)。
func lastUserIdx(msgs []llm.Message, n int) int {
	if n <= 0 {
		return len(msgs)
	}
	count := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			count++
			if count == n {
				return i
			}
		}
	}
	return 0
}

// renderMessages 把消息渲染为文本(供 LLM 摘要)。跳过工具调用日志。
func renderMessages(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Role == llm.RoleTool {
			continue
		}
		if m.Content == "" {
			if len(m.ToolCalls) > 0 {
				fmt.Fprintf(&b, "%s: (调用工具 %s)\n", m.Role, m.ToolCalls[0].Function.Name)
			}
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
	}
	return strings.TrimSpace(b.String())
}
