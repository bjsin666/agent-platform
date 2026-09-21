package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agent-platform/internal/config"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// runMiddleware 执行中间件,返回是否被放行(未 Abort)。
func runMiddleware(mw gin.HandlerFunc, req *http.Request) bool {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	mw(c)
	return !c.IsAborted()
}

// TestAuthMiddleware 配置了 API Key 时,缺失/错误拒绝,正确放行。
func TestAuthMiddleware(t *testing.T) {
	cfg := &config.Config{}
	cfg.Env.AgentAPIKey = "secret-key"
	mw := AuthMiddleware(cfg)

	if runMiddleware(mw, httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("缺 key 应拒绝")
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer wrong")
	if runMiddleware(mw, r) {
		t.Fatal("错误 key 应拒绝")
	}
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer secret-key")
	if !runMiddleware(mw, r) {
		t.Fatal("正确 key 应放行")
	}
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-API-Key", "secret-key")
	if !runMiddleware(mw, r) {
		t.Fatal("X-API-Key 头应放行")
	}
}

// TestAuthMiddlewareDisabled 未配置 API Key 时全部放行。
func TestAuthMiddlewareDisabled(t *testing.T) {
	mw := AuthMiddleware(&config.Config{})
	if !runMiddleware(mw, httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("未配置 key 应全部放行")
	}
}

// TestRateLimit 令牌桶限流:burst 次放行后拒绝。
func TestRateLimit(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", PoolSize: 4})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("本地 Redis 不可用,跳过限流测试")
	}
	defer rdb.Close()

	cfg := &config.Config{}
	cfg.Governance.RateLimit.Rate = 0.1 // 慢速补充
	cfg.Governance.RateLimit.Burst = 2
	mw := RateLimitMiddleware(cfg, rdb)

	bucket := "ratelimit:test-" + time.Now().Format("150405.000000000")
	do := func() bool {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-API-Key", bucket)
		return runMiddleware(mw, req)
	}

	if !do() {
		t.Fatal("第 1 次应放行")
	}
	if !do() {
		t.Fatal("第 2 次应放行")
	}
	if do() {
		t.Fatal("第 3 次应被限流(429)")
	}
}
