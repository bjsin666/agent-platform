// Package tools 实现工具注册、参数校验与并发治理(§8.2)。
package tools

import (
	"context"
	"encoding/json"
)

// Tool 一个可被 Agent 调用的工具。
type Tool struct {
	Name         string                                                          // 工具名,LLM 据此调用
	Description  string                                                          // 给 LLM 看的用途说明
	Parameters   json.RawMessage                                                 // 参数 JSON Schema;空表示无参
	IsIdempotent bool                                                            // 幂等工具可安全重试(读操作通常幂等)
	Execute      func(ctx context.Context, args json.RawMessage) (string, error) // 返回字符串结果供回填 LLM
}
