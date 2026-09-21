// 临时排查:直接调用 LLM 网关,打印原始返回。用完即删。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"agent-platform/internal/agent/llm"
	"agent-platform/internal/config"
	"agent-platform/internal/store"
)

func main() {
	cfg, _ := config.Load()
	rdb, err := store.NewRedis(cfg)
	if err != nil {
		fmt.Println("redis err(忽略):", err)
	}
	c := llm.NewClient(cfg, rdb)

	tools := []llm.ToolSchema{
		{Type: "function", Function: llm.ToolFunction{Name: "echo", Description: "原样返回输入", Parameters: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 1) 非流式带工具
	fmt.Println("=== Chat(非流式) ===")
	resp, err := c.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "请把 ping 原样回显给我"}},
		Tools:    tools,
	})
	if err != nil {
		fmt.Println("Chat err:", err)
	} else {
		fmt.Printf("content=%q tool_calls=%d finish=%q usage=%+v\n",
			resp.Message.Content, len(resp.Message.ToolCalls), resp.FinishReason, resp.Usage)
		for _, tc := range resp.Message.ToolCalls {
			fmt.Printf("  tool_call: id=%s name=%s args=%s\n", tc.ID, tc.Function.Name, tc.Function.Arguments)
		}
	}

	// 2) 流式带工具
	fmt.Println("=== ChatStreamFull ===")
	var sb []byte
	sresp, err := c.ChatStreamFull(ctx, llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "请把 ping 原样回显给我"}},
		Tools:    tools,
	}, func(d string) { sb = append(sb, d...) })
	if err != nil {
		fmt.Println("ChatStreamFull err:", err)
	} else {
		fmt.Printf("content=%q tool_calls=%d finish=%q streamed_len=%d\n",
			sresp.Message.Content, len(sresp.Message.ToolCalls), sresp.FinishReason, len(sb))
		for _, tc := range sresp.Message.ToolCalls {
			fmt.Printf("  tool_call: id=%s name=%s args=%s\n", tc.ID, tc.Function.Name, tc.Function.Arguments)
		}
	}
}
