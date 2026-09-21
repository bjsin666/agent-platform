package api

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

// HandleCreateSession 创建会话(§7)。
func (a *App) HandleCreateSession(c *gin.Context) {
	var body struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		Fail(c, 400, "请求体错误")
		return
	}
	if body.Title == "" {
		body.Title = "新会话"
	}
	s, err := a.Sessions.Create(c.Request.Context(), body.Title)
	if err != nil {
		Fail(c, 500, "创建会话失败")
		return
	}
	OK(c, gin.H{"session_id": s.ID, "title": s.Title})
}

// HandleListSessions 会话列表(新建优先,最多 20 条)。
func (a *App) HandleListSessions(c *gin.Context) {
	list, err := a.Sessions.List(c.Request.Context(), 20)
	if err != nil {
		Fail(c, 500, "查询会话列表失败")
		return
	}
	OK(c, list)
}

// HandleDeleteSession 删除会话(含全部消息)。
func (a *App) HandleDeleteSession(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Fail(c, 400, "会话 ID 无效")
		return
	}
	if err := a.Sessions.Delete(c.Request.Context(), id); err != nil {
		Fail(c, 500, "删除会话失败")
		return
	}
	OK(c, gin.H{"deleted": id})
}

// HandleListMessages 拉取会话历史消息(§7)。
func (a *App) HandleListMessages(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Fail(c, 400, "会话 ID 无效")
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
	msgs, err := a.Messages.LoadMessages(c.Request.Context(), id)
	if err != nil {
		Fail(c, 500, "查询消息失败")
		return
	}
	OK(c, msgs)
}
