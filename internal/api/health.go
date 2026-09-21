package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// HealthResp 健康检查响应(§7)。
type HealthResp struct {
	MySQL bool `json:"mysql"`
	Redis bool `json:"redis"`
	Embed bool `json:"embed"`
}

// HandleHealth 返回三项连通状态。每项独立探测,单项失败不影响其他项上报。
func (a *App) HandleHealth(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	resp := HealthResp{}

	// MySQL:Ping 底层连接池
	if sqlDB, err := a.DB.DB(); err == nil {
		resp.MySQL = sqlDB.PingContext(ctx) == nil
	}

	// Redis:Ping
	resp.Redis = a.Redis.Ping(ctx).Err() == nil

	// Embed:请求 Python 服务的 /health
	resp.Embed = a.pingEmbed(ctx)

	OK(c, resp)
}

// pingEmbed 探测 embedding 服务健康状态。服务未启动时返回 false 而非报错。
func (a *App) pingEmbed(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Cfg.Env.EmbedServiceURL+"/health", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
