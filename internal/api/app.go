// Package api 提供 Gin 路由、中间件与 HTTP handler。
package api

import (
	"agent-platform/internal/agent/engine"
	"agent-platform/internal/config"
	"agent-platform/internal/kb"
	"agent-platform/internal/store"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// App 聚合各依赖供 handler 使用。随 Phase 演进逐项补充。
type App struct {
	Cfg      *config.Config
	DB       *gorm.DB
	Redis    *redis.Client
	Engine   *engine.Engine     // 执行引擎(Phase 5 起)
	Sessions *store.SessionRepo // 会话仓储(Phase 5 起)
	Messages *store.MessageRepo // 消息仓储(Phase 5 起)
	KB       *kb.Service        // 知识库服务(Phase 6 起)
}
