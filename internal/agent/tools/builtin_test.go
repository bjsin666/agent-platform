package tools

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeKB 模拟知识库依赖,拼接结果便于断言参数透传。
type fakeKB struct {
	searchOut string
	docOut    string
}

func (f *fakeKB) Search(_ context.Context, query string, topK int) (string, error) {
	return f.searchOut + "|" + query + "|" + strconv.Itoa(topK), nil
}
func (f *fakeKB) GetDocument(_ context.Context, docID uint64) (string, error) {
	return f.docOut, nil
}

// fakeReport 模拟报告依赖。
type fakeReport struct{ out string }

func (f *fakeReport) ListRuns(_ context.Context, limit int) (string, error) {
	return f.out + "|" + strconv.Itoa(limit), nil
}

// newBuiltinExecutor 注册内置工具并构造执行器。
func newBuiltinExecutor(t *testing.T, deps Deps) *Executor {
	t.Helper()
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, deps); err != nil {
		t.Fatalf("注册内置工具失败: %v", err)
	}
	return NewExecutor(reg, Options{
		ToolConcurrency:          3,
		ToolTimeout:              10 * time.Second,
		ToolRetry:                2,
		ToolCircuitFailThreshold: 3,
		ToolCircuitPause:         30 * time.Second,
	})
}

// TestBuiltinsEcho echo 原样返回。
func TestBuiltinsEcho(t *testing.T) {
	x := newBuiltinExecutor(t, Deps{})
	out, err := x.Execute(context.Background(), "echo", mustArgs(t, map[string]any{"text": "hello"}))
	if err != nil || out != "hello" {
		t.Fatalf("echo 失败: out=%q err=%v", out, err)
	}
}

// TestBuiltinsSearchKBPlaceholder kb 未接入时返回占位而非错误。
func TestBuiltinsSearchKBPlaceholder(t *testing.T) {
	x := newBuiltinExecutor(t, Deps{})
	out, err := x.Execute(context.Background(), "search_kb", mustArgs(t, map[string]any{"query": "测试"}))
	if err != nil {
		t.Fatalf("占位不应报错: %v", err)
	}
	if !strings.Contains(out, "尚未接入") {
		t.Fatalf("应返回占位提示: %q", out)
	}
}

// TestBuiltinsSearchKBReal kb 接入后透传 query 与 top_k。
func TestBuiltinsSearchKBReal(t *testing.T) {
	x := newBuiltinExecutor(t, Deps{KB: &fakeKB{searchOut: "命中"}})
	out, err := x.Execute(context.Background(), "search_kb", mustArgs(t, map[string]any{"query": "问题", "top_k": 5}))
	if err != nil || out != "命中|问题|5" {
		t.Fatalf("透传错误: out=%q err=%v", out, err)
	}
}

// TestBuiltinsSearchKBDefaultTopK 未传 top_k 时使用默认 5。
func TestBuiltinsSearchKBDefaultTopK(t *testing.T) {
	x := newBuiltinExecutor(t, Deps{KB: &fakeKB{searchOut: "x"}})
	out, _ := x.Execute(context.Background(), "search_kb", mustArgs(t, map[string]any{"query": "q"}))
	if out != "x|q|5" {
		t.Fatalf("默认 top_k 应为 5: %q", out)
	}
}

// TestBuiltinsListRuns report 接入后透传 limit。
func TestBuiltinsListRuns(t *testing.T) {
	x := newBuiltinExecutor(t, Deps{Report: &fakeReport{out: "记录"}})
	out, err := x.Execute(context.Background(), "list_report_runs", mustArgs(t, map[string]any{"limit": 3}))
	if err != nil || out != "记录|3" {
		t.Fatalf("list_report_runs 失败: out=%q err=%v", out, err)
	}
}

// TestBuiltinsSchemas 四个内置工具注册成功且 schema 可供 LLM 消费。
func TestBuiltinsSchemas(t *testing.T) {
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, Deps{}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if got := len(reg.List()); got != 4 {
		t.Fatalf("应注册 4 个内置工具,实际 %d", got)
	}
	schemas := reg.Schemas()
	if len(schemas) != 4 {
		t.Fatalf("Schemas 数量错误: %d", len(schemas))
	}
	for _, s := range schemas {
		if s.Function.Name == "" || len(s.Function.Parameters) == 0 {
			t.Fatalf("内置工具应带完整 schema: %+v", s.Function)
		}
	}
}
