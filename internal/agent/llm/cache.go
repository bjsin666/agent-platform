package llm

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache LLM 非流式响应缓存后端接口。
// 为什么抽接口:Phase 8 计划把缓存替换为 mini-cache,接口隔离便于替换且便于单测注入内存实现。
type Cache interface {
	// Get 返回缓存值;ok=false 表示未命中。
	Get(ctx context.Context, key string) (value []byte, ok bool, err error)
	// Set 写入缓存并设置 TTL。
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

// redisCache 基于 go-redis 的缓存实现。
type redisCache struct {
	rdb *redis.Client
}

func (c *redisCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	data, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (c *redisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, value, ttl).Err()
}
