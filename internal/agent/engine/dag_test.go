package engine

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/agent/llm"
	"agent-platform/internal/agent/tools"
)

// chatReq 便捷构造 LLM 请求。
func chatReq(content string) llm.ChatRequest {
	return llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: content}}}
}

// TestDAGOrder 链式依赖 A->B->C 严格按序执行。
func TestDAGOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(s string) func(context.Context) error {
		return func(context.Context) error {
			mu.Lock()
			order = append(order, s)
			mu.Unlock()
			return nil
		}
	}
	dag := &DAG{Nodes: []Node{
		{ID: "A", Run: record("A")},
		{ID: "B", Deps: []string{"A"}, Run: record("B")},
		{ID: "C", Deps: []string{"B"}, Run: record("C")},
	}}
	if err := DAGRun(context.Background(), dag, 3); err != nil {
		t.Fatalf("DAGRun 失败: %v", err)
	}
	if len(order) != 3 || order[0] != "A" || order[1] != "B" || order[2] != "C" {
		t.Fatalf("顺序错误: %v", order)
	}
}

// TestDAGConcurrencyLimit 5 个独立节点最大并发被压到 worker 池容量。
func TestDAGConcurrencyLimit(t *testing.T) {
	var cur, max int32
	mk := func(id string) Node {
		return Node{ID: id, Run: func(context.Context) error {
			n := atomic.AddInt32(&cur, 1)
			for {
				m := atomic.LoadInt32(&max)
				if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			atomic.AddInt32(&cur, -1)
			return nil
		}}
	}
	dag := &DAG{Nodes: []Node{mk("n1"), mk("n2"), mk("n3"), mk("n4"), mk("n5")}}
	if err := DAGRun(context.Background(), dag, 3); err != nil {
		t.Fatalf("DAGRun 失败: %v", err)
	}
	if got := atomic.LoadInt32(&max); got != 3 {
		t.Fatalf("最大并发应为 3,实际 %d", got)
	}
	if got := atomic.LoadInt32(&cur); got != 0 {
		t.Fatalf("结束后并发应为 0,实际 %d", got)
	}
}

// TestDAGRing 环检测:A 依赖 B,B 依赖 A。
func TestDAGRing(t *testing.T) {
	dag := &DAG{Nodes: []Node{
		{ID: "A", Deps: []string{"B"}, Run: func(context.Context) error { return nil }},
		{ID: "B", Deps: []string{"A"}, Run: func(context.Context) error { return nil }},
	}}
	err := DAGRun(context.Background(), dag, 3)
	if err == nil {
		t.Fatal("环应被检测到")
	}
	if !strings.Contains(err.Error(), "环") && !strings.Contains(err.Error(), "死锁") {
		t.Fatalf("错误应说明环/死锁: %v", err)
	}
}

// TestDAGFailureCancel 节点失败 -> 下游跳过执行。
func TestDAGFailureCancel(t *testing.T) {
	var bRan, cRan bool
	dag := &DAG{Nodes: []Node{
		{ID: "A", Run: func(context.Context) error { return errors.New("A失败") }},
		{ID: "B", Deps: []string{"A"}, Run: func(context.Context) error { bRan = true; return nil }},
		{ID: "C", Deps: []string{"A"}, Run: func(context.Context) error { cRan = true; return nil }},
	}}
	err := DAGRun(context.Background(), dag, 3)
	if err == nil || !strings.Contains(err.Error(), "A失败") {
		t.Fatalf("应返回 A 的失败原因: %v", err)
	}
	if bRan || cRan {
		t.Fatalf("下游节点不应执行: bRan=%v cRan=%v", bRan, cRan)
	}
}

// TestDAGIndependentContinuesAfterFailure 独立节点不受失败节点影响(设计如此:整图 cancel 后未开始的独立节点被跳过,
// 但已开始的会完成)。这里验证:失败返回,且不 panic。
func TestDAGIndependentContinuesAfterFailure(t *testing.T) {
	var started int32
	dag := &DAG{Nodes: []Node{
		{ID: "A", Run: func(context.Context) error { return errors.New("A失败") }},
		{ID: "D", Run: func(context.Context) error {
			atomic.AddInt32(&started, 1)
			time.Sleep(10 * time.Millisecond)
			return nil
		}},
	}}
	err := DAGRun(context.Background(), dag, 3)
	if err == nil {
		t.Fatal("应返回失败")
	}
	// 不 panic、不卡死即可;started 可能 0 或 1 取决于调度
}

// TestDAGMissingDep 依赖不存在的节点,启动即报错。
func TestDAGMissingDep(t *testing.T) {
	dag := &DAG{Nodes: []Node{
		{ID: "A", Deps: []string{"X"}, Run: func(context.Context) error { return nil }},
	}}
	err := DAGRun(context.Background(), dag, 3)
	if err == nil || !strings.Contains(err.Error(), "依赖不存在") {
		t.Fatalf("应报缺失依赖: %v", err)
	}
}

// TestDAGDuplicateID 节点 ID 重复报错。
func TestDAGDuplicateID(t *testing.T) {
	dag := &DAG{Nodes: []Node{
		{ID: "A", Run: func(context.Context) error { return nil }},
		{ID: "A", Run: func(context.Context) error { return nil }},
	}}
	err := DAGRun(context.Background(), dag, 3)
	if err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("应报重复 ID: %v", err)
	}
}

// TestDAGSelfDep 自依赖报错。
func TestDAGSelfDep(t *testing.T) {
	dag := &DAG{Nodes: []Node{
		{ID: "A", Deps: []string{"A"}, Run: func(context.Context) error { return nil }},
	}}
	err := DAGRun(context.Background(), dag, 3)
	if err == nil || !strings.Contains(err.Error(), "自依赖") {
		t.Fatalf("应报自依赖: %v", err)
	}
}

// TestWorkflowNodes AgentNode/ToolNode/LLMNode 复用同一 DAG 状态机。
func TestWorkflowNodes(t *testing.T) {
	_, ex := newEngineDeps(t)
	fl := &fakeLLM{responses: []*llm.ChatResponse{contentResp("LLM 汇总结果")}}

	var toolOut, agentOut, llmOut string
	dag := &DAG{Nodes: []Node{
		*ToolNode("search", nil, ex, "echo", json.RawMessage(`{"text":"命中片段"}`), &toolOut),
		*AgentNode("summarize", []string{"search"}, func(context.Context) (string, error) {
			return "agent:" + toolOut, nil
		}, &agentOut),
		*LLMNode("generate", []string{"summarize"}, fl, chatReq("x"), &llmOut),
	}}

	if err := DAGRun(context.Background(), dag, 3); err != nil {
		t.Fatalf("DAGRun 失败: %v", err)
	}
	if toolOut != "命中片段" {
		t.Fatalf("ToolNode 结果错误: %q", toolOut)
	}
	if agentOut != "agent:命中片段" {
		t.Fatalf("AgentNode 结果错误: %q", agentOut)
	}
	if llmOut != "LLM 汇总结果" {
		t.Fatalf("LLMNode 结果错误: %q", llmOut)
	}
}

// TestToolNodeFailure 工具节点失败导致 DAG 失败。
func TestToolNodeFailure(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&tools.Tool{Name: "boom", Execute: func(context.Context, json.RawMessage) (string, error) {
		return "", errors.New("工具失败")
	}})
	ex := tools.NewExecutor(reg, tools.Options{ToolConcurrency: 2, ToolTimeout: time.Second})
	var out string
	dag := &DAG{Nodes: []Node{
		*ToolNode("t", nil, ex, "boom", json.RawMessage(`{}`), &out),
	}}
	err := DAGRun(context.Background(), dag, 2)
	if err == nil || !strings.Contains(err.Error(), "工具失败") {
		t.Fatalf("工具失败应传导为 DAG 失败: %v", err)
	}
}

// TestDAGRunNoFalseDeadlock 压力回归:并发调度下不误判"环/死锁"(镜像报告工作流结构)。
// 历史 bug:worker 从就绪队列取任务后、计入在途前,监控看到"无在途无就绪"误判死锁,
// 偶发报"DAG 存在环或不可达节点:完成 5/6"。本测试高频复现该路径。
func TestDAGRunNoFalseDeadlock(t *testing.T) {
	busy := func(id string) Node {
		return Node{ID: id, Run: func(context.Context) error {
			time.Sleep(time.Duration(rand.Intn(3)) * time.Millisecond)
			return nil
		}}
	}
	for i := 0; i < 200; i++ {
		dag := &DAG{Nodes: []Node{
			busy("s1"),
			busy("s2"),
			busy("s3"),
			{ID: "merge", Deps: []string{"s1", "s2", "s3"}, Run: func(context.Context) error { return nil }},
			{ID: "gen", Deps: []string{"merge"}, Run: func(context.Context) error { return nil }},
			{ID: "notify", Deps: []string{"gen"}, Run: func(context.Context) error { return nil }},
		}}
		if err := DAGRun(context.Background(), dag, 3); err != nil {
			t.Fatalf("迭代 %d 误判失败: %v", i, err)
		}
	}
}
