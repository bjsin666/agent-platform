package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// CORS 跨域中间件:允许演示页(不同端口)直接调用 API。
// 仅演示用途;生产环境应收敛为白名单 Origin。
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
