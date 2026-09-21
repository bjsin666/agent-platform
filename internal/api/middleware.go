package api

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestLogger 结构化请求日志中间件。用 log/slog 输出 method/path/status/latency。
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		slog.Info("http_request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}
