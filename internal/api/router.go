package api

import (
	"github.com/gin-gonic/gin"
)

// NewRouter 构建 Gin 路由。
func NewRouter(app *App) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())  // 兜底 panic,避免进程崩溃
	r.Use(RequestLogger()) // 结构化请求日志
	r.Use(CORS())          // 跨域(演示页)

	// 演示页(单文件 HTML,无构建依赖)
	r.Static("/demo", "./web")

	// 健康检查(§7),不经过认证与限流
	r.GET("/health", app.HandleHealth)

	// v1 分组:API Key 认证 + Redis 令牌桶限流(Phase 8)
	v1 := r.Group("/v1")
	if app.Cfg.Env.AgentAPIKey != "" {
		v1.Use(AuthMiddleware(app.Cfg))
	}
	v1.Use(RateLimitMiddleware(app.Cfg, app.Redis))

	// 会话与聊天
	v1.POST("/sessions", app.HandleCreateSession)            // 创建会话
	v1.GET("/sessions", app.HandleListSessions)              // 会话列表(历史)
	v1.DELETE("/sessions/:id", app.HandleDeleteSession)      // 删除会话(级联消息)
	v1.GET("/sessions/:id/messages", app.HandleListMessages) // 拉历史消息
	v1.POST("/sessions/:id/chat", app.HandleChat)            // SSE 聊天
	// 知识库(§7)
	v1.POST("/kb/documents", app.HandleUploadDocument)       // 上传文档(异步入库)
	v1.GET("/kb/documents", app.HandleListDocuments)         // 文档列表
	v1.DELETE("/kb/documents/:id", app.HandleDeleteDocument) // 删除文档
	// 定时报告(§7)
	v1.POST("/report-tasks", app.HandleCreateReportTask)       // 创建报告任务
	v1.GET("/report-tasks", app.HandleListReportTasks)         // 任务列表
	v1.DELETE("/report-tasks/:id", app.HandleDeleteReportTask) // 删除报告任务
	v1.GET("/report-tasks/:id/runs", app.HandleListReportRuns) // 执行记录列表
	// 可观测(Phase 8)
	v1.GET("/traces/:run_id", app.HandleTraces) // trace 事件列表

	return r
}
