package context

import (
	"context"
	"strings"
	"testing"

	"agent-platform/internal/agent/llm"
)

// TestHeuristicEstimator 启发式估算的量级校验(纯本地,不联网)。
func TestHeuristicEstimator(t *testing.T) {
	h := HeuristicEstimator{}
	if got := h.Count("Hello world"); got <= 0 {
		t.Fatalf("英文计数异常: %d", got)
	}
	if got := h.Count("你好世界"); got < 4 {
		t.Fatalf("4 个中文字应至少估算为 4 token, got %d", got)
	}
	if got := h.Count("        "); got < 1 {
		t.Fatalf("空白文本也应至少 1 token, got %d", got)
	}
	if got := h.CountMessages([]llm.Message{{Role: llm.RoleUser, Content: "hi"}}); got <= 0 {
		t.Fatalf("消息计数异常: %d", got)
	}
}

// TestCalibratedEstimator_Converges 验证系数会向真实比值收敛。
func TestCalibratedEstimator_Converges(t *testing.T) {
	c := NewCalibratedEstimator()
	if coef, n := c.Coefficient(); coef != 1.0 || n != 0 {
		t.Fatalf("初始应为 1.0/0, got %v/%d", coef, n)
	}
	for i := 0; i < 60; i++ {
		est := c.CountMessages([]llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("a", 400)}})
		c.Calibrate(est, int(float64(est)*1.5))
	}
	coef, n := c.Coefficient()
	if n != 60 {
		t.Fatalf("样本数应为 60, got %d", n)
	}
	if coef < 1.4 || coef > 1.6 {
		t.Fatalf("系数应收敛到 1.5 附近, got %v", coef)
	}
}

// TestCalibratedEstimator_IgnoresNoise 验证噪声样本被跳过、极端值被截断。
func TestCalibratedEstimator_IgnoresNoise(t *testing.T) {
	c := NewCalibratedEstimator()
	c.Calibrate(5, 100)
	c.Calibrate(100, 0)
	if coef, n := c.Coefficient(); coef != 1.0 || n != 0 {
		t.Fatalf("噪声样本不应影响系数, got %v/%d", coef, n)
	}
	c.Calibrate(1000, 100000)
	if coef, _ := c.Coefficient(); coef > maxRatio {
		t.Fatalf("系数不应超过上限 %v, got %v", maxRatio, coef)
	}
}

// fakeSummarizer 返回固定摘要的假 LLM。
type fakeSummarizer struct{ out string }

func (f *fakeSummarizer) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: f.out}}, nil
}

// mkManager 构造测试 Manager。
func mkManager(opts Options) *Manager {
	return NewManager(&fakeEstimator{tokensPerChar: 1}, opts)
}

// fakeEstimator 简单估算:按字符数,便于精确控制阈值。
type fakeEstimator struct{ tokensPerChar int }

func (f *fakeEstimator) Count(text string) int { return len(text) * f.tokensPerChar }
func (f *fakeEstimator) CountMessages(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += len(m.Content) * f.tokensPerChar
	}
	return total
}

func user(n string) llm.Message { return llm.Message{Role: llm.RoleUser, Content: "u" + n} }
func asst(n string) llm.Message { return llm.Message{Role: llm.RoleAssistant, Content: "a" + n} }
func tool(n string) llm.Message { return llm.Message{Role: llm.RoleTool, Content: "t" + n} }

// TestBuildMessagesWindow 只保留最近 N 轮,系统提示在最前。
func TestBuildMessagesWindow(t *testing.T) {
	m := mkManager(Options{RecentRounds: 2})
	history := []llm.Message{user("1"), asst("1"), user("2"), asst("2"), user("3"), asst("3")}
	out := m.BuildMessages(history)
	// 默认系统提示词 1 条 + 最近 2 轮(user2..asst3) 4 条 = 5 条
	if len(out) != 5 || out[0].Role != llm.RoleSystem || out[1].Content != "u2" || out[4].Content != "a3" {
		t.Fatalf("窗口裁剪错误: %+v", out)
	}
}

// TestBuildMessagesSystem 系统提示 + 历史 system 消息保留在最前。
func TestBuildMessagesSystem(t *testing.T) {
	m := mkManager(Options{RecentRounds: 1, SystemPrompt: "全局提示"})
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "旧摘要"},
		user("1"), asst("1"),
	}
	out := m.BuildMessages(history)
	if len(out) != 4 {
		t.Fatalf("数量错误: %d", len(out))
	}
	if out[0].Content != "全局提示" || out[1].Content != "旧摘要" {
		t.Fatalf("system 顺序错误: %+v", out)
	}
}

// TestOverLimitAndCompact 超阈值才压缩,未超原样返回。
func TestOverLimitAndCompact(t *testing.T) {
	m := mkManager(Options{MaxTokens: 10, CompactKeepRounds: 1})
	// 6 轮,每轮约 4 token,共约 24 > 10
	history := []llm.Message{user("1"), asst("1"), user("2"), asst("2"), user("3"), asst("3")}
	if !m.OverLimit(history) {
		t.Fatal("应判定超阈值")
	}
	out := m.Compact(context.Background(), history)
	if len(out) < 2 {
		t.Fatalf("压缩结果应含摘要+最近1轮,实际 %d 条", len(out))
	}
	found := false
	for _, msg := range out {
		if msg.Role == llm.RoleSystem && strings.Contains(msg.Content, "[compressed-summary]") {
			found = true
		}
	}
	if !found {
		t.Fatalf("应注入 [compressed-summary] 消息: %+v", out)
	}
}

// TestCompactNotTriggered 未超阈值时原样返回。
func TestCompactNotTriggered(t *testing.T) {
	m := mkManager(Options{MaxTokens: 1000, CompactKeepRounds: 1})
	history := []llm.Message{user("1"), asst("1")}
	out := m.Compact(context.Background(), history)
	if len(out) != len(history) {
		t.Fatalf("未超阈值不应压缩: %+v", out)
	}
}

// TestCompactMechanical 机械模式裁剪工具调用日志(tool 消息)。
func TestCompactMechanical(t *testing.T) {
	m := mkManager(Options{MaxTokens: 1, CompactKeepRounds: 0, Summarizer: nil})
	history := []llm.Message{user("1"), tool("log1"), tool("log2"), asst("1")}
	out := m.Compact(context.Background(), history)
	// CompactKeepRounds=0 -> 保留最近 0 轮;旧内容全压缩。摘要不应含 tool 日志
	if len(out) != 1 {
		t.Fatalf("应只剩一条摘要: %d", len(out))
	}
	if strings.Contains(out[0].Content, "tlog1") || strings.Contains(out[0].Content, "tlog2") {
		t.Fatalf("机械摘要不应含工具日志: %q", out[0].Content)
	}
	if !strings.Contains(out[0].Content, "u1") {
		t.Fatalf("机械摘要应保留用户消息: %q", out[0].Content)
	}
}

// TestCompactSmart 智能模式优先使用 LLM 摘要。
func TestCompactSmart(t *testing.T) {
	sm := &fakeSummarizer{out: "LLM 生成的摘要"}
	m := mkManager(Options{MaxTokens: 1, CompactKeepRounds: 0, Summarizer: sm})
	history := []llm.Message{user("1"), asst("1")}
	out := m.Compact(context.Background(), history)
	if len(out) != 1 || !strings.Contains(out[0].Content, "LLM 生成的摘要") {
		t.Fatalf("智能摘要未生效: %+v", out)
	}
}
