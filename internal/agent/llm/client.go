package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/config"

	"github.com/redis/go-redis/v9"
)

// Client LLM 网关客户端。
type Client struct {
	apiKey          string
	baseURL         string
	model           string
	http            *http.Client
	timeoutTotal    time.Duration
	timeoutPerChunk time.Duration // 首 token 与块间空闲超时(仅流式)
	retryMax        int
	cb              *circuitBreaker
	cache           Cache
	cacheTTL        time.Duration
}

// NewClient 构造 LLM 网关客户端。
func NewClient(cfg *config.Config, rdb *redis.Client) *Client {
	if cfg.Env.DeepSeekAPIKey == "" {
		slog.Warn("DeepSeek API Key 为空,LLM 调用将失败")
	}
	var cache Cache
	if rdb != nil {
		cache = &redisCache{rdb: rdb}
	}
	return &Client{
		apiKey:          cfg.Env.DeepSeekAPIKey,
		baseURL:         strings.TrimRight(cfg.Env.DeepSeekBaseURL, "/"),
		model:           cfg.Env.DeepSeekModel,
		http:            &http.Client{Timeout: cfg.LLM.TimeoutTotal},
		timeoutTotal:    cfg.LLM.TimeoutTotal,
		timeoutPerChunk: cfg.LLM.TimeoutFirstToken,
		retryMax:        cfg.LLM.RetryMax,
		cb:              newCircuitBreaker(cfg.LLM.CircuitFailThreshold, cfg.LLM.CircuitOpenDuration, cfg.LLM.CircuitHalfOpenProbes),
		cache:           cache,
		cacheTTL:        cfg.LLM.CacheTTL,
	}
}

// Chat 非流式聊天:先查 Redis 缓存,未命中则调 API(带重试+熔断)。
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if key, ok := c.cacheKey(req); ok && c.cache != nil {
		if data, found, err := c.cache.Get(ctx, key); err == nil && found {
			var resp ChatResponse
			if err := json.Unmarshal(data, &resp); err == nil {
				slog.Debug("llm 缓存命中", "key", key)
				return &resp, nil
			}
			slog.Warn("llm 缓存反序列化失败,忽略", "err", err)
		}
		resp, err := c.chatAPI(ctx, req)
		if err != nil {
			return nil, err
		}
		if data, err := json.Marshal(resp); err == nil {
			if err := c.cache.Set(ctx, key, data, c.cacheTTL); err != nil {
				slog.Warn("写入 llm 缓存失败", "err", err)
			}
		}
		return resp, nil
	}
	return c.chatAPI(ctx, req)
}

// ChatStream 流式聊天:解析 SSE 增量,把内容片段通过 onDelta 回调逐段吐出。
// 不缓存(§8.1 仅非流式缓存)。工具调用增量在此场景被丢弃;需要 tool_calls 的轮次走 ChatStreamFull。
func (c *Client) ChatStream(ctx context.Context, req ChatRequest, onDelta func(string)) error {
	_, err := c.ChatStreamFull(ctx, req, onDelta)
	return err
}

// ChatStreamFull 流式聊天并返回聚合结果(含 tool_calls 与 usage)。
// 为什么提供:执行引擎的 ReAct 循环既需实时向用户吐内容,又需拿到工具调用决定下一轮。
func (c *Client) ChatStreamFull(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResponse, error) {
	return c.streamAPI(ctx, req, onDelta)
}

// chatAPI 非流式调用:重试 + 熔断。
func (c *Client) chatAPI(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= c.retryMax; attempt++ {
		if !c.cb.allow() {
			return nil, errors.New("LLM 熔断中,拒绝请求")
		}
		if attempt > 0 {
			if !sleep(ctx, backoff(attempt)) {
				return nil, ctx.Err()
			}
		}
		resp, err := c.chatOnce(ctx, req)
		if err == nil {
			c.cb.success()
			return resp, nil
		}
		c.cb.failure()
		lastErr = err
		if !isRetryable(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("LLM 调用失败(已重试 %d 次): %w", c.retryMax, lastErr)
}

// chatOnce 单次非流式 HTTP 调用。
func (c *Client) chatOnce(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	body, err := json.Marshal(buildWireRequest(c.model, req))
	if err != nil {
		return nil, err
	}
	resp, err := c.doRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		return nil, &httpError{status: resp.StatusCode, body: buf.String()}
	}

	var wire wireChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, fmt.Errorf("解析 LLM 响应: %w", err)
	}
	if len(wire.Choices) == 0 {
		return nil, errors.New("LLM 响应无 choices")
	}
	ch := wire.Choices[0]
	return &ChatResponse{
		Message: Message{
			Role:      ch.Message.Role,
			Content:   ch.Message.Content,
			ToolCalls: ch.Message.ToolCalls,
		},
		FinishReason: ch.FinishReason,
		Usage:        wire.Usage,
	}, nil
}

// streamAPI 流式调用:SSE 逐行解析,前台按块刷新空闲超时,聚合内容/工具调用/用量。
func (c *Client) streamAPI(ctx context.Context, req ChatRequest, onDelta func(string)) (*ChatResponse, error) {
	if !c.cb.allow() {
		return nil, errors.New("LLM 熔断中,拒绝请求")
	}

	// 整体超时 + 可取消子上下文(用于及时释放后台读协程)
	ctx, cancel := context.WithTimeout(ctx, c.timeoutTotal)
	reqCtx, reqCancel := context.WithCancel(ctx)
	defer cancel()
	defer reqCancel()

	wire := buildWireRequest(c.model, req)
	wire.Stream = true // ChatStream/ChatStreamFull 必须流式,与调用方是否设 Stream 无关
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	resp, err := c.doRequest(reqCtx, body)
	if err != nil {
		c.cb.failure()
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		c.cb.failure()
		return nil, &httpError{status: resp.StatusCode, body: buf.String()}
	}

	// 后台读行;发送均带 ctx 感知,避免函数退出后协程泄漏
	lineCh := make(chan string, 8)
	errCh := make(chan error, 1)
	go readStreamLines(reqCtx, resp.Body, lineCh, errCh)

	acc := &streamAccumulator{}
	idle := time.NewTimer(c.timeoutPerChunk)
	defer idle.Stop()

	for {
		select {
		case <-ctx.Done():
			c.cb.failure()
			return nil, fmt.Errorf("LLM 流式响应整体超时: %w", ctx.Err())
		case <-idle.C:
			c.cb.failure()
			return nil, fmt.Errorf("LLM 流式响应空闲超时(>%s 无数据)", c.timeoutPerChunk)
		case err := <-errCh:
			c.cb.failure()
			return nil, fmt.Errorf("读取 LLM 流: %w", err)
		case line, ok := <-lineCh:
			if !ok {
				// 正常结束(读到 EOF):关闭 channel 保证已入队行全部先被消费
				c.cb.success()
				return acc.response(), nil
			}
			idle.Reset(c.timeoutPerChunk)
			done, err := c.handleSSELine(line, acc, onDelta)
			if err != nil {
				c.cb.failure()
				return nil, err
			}
			if done {
				c.cb.success()
				return acc.response(), nil
			}
		}
	}
}

// readStreamLines 读取响应体并按行分发。
// 为什么用 close(lineCh) 而非 errCh 传 EOF:读协程可能把多行快速入队后再读到大块 EOF,
// 若以 errCh(EOF) 作结束信号,前台 select 可能先选到它而丢弃已入队行。关闭 channel 可保证
// 已入队的行全部被消费后才返回。
func readStreamLines(ctx context.Context, body io.Reader, lineCh chan<- string, errCh chan<- error) {
	reader := bufio.NewReader(body)
	defer close(lineCh)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			select {
			case lineCh <- line:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				select {
				case errCh <- err:
				case <-ctx.Done():
				}
			}
			return // EOF 视为正常结束,close(lineCh) 由 defer 完成
		}
	}
}

// streamAccumulator 流式块聚合器:内容、工具调用(按 index 合并)、用量、结束原因。
type streamAccumulator struct {
	content      strings.Builder
	toolCalls    []ToolCall
	usage        Usage
	finishReason string
}

// response 生成聚合后的完整响应。
func (a *streamAccumulator) response() *ChatResponse {
	return &ChatResponse{
		Message: Message{
			Role:      RoleAssistant,
			Content:   a.content.String(),
			ToolCalls: a.toolCalls,
		},
		FinishReason: a.finishReason,
		Usage:        a.usage,
	}
}

// mergeStreamToolCall 按 index 合并流式工具调用片段。
// 片段通常形如:先(index,name/arguments 首段),再(index,arguments 续段)。
func mergeStreamToolCall(calls []ToolCall, frag wireStreamToolCall) []ToolCall {
	for len(calls) <= frag.Index {
		calls = append(calls, ToolCall{})
	}
	c := &calls[frag.Index]
	if frag.ID != "" {
		c.ID = frag.ID
	}
	if frag.Type != "" {
		c.Type = frag.Type
	}
	if frag.Function.Name != "" {
		c.Function.Name = frag.Function.Name
	}
	if frag.Function.Arguments != "" {
		c.Function.Arguments += frag.Function.Arguments
	}
	return calls
}

// handleSSELine 处理一行 SSE;返回 done=true 表示收到 [DONE]。
func (c *Client) handleSSELine(line string, acc *streamAccumulator, onDelta func(string)) (bool, error) {
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, "data:") {
		return false, nil // 空行/注释行
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "[DONE]" {
		return true, nil
	}
	var chunk wireStreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return false, fmt.Errorf("解析 LLM 流块: %w", err)
	}
	if chunk.Usage.TotalTokens > 0 {
		acc.usage = chunk.Usage // 部分实现只在最后一块带 usage
	}
	if len(chunk.Choices) == 0 {
		return false, nil // 心跳/空块
	}
	delta := chunk.Choices[0].Delta
	if delta.Content != "" {
		acc.content.WriteString(delta.Content)
		if onDelta != nil {
			onDelta(delta.Content)
		}
	}
	for _, tc := range delta.ToolCalls {
		acc.toolCalls = mergeStreamToolCall(acc.toolCalls, tc)
	}
	if chunk.Choices[0].FinishReason != "" {
		acc.finishReason = chunk.Choices[0].FinishReason
	}
	return false, nil
}

// doRequest 构造并发送 OpenAI 兼容请求。
func (c *Client) doRequest(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	return c.http.Do(req)
}

// buildWireRequest 把公共请求转换为线格式。
func buildWireRequest(model string, req ChatRequest) wireChatRequest {
	w := wireChatRequest{Model: model, Messages: make([]wireMessage, len(req.Messages)), Stream: req.Stream}
	for i, m := range req.Messages {
		w.Messages[i] = wireMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
		}
	}
	if len(req.Tools) > 0 {
		w.Tools = req.Tools
	}
	return w
}

// cacheKey 计算缓存键:model + messages + tools 的 sha256。仅非流式请求参与缓存。
func (c *Client) cacheKey(req ChatRequest) (string, bool) {
	if req.Stream {
		return "", false
	}
	h := sha256.New()
	io.WriteString(h, c.model)
	if buf, err := json.Marshal(req.Messages); err == nil {
		h.Write(buf)
	}
	if buf, err := json.Marshal(req.Tools); err == nil {
		h.Write(buf)
	}
	return "llm:cache:" + hex.EncodeToString(h.Sum(nil)), true
}

// httpError 非 2xx 响应错误。status>=500 可重试。
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("LLM API 返回 %d: %s", e.status, e.body)
}

// isRetryable 判断是否值得重试:5xx 可重试,4xx 不可重试,网络/超时错误可重试。
func isRetryable(err error) bool {
	var he *httpError
	if errors.As(err, &he) {
		return he.status >= 500
	}
	return true
}

// backoff 指数退避 + 随机 jitter。
func backoff(attempt int) time.Duration {
	base := 200 * time.Millisecond
	nominal := base << (attempt - 1)
	return nominal + time.Duration(rand.Int63n(int64(nominal)))
}

// sleep 可被 ctx 中断的睡眠。
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
