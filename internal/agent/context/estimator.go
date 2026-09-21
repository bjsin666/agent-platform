// Package context 上下文管理:token 估算、消息窗口构建、超阈值压缩(§8.4)。
package context

import (
	"agent-platform/internal/agent/llm"

	tiktoken "github.com/pkoukk/tiktoken-go"
)

// TokenEstimator token 估算接口,便于替换估算算法。
type TokenEstimator interface {
	// Count 估算单段文本的 token 数。
	Count(text string) int
	// CountMessages 估算一组消息的 token 数(含角色开销近似)。
	CountMessages(msgs []llm.Message) int
}

// TiktokenEstimator 基于 cl100k_base 的估算器(§8.4 指定)。
type TiktokenEstimator struct {
	enc *tiktoken.Tiktoken
}

// NewTiktokenEstimator 加载 cl100k_base 编码。
func NewTiktokenEstimator() (*TiktokenEstimator, error) {
	enc, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		return nil, err
	}
	return &TiktokenEstimator{enc: enc}, nil
}

// Count 估算单段文本 token 数。
func (t *TiktokenEstimator) Count(text string) int {
	return len(t.enc.Encode(text, nil, nil))
}

// CountMessages 估算消息列表 token 数:每条消息基础开销 + 内容 + 工具调用参数。
func (t *TiktokenEstimator) CountMessages(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += 3 // 每条消息的角色/分隔等基础开销(OpenAI 官方近似)
		total += t.Count(m.Content)
		for _, tc := range m.ToolCalls {
			total += t.Count(tc.Function.Name) + t.Count(tc.Function.Arguments)
		}
	}
	return total
}
