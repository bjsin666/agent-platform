// Package cachetool 把 mini-cache 包装成 cache_get/cache_set 工具(§8 Governance/MCP 联动)。
//
// 设计说明:mini-cache 是 getter 型读穿缓存(无 Set 接口),故用 Redis 作为"数据源",
// getter 在未命中时回源 Redis;cache_set 写 Redis 并失效 mini-cache,下次 Get 重新缓存。
package cachetool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agent-platform/internal/agent/tools"

	minicache "github.com/bjsin666/mini-cache"

	"github.com/redis/go-redis/v9"
)

// Service mini-cache + Redis 数据源包装。
type Service struct {
	cache  *minicache.Cache
	rdb    *redis.Client
	prefix string // Redis 键前缀
}

// New 构造服务。prefix 隔离命名空间。
func New(rdb *redis.Client, prefix string) *Service {
	if prefix == "" {
		prefix = "minicache:"
	}
	c := minicache.NewWithTTL("agent", 16<<20, 5*time.Minute, minicache.GetterFunc(
		func(ctx context.Context, key string) ([]byte, error) {
			val, err := rdb.Get(ctx, prefix+key).Bytes()
			if errors.Is(err, redis.Nil) {
				return nil, minicache.ErrKeyNotFound
			}
			if err != nil {
				return nil, err
			}
			return val, nil
		},
	))
	// 单节点嵌入式:注册自己为唯一 peer,所有 key 走本地路径
	c.RegisterPeers("local", "local")
	return &Service{cache: c, rdb: rdb, prefix: prefix}
}

// Tools 返回两个工具:cache_get / cache_set。
func (s *Service) Tools() []*tools.Tool {
	return []*tools.Tool{s.cacheGetTool(), s.cacheSetTool()}
}

// cacheGetTool 从 mini-cache 读取 key(未命中回源 Redis)。
func (s *Service) cacheGetTool() *tools.Tool {
	return &tools.Tool{
		Name:         "cache_get",
		Description:  "从分布式缓存读取指定 key 的值;未命中返回提示",
		Parameters:   json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}`),
		IsIdempotent: true,
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Key string `json:"key"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if a.Key == "" {
				return "", errors.New("key 不能为空")
			}
			val, err := s.cache.Get(ctx, a.Key)
			if errors.Is(err, minicache.ErrKeyNotFound) {
				return fmt.Sprintf("缓存未命中: key=%s 不存在", a.Key), nil
			}
			if err != nil {
				return "", fmt.Errorf("缓存读取失败: %w", err)
			}
			return fmt.Sprintf("缓存命中: key=%s value=%s", a.Key, string(val)), nil
		},
	}
}

// cacheSetTool 写入 key:落 Redis(数据源)并失效 mini-cache 缓存。
func (s *Service) cacheSetTool() *tools.Tool {
	return &tools.Tool{
		Name:         "cache_set",
		Description:  "把 key 的值写入分布式缓存(数据源 Redis),下次 cache_get 命中",
		Parameters:   json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"},"value":{"type":"string"}},"required":["key","value"]}`),
		IsIdempotent: true,
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if a.Key == "" {
				return "", errors.New("key 不能为空")
			}
			if err := s.rdb.Set(ctx, s.prefix+a.Key, a.Value, 0).Err(); err != nil {
				return "", fmt.Errorf("写入数据源失败: %w", err)
			}
			s.cache.Del(a.Key) // 失效缓存,下次 Get 重新回源
			return fmt.Sprintf("已写入: key=%s value=%s", a.Key, a.Value), nil
		},
	}
}
