package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 统一响应格式(§7):成功 {"code":0,"data":...};失败 {"code":非0,"msg":"..."}。

// OK 成功响应。
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": data})
}

// Fail 失败响应。code 非 0,HTTP 状态码仍用 200,业务错误码由 code 表达。
func Fail(c *gin.Context, code int, msg string) {
	c.JSON(http.StatusOK, gin.H{"code": code, "msg": msg})
}
