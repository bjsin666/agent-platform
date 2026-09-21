// 程序入口:加载配置 -> 初始化 slog -> 连 MySQL/Redis -> 启动 HTTP。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	agentctx "agent-platform/internal/agent/context"
	"agent-platform/internal/agent/engine"
	"agent-platform/internal/agent/llm"
	"agent-platform/internal/agent/tools"
	"agent-platform/internal/api"
	"agent-platform/internal/cachetool"
	"agent-platform/internal/config"
	"agent-platform/internal/embedding"
	"agent-platform/internal/kb"
	"agent-platform/internal/report"
	"agent-platform/internal/store"
	"agent-platform/internal/trace"
	"agent-platform/internal/usage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("服务启动失败", "err", err)
		os.Exit(1)
	}
}

func run() error {
	// 1) 加载配置(config.yaml + .env)
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("加载配置: %w", err)
	}

	// 2) 初始化 slog(全局默认 logger)
	slog.SetDefault(newLogger(cfg.Log.Level))
	slog.Info("配置加载完成", "server_port", cfg.Env.ServerPort)

	// 3) 连 MySQL(自动建库 + AutoMigrate)与 Redis
	db, err := store.NewMySQL(cfg)
	if err != nil {
		return err
	}
	slog.Info("MySQL 连接成功", "dbname", cfg.Env.DB.DBName, "host", cfg.Env.DB.Host)

	redisClient, err := store.NewRedis(cfg)
	if err != nil {
		return err
	}
	slog.Info("Redis 连接成功", "addr", cfg.Env.Redis.Addr)

	// 4) 装配 Agent 运行时依赖
	// 4.1 embedding client + 知识库服务(Phase 6)
	embedClient := embedding.NewClient(
		cfg.Env.EmbedServiceURL,
		cfg.Embedding.BatchSize,
		cfg.Embedding.Timeout,
		cfg.Embedding.RetryMax,
	)
	kbService := kb.NewService(db, embedClient, cfg.KB.TopK, cfg.KB.MaxChunkChars, cfg.KB.OverlapChars)
	loadCtx, loadCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := kbService.Load(loadCtx); err != nil {
		loadCancel()
		return fmt.Errorf("加载知识库向量缓存: %w", err)
	}
	loadCancel()
	kbService.Start(context.Background(), 2) // 异步入库 worker 随进程常驻

	// 4.2 工具注册表 + 内置工具(kb 真实接通)+ mini-cache 工具(Phase 8)
	registry := tools.NewRegistry()
	if err := tools.RegisterBuiltins(registry, tools.Deps{KB: kbService}); err != nil {
		return fmt.Errorf("注册内置工具: %w", err)
	}
	cacheSvc := cachetool.New(redisClient, "minicache:")
	for _, t := range cacheSvc.Tools() {
		if err := registry.Register(t); err != nil {
			return fmt.Errorf("注册缓存工具: %w", err)
		}
	}
	executor := tools.NewExecutor(registry, tools.Options{
		ToolConcurrency:          cfg.Agent.ToolConcurrency,
		ToolTimeout:              cfg.Agent.ToolTimeout,
		ToolRetry:                cfg.Agent.ToolRetry,
		ToolCircuitFailThreshold: cfg.Agent.ToolCircuitFailThreshold,
		ToolCircuitPause:         cfg.Agent.ToolCircuitPause,
	})
	llmClient := llm.NewClient(cfg, redisClient)
	recorder := trace.NewRecorder()
	traceCtx, traceCancel := context.WithCancel(context.Background())
	defer traceCancel()
	recorder.StartPersist(traceCtx, db) // trace 异步落库 tool_events(Phase 8)

	// 用量统计(Phase 8)
	usageAgg := usage.NewAggregator(db, time.Minute)
	usageCtx, usageCancel := context.WithCancel(context.Background())
	defer usageCancel()
	go usageAgg.Run(usageCtx)

	msgRepo := store.NewMessageRepo(db)
	estimator, err := agentctx.NewTiktokenEstimator()
	if err != nil {
		return fmt.Errorf("初始化 token 估算器: %w", err)
	}
	ctxMgr := agentctx.NewManager(estimator, agentctx.Options{
		MaxTokens:         cfg.Context.MaxTokens,
		RecentRounds:      cfg.Context.RecentRounds,
		CompactKeepRounds: cfg.Context.CompactKeepRounds,
		Summarizer:        llmClient,
	})
	eng := engine.NewEngine(llmClient, registry, executor, msgRepo, recorder, cfg.Agent.MaxIterations)
	eng.WithContext(ctxMgr)
	eng.Usage = usageAgg

	// 4.3 定时报告调度器(Phase 7)
	reportScheduler := report.NewScheduler(
		db,
		report.NewExecutor(db, llmClient, executor, cfg.Report.WorkerConcurrency, cfg.Report.WebhookTimeout, cfg.Report.WebhookRetry),
		cfg.Report.PollInterval,
		2,
	)
	schedulerCtx, schedulerCancel := context.WithCancel(context.Background())
	defer schedulerCancel() // 退出 run() 时停调度器,避免上下文泄漏
	go reportScheduler.Run(schedulerCtx)
	reportScheduler.PollOnce(context.Background()) // 启动即补一次扫描,兜底重启期间到期的任务

	// 5) 构建路由并启动 HTTP 服务
	app := &api.App{
		Cfg:      cfg,
		DB:       db,
		Redis:    redisClient,
		Engine:   eng,
		Sessions: store.NewSessionRepo(db),
		Messages: msgRepo,
		KB:       kbService,
	}
	router := api.NewRouter(app)

	addr := ":" + cfg.Env.ServerPort
	srv := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("HTTP 服务启动", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// 5) 优雅退出:等待信号,给在途请求留 10s 收尾
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return fmt.Errorf("HTTP 服务异常: %w", err)
	case sig := <-quit:
		slog.Info("收到退出信号,开始优雅停机", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("优雅停机失败: %w", err)
	}
	slog.Info("服务已退出")
	return nil
}

// newLogger 按配置级别构建 JSON 结构化日志,输出到 stdout。
func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}
