package engine

import (
	"context"
	"encoding/json"

	"agent-platform/internal/agent/llm"
	"agent-platform/internal/agent/tools"
)

// 本文件提供 AgentNode / ToolNode / LLMNode 三种节点的构造器,
// 让 DAG 工作流(如定时报告)复用同一套执行状态机。

// AgentNode 把一段子 Agent 逻辑包装成 DAG 节点。run 返回文本,result 非空时写入。
func AgentNode(id string, deps []string, run func(ctx context.Context) (string, error), result *string) *Node {
	return &Node{
		ID:   id,
		Deps: deps,
		Run: func(ctx context.Context) error {
			out, err := run(ctx)
			if err != nil {
				return err
			}
			if result != nil {
				*result = out
			}
			return nil
		},
	}
}

// ToolNode 把一次工具调用包装成 DAG 节点。result 非空时写入工具输出。
func ToolNode(id string, deps []string, ex *tools.Executor, name string, args json.RawMessage, result *string) *Node {
	return &Node{
		ID:   id,
		Deps: deps,
		Run: func(ctx context.Context) error {
			out, err := ex.Execute(ctx, name, args)
			if err != nil {
				return err
			}
			if result != nil {
				*result = out
			}
			return nil
		},
	}
}

// LLMNode 把一次 LLM 调用包装成 DAG 节点。result 非空时写入响应文本。
func LLMNode(id string, deps []string, client LLM, req llm.ChatRequest, result *string) *Node {
	return &Node{
		ID:   id,
		Deps: deps,
		Run: func(ctx context.Context) error {
			resp, err := client.Chat(ctx, req)
			if err != nil {
				return err
			}
			if result != nil {
				*result = resp.Message.Content
			}
			return nil
		},
	}
}
