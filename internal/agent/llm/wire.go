package llm

// 本文件定义 OpenAI 兼容 HTTP 协议的请求/响应 JSON 结构,与公共类型隔离。

// wireChatRequest 发给 LLM 的请求体。
type wireChatRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Tools    []ToolSchema  `json:"tools,omitempty"`
	Stream   bool          `json:"stream"`
}

// wireMessage 线格式消息。assistant 只带 tool_calls 时 content 可为空串。
type wireMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// wireChatResponse 非流式响应体。
type wireChatResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Index        int         `json:"index"`
		Message      wireMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

// wireStreamToolCall 流式工具调用增量片段,携带 index 用于按位置合并。
type wireStreamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// wireDelta 流式块中的增量内容。
type wireDelta struct {
	Content   string               `json:"content"`
	ToolCalls []wireStreamToolCall `json:"tool_calls"`
}

// wireStreamChunk 流式响应中的单块(SSE data 行)。
type wireStreamChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Index        int       `json:"index"`
		Delta        wireDelta `json:"delta"`
		FinishReason string    `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}
