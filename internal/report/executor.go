// Package report 定时报告:调度器 + 报告 DAG 执行 + webhook 通知(§8.6)。
package report

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agent-platform/internal/agent/engine"
	"agent-platform/internal/agent/llm"
	"agent-platform/internal/agent/tools"
	"agent-platform/internal/model"

	"gorm.io/gorm"
)

// LLMClient 报告生成所需的最小 LLM 接口(便于测试注入 mock)。
type LLMClient interface {
	Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
}

// Result 报告执行结果。
type Result struct {
	Content string
	Usage   llm.Usage
}

// Executor 报告执行器:按任务配置构建 DAG(search 并行 -> merge -> LLM 生成 -> 通知)。
type Executor struct {
	db             *gorm.DB
	llm            LLMClient
	tools          *tools.Executor
	concurrency    int
	webhookTimeout time.Duration
	webhookRetry   int
}

// NewExecutor 构造报告执行器。
func NewExecutor(db *gorm.DB, llmc LLMClient, toolsEx *tools.Executor, concurrency int, webhookTimeout time.Duration, webhookRetry int) *Executor {
	return &Executor{
		db:             db,
		llm:            llmc,
		tools:          toolsEx,
		concurrency:    concurrency,
		webhookTimeout: webhookTimeout,
		webhookRetry:   webhookRetry,
	}
}

// Execute 执行一次报告:构建并运行 DAG,返回报告内容与用量。
func (x *Executor) Execute(ctx context.Context, task *model.ReportTask) (*Result, error) {
	var kbIDs []uint64
	if len(task.KbIDs) > 0 {
		if err := json.Unmarshal(task.KbIDs, &kbIDs); err != nil {
			return nil, fmt.Errorf("解析 kb_ids: %w", err)
		}
	}

	// 1) 并行 search 节点:对每个 kb_id(文档)按其标题检索
	var searches []*string
	var searchDeps []string
	var nodes []engine.Node
	for i, docID := range kbIDs {
		title := x.loadDocTitle(ctx, docID)
		query := title
		if strings.TrimSpace(query) == "" {
			query = fmt.Sprintf("文档 %d", docID)
		}
		args, _ := json.Marshal(map[string]any{"query": query, "top_k": 5})
		out := new(string)
		searches = append(searches, out)
		nodeID := fmt.Sprintf("search_%d", i)
		nodes = append(nodes, *engine.ToolNode(nodeID, nil, x.tools, "search_kb", args, out))
		searchDeps = append(searchDeps, nodeID)
	}

	// 2) 汇总节点:合并所有检索结果
	merged := new(string)
	nodes = append(nodes, *engine.AgentNode("merge", searchDeps, func(context.Context) (string, error) {
		var b strings.Builder
		for i, s := range searches {
			fmt.Fprintf(&b, "--- 检索源 %d ---\n%s\n", i+1, *s)
		}
		*merged = b.String()
		return *merged, nil
	}, nil))

	// 3) LLM 生成节点:输出结构化 JSON(title/summary/sections[])
	reportOut := new(string)
	usage := &llm.Usage{}
	nodes = append(nodes, *engine.AgentNode("generate", []string{"merge"}, func(ctx context.Context) (string, error) {
		resp, err := x.llm.Chat(ctx, llm.ChatRequest{
			Messages: buildReportMessages(task, *merged),
		})
		if err != nil {
			return "", err
		}
		*usage = resp.Usage
		*reportOut = resp.Message.Content
		return resp.Message.Content, nil
	}, nil))

	// 4) 通知节点:POST webhook(失败重试)
	nodes = append(nodes, *engine.AgentNode("notify", []string{"generate"}, func(ctx context.Context) (string, error) {
		if err := x.notify(ctx, task, *reportOut); err != nil {
			return "", err
		}
		return "notified", nil
	}, nil))

	dag := &engine.DAG{Nodes: nodes}
	if err := engine.DAGRun(ctx, dag, x.concurrency); err != nil {
		return nil, err
	}
	return &Result{Content: *reportOut, Usage: *usage}, nil
}

// loadDocTitle 加载文档标题,作为检索 query。db 为空或查询失败时返回空串(便于单元测试)。
func (x *Executor) loadDocTitle(ctx context.Context, docID uint64) string {
	if x.db == nil {
		return ""
	}
	var doc model.KbDocument
	if err := x.db.WithContext(ctx).First(&doc, docID).Error; err != nil {
		return ""
	}
	return doc.Title
}

// buildReportMessages 构造报告生成提示词,要求输出结构化 JSON。
func buildReportMessages(task *model.ReportTask, merged string) []llm.Message {
	prompt := "你是定时报告生成器。请基于以下检索内容,按任务提示生成一份结构化报告。\n" +
		"必须以 JSON 输出,格式:\n" +
		`{"title":"报告标题","summary":"摘要","sections":[{"heading":"小节标题","content":"小节内容"}]}` + "\n\n" +
		"任务提示:\n" + task.PromptTemplate + "\n\n" +
		"检索内容:\n" + merged
	return []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
	}
}
