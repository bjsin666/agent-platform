package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"strings"

	"agent-platform/internal/config"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// AuthMiddleware API Key 认证(§8.10/§8 Governance)。
// 配置了 AGENT_API_KEY 时启用:请求需带 Authorization: Bearer <key> 或 X-API-Key。
// 未配置则放行(开发模式)。比较用 SHA-256 哈希 + 常数时间比较,避免明文与时序泄露。
func AuthMiddleware(cfg *config.Config) gin.HandlerFunc {
	key := cfg.Env.AgentAPIKey
	if key == "" {
		return func(c *gin.Context) { c.Next() }
	}
	keyHash := sha256.Sum256([]byte(key))
	return func(c *gin.Context) {
		token := bearerToken(c)
		if token == "" {
			Fail(c, 401, "缺少 API Key")
			c.Abort()
			return
		}
		got := sha256.Sum256([]byte(token))
		if !hmac.Equal(keyHash[:], got[:]) {
			Fail(c, 401, "无效的 API Key")
			c.Abort()
			return
		}
		c.Next()
	}
}

// bearerToken 提取 Authorization: Bearer <key> 或 X-API-Key 头。
func bearerToken(c *gin.Context) string {
	auth := c.GetHeader("Authorization")
	if t := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")); t != "" && t != auth {
		return t
	}
	return strings.TrimSpace(c.GetHeader("X-API-Key"))
}

// tokenBucketScript Redis 令牌桶 Lua 脚本(原子)。
// KEYS[1]=桶 key;ARGV=rate, burst, cost, ttl。返回 1 放行,0 拒绝。
// 使用 Redis 服务端时间,避免客户端时钟偏移。
var tokenBucketScript = redis.NewScript(`
local key = KEYS[1]
local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local cost = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])

local t = redis.call('TIME')
local now = tonumber(t[1]) + tonumber(t[2]) / 1000000

local data = redis.call('HMGET', key, 'tokens', 'last')
local tokens = tonumber(data[1])
local last = tonumber(data[2])
if tokens == nil then
  tokens = burst
  last = now
end

local elapsed = math.max(0, now - last)
tokens = math.min(burst, tokens + elapsed * rate)
last = now

if tokens >= cost then
  tokens = tokens - cost
  redis.call('HMSET', key, 'tokens', tokens, 'last', last)
  redis.call('EXPIRE', key, ttl)
  return 1
end
redis.call('HMSET', key, 'tokens', tokens, 'last', last)
redis.call('EXPIRE', key, ttl)
return 0
`)

// rateLimiter Redis 令牌桶限流器。
type rateLimiter struct {
	rdb   *redis.Client
	rate  float64
	burst int
}

func newRateLimiter(rdb *redis.Client, rate float64, burst int) *rateLimiter {
	return &rateLimiter{rdb: rdb, rate: rate, burst: burst}
}

// allow 消耗一个令牌;成功返回 true。
func (l *rateLimiter) allow(ctx context.Context, bucket string) bool {
	res, err := tokenBucketScript.Run(ctx, l.rdb, []string{bucket}, l.rate, l.burst, 1, 60).Int()
	return err == nil && res == 1
}

// RateLimitMiddleware Redis 令牌桶限流(§8.10)。桶 key 按 API Key(未认证则按 IP)。
func RateLimitMiddleware(cfg *config.Config, rdb *redis.Client) gin.HandlerFunc {
	limiter := newRateLimiter(rdb, cfg.Governance.RateLimit.Rate, cfg.Governance.RateLimit.Burst)
	return func(c *gin.Context) {
		id := bearerToken(c)
		if id == "" {
			id = c.ClientIP()
		}
		if !limiter.allow(c.Request.Context(), "ratelimit:"+id) {
			c.Header("Retry-After", "1")
			Fail(c, 429, "请求过于频繁,请稍后重试")
			c.Abort()
			return
		}
		c.Next()
	}
}
