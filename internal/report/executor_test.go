package report

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/agent/llm"
	"agent-platform/internal/agent/tools"
	"agent-platform/internal/model"
)

// fakeLLM 报告生成用的假 LLM,返回预设 JSON。
type fakeLLM struct{ out string }

func (f *fakeLLM) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{
		Message: llm.Message{Role: llm.RoleAssistant, Content: f.out},
		Usage:   llm.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}, nil
}

// fakeKB 假知识库。
type fakeKB struct{}

func (fakeKB) Search(_ context.Context, query string, topK int) (string, error) {
	return "检索结果:" + query, nil
}
func (fakeKB) GetDocument(_ context.Context, docID uint64) (string, error) {
	return "文档全文", nil
}

// newReportExecutor 构造报告执行器(真实工具执行器 + 假 LLM/KB)。
func newReportExecutor(t *testing.T, llmOut string, webhookURL string) (*Executor, *tools.Executor) {
	t.Helper()
	reg := tools.NewRegistry()
	if err := tools.RegisterBuiltins(reg, tools.Deps{KB: fakeKB{}}); err != nil {
		t.Fatalf("注册内置工具: %v", err)
	}
	toolsEx := tools.NewExecutor(reg, tools.Options{
		ToolConcurrency:          3,
		ToolTimeout:              5 * time.Second,
		ToolRetry:                2,
		ToolCircuitFailThreshold: 3,
		ToolCircuitPause:         30 * time.Second,
	})
	exec := NewExecutor(nil, &fakeLLM{out: llmOut}, toolsEx, 3, 3*time.Second, 2)
	return exec, toolsEx
}

func mkTask(webhookURL string) *model.ReportTask {
	notify, _ := json.Marshal(map[string]string{"type": "webhook", "url": webhookURL})
	return &model.ReportTask{
		Name:           "测试报告",
		CronExpr:       "*/1 * * * *",
		KbIDs:          json.RawMessage(`[1,2]`),
		PromptTemplate: "生成一份摘要报告",
		NotifyChannel:  "webhook",
		NotifyConfig:   notify,
		Status:         "active",
	}
}

// TestExecutorRun 报告 DAG 全流程:两个并行 search -> merge -> LLM 生成 -> webhook 通知。
func TestExecutorRun(t *testing.T) {
	var received atomic.Int32
	var mu sync.Mutex
	var bodies []string
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		mu.Lock()
		bodies = append(bodies, string(buf))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer webhook.Close()

	jsonOut := `{"title":"测试报告","summary":"摘要","sections":[{"heading":"a","content":"b"}]}`
	exec, _ := newReportExecutor(t, jsonOut, webhook.URL)

	ctx := context.Background()
	res, err := exec.Execute(ctx, mkTask(webhook.URL))
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if res.Content != jsonOut {
		t.Fatalf("报告内容错误: %q", res.Content)
	}
	if res.Usage.TotalTokens != 30 {
		t.Fatalf("用量错误: %+v", res.Usage)
	}
	if received.Load() != 1 {
		t.Fatalf("webhook 应收到 1 次,实际 %d", received.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	// 报告内容在 webhook body 中是 JSON 转义后的字符串,匹配关键子串即可
	if len(bodies) != 1 || !strings.Contains(bodies[0], "测试报告") ||
		!strings.Contains(bodies[0], "summary") || !strings.Contains(bodies[0], "sections") {
		t.Fatalf("webhook 内容错误: %s", bodies[0])
	}
}

// TestExecutorWebhookRetry webhook 前两次 500,第三次成功,验证重试。
func TestExecutorWebhookRetry(t *testing.T) {
	var attempts int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) <= 2 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(200)
	}))
	defer webhook.Close()

	exec, _ := newReportExecutor(t, `{"title":"x"}`, webhook.URL)
	if _, err := exec.Execute(context.Background(), mkTask(webhook.URL)); err != nil {
		t.Fatalf("Execute 应重试后成功: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("应重试 2 次(共 3 次),实际 %d", got)
	}
}

// TestExecutorNoWebhook 未配置 webhook URL 时通知失败 -> 报告执行失败。
func TestExecutorNoWebhook(t *testing.T) {
	exec, _ := newReportExecutor(t, `{"title":"x"}`, "")
	task := mkTask("")
	task.NotifyConfig = json.RawMessage(`{"url":""}`)
	_, err := exec.Execute(context.Background(), task)
	if err == nil || !strings.Contains(err.Error(), "未配置 webhook") {
		t.Fatalf("应报未配置 webhook: %v", err)
	}
}
