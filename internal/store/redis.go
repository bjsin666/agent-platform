package store

import (
	"context"
	"fmt"
	"time"

	"agent-platform/internal/config"

	"github.com/redis/go-redis/v9"
)

// NewRedis 建立 Redis 客户端并 Ping 验证连通。
// 为什么返回 *redis.Client 而非封装类:上层(限流/缓存)需要细粒度 API,直接给原生 client 最灵活。
func NewRedis(cfg *config.Config) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Env.Redis.Addr,
		Password: cfg.Env.Redis.Password,
		PoolSize: cfg.Store.RedisPoolSize,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("连接 Redis %s: %w", cfg.Env.Redis.Addr, err)
	}
	return client, nil
}
