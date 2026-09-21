package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/agent/engine"

	"github.com/gin-gonic/gin"
)

// HandleChat SSE 流式聊天(§7)。流式返回 delta 事件,结束发 done + [DONE]。
func (a *App) HandleChat(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Fail(c, 400, "会话 ID 无效")
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		Fail(c, 400, "请求体错误")
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		Fail(c, 400, "message 不能为空")
		return
	}

	s, err := a.Sessions.Get(c.Request.Context(), id)
	if err != nil {
		Fail(c, 500, "查询会话失败")
		return
	}
	if s == nil {
		Fail(c, 404, "会话不存在")
		return
	}

	// 切换为 SSE 响应
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		Fail(c, 500, "当前连接不支持 SSE")
		return
	}

	runID := fmt.Sprintf("chat_%d_%d", id, time.Now().UnixNano())
	ctx := engine.WithRunID(c.Request.Context(), runID)

	final, err := a.Engine.AgentRunStream(ctx, id, body.Message, func(delta string) {
		writeSSEEvent(c, flusher, gin.H{"type": "delta", "content": delta})
	})
	if err != nil {
		writeSSEEvent(c, flusher, gin.H{"type": "error", "message": err.Error()})
		writeSSEDone(c, flusher)
		return
	}
	writeSSEEvent(c, flusher, gin.H{"type": "done", "run_id": runID, "content": final})
	writeSSEDone(c, flusher)
}

// writeSSEEvent 写一条 SSE 事件:data: <json>\n\n。
func writeSSEEvent(c *gin.Context, f http.Flusher, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		data = []byte(`{"type":"error","message":"序列化失败"}`)
	}
	c.Writer.WriteString("data: " + string(data) + "\n\n")
	f.Flush()
}

// writeSSEDone 写 SSE 结束标记 [DONE](§8.10)。
func writeSSEDone(c *gin.Context, f http.Flusher) {
	c.Writer.WriteString("data: [DONE]\n\n")
	f.Flush()
}
