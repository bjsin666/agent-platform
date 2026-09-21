package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// KBSearcher 知识库检索接口。真实实现由 kb 包在 Phase 6 提供。
type KBSearcher interface {
	Search(ctx context.Context, query string, topK int) (string, error)
	GetDocument(ctx context.Context, docID uint64) (string, error)
}

// ReportQuerier 报告执行记录查询接口。真实实现由 report 包在 Phase 7 提供。
type ReportQuerier interface {
	ListRuns(ctx context.Context, limit int) (string, error)
}

// Deps 内置工具的外部依赖。对应模块未接入(Phase 3/4 阶段)时传 nil,
// 工具返回占位提示而非报错,保证引擎流程可先跑通。
type Deps struct {
	KB     KBSearcher
	Report ReportQuerier
}

// RegisterBuiltins 注册四个内置工具(§8.2)。
func RegisterBuiltins(r *Registry, deps Deps) error {
	builtins := []*Tool{
		echoTool(),
		searchKBTool(deps.KB),
		getDocumentTool(deps.KB),
		listReportRunsTool(deps.Report),
	}
	for _, t := range builtins {
		if err := r.Register(t); err != nil {
			return fmt.Errorf("注册内置工具 %s: %w", t.Name, err)
		}
	}
	return nil
}

// echoTool 原样返回输入,测试用。幂等可重试。
func echoTool() *Tool {
	return &Tool{
		Name:         "echo",
		Description:  "原样返回输入文本,用于测试工具链路",
		Parameters:   json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		IsIdempotent: true,
		Execute: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			return a.Text, nil
		},
	}
}

// searchKBTool 知识库检索。kb 为 nil 时返回占位提示(Phase 6 接通)。
func searchKBTool(kb KBSearcher) *Tool {
	return &Tool{
		Name:         "search_kb",
		Description:  "在知识库中检索与查询最相关的文本片段,返回命中内容与引用(文档ID+序号)",
		Parameters:   json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"top_k":{"type":"integer","minimum":1,"default":5}},"required":["query"]}`),
		IsIdempotent: true,
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Query string `json:"query"`
				TopK  int    `json:"top_k"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if a.Query == "" {
				return "", fmt.Errorf("query 不能为空")
			}
			if a.TopK <= 0 {
				a.TopK = 5 // schema 的 default 只对 LLM 提示,不代填,这里兜底
			}
			if kb == nil {
				return `{"warning":"search_kb 尚未接入知识库,Phase 6 接通"}`, nil
			}
			return kb.Search(ctx, a.Query, a.TopK)
		},
	}
}

// getDocumentTool 获取文档全文。kb 为 nil 时返回占位提示(Phase 6 接通)。
func getDocumentTool(kb KBSearcher) *Tool {
	return &Tool{
		Name:         "get_document",
		Description:  "按文档 ID 返回知识库文档全文",
		Parameters:   json.RawMessage(`{"type":"object","properties":{"doc_id":{"type":"integer","minimum":1}},"required":["doc_id"]}`),
		IsIdempotent: true,
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				DocID uint64 `json:"doc_id"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if a.DocID == 0 {
				return "", fmt.Errorf("doc_id 必须大于 0")
			}
			if kb == nil {
				return `{"warning":"get_document 尚未接入知识库,Phase 6 接通"}`, nil
			}
			return kb.GetDocument(ctx, a.DocID)
		},
	}
}

// listReportRunsTool 列出最近报告执行记录。report 为 nil 时返回占位提示(Phase 7 接通)。
func listReportRunsTool(report ReportQuerier) *Tool {
	return &Tool{
		Name:         "list_report_runs",
		Description:  "列出最近的定时报告执行记录(时间、状态、摘要)",
		Parameters:   json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","minimum":1,"default":10}}}`),
		IsIdempotent: true,
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Limit int `json:"limit"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if a.Limit <= 0 {
				a.Limit = 10
			}
			if report == nil {
				return `{"warning":"list_report_runs 尚未接入报告模块,Phase 7 接通"}`, nil
			}
			return report.ListRuns(ctx, a.Limit)
		},
	}
}
