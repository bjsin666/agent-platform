package api

import (
	"agent-platform/internal/model"

	"github.com/gin-gonic/gin"
)

// HandleTraces 按 run_id 返回 trace 事件列表(§7/§8.9)。读取已异步落库的 tool_events。
func (a *App) HandleTraces(c *gin.Context) {
	runID := c.Param("run_id")
	var events []model.ToolEvent
	if err := a.DB.WithContext(c.Request.Context()).
		Where("run_id = ?", runID).Order("id ASC").Find(&events).Error; err != nil {
		Fail(c, 500, "查询 trace 失败")
		return
	}
	OK(c, events)
}
