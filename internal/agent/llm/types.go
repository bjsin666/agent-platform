// Package llm 实现 DeepSeek/OpenAI 兼容的 LLM 网关(§8.1)。
package llm

import "encoding/json"

// Role 常量(OpenAI 兼容)。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message 对话消息。
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant 发起工具调用
	ToolCallID string     `json:"tool_call_id,omitempty"` // role=tool 时回填关联的调用 ID
}

// ToolCall LLM 输出的工具调用。
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // function
	Function FunctionCall `json:"function"`
}

// FunctionCall 工具调用具体内容。Arguments 为 JSON 字符串。
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolSchema 工具定义(OpenAI tools 格式,供 LLM 识别)。
type ToolSchema struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction 工具 function 定义。Parameters 为 JSON Schema 原文。
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Usage token 用量。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatRequest 聊天请求。
type ChatRequest struct {
	Messages []Message
	Tools    []ToolSchema
	Stream   bool
}

// ChatResponse 聊天响应。
type ChatResponse struct {
	Message      Message
	FinishReason string
	Usage        Usage
}
