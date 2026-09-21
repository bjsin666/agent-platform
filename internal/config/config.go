// Package config 负责加载应用配置:先读 config.yaml(非敏感),再用 .env 覆盖敏感项。
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// Config 应用配置总集。yaml 字段来自 config.yaml;Env 来自 .env(后者优先级更高)。
type Config struct {
	Log        LogConfig        `yaml:"log"`
	HTTP       HTTPConfig       `yaml:"http"`
	LLM        LLMConfig        `yaml:"llm"`
	Agent      AgentConfig      `yaml:"agent"`
	Context    ContextConfig    `yaml:"context"`
	Embedding  EmbeddingConfig  `yaml:"embedding"`
	KB         KBConfig         `yaml:"kb"`
	Report     ReportConfig     `yaml:"report"`
	Store      StoreConfig      `yaml:"store"`
	Governance GovernanceConfig `yaml:"governance"`

	// Env 敏感/环境相关配置,来自 .env。
	Env EnvConfig
}

// LogConfig 日志配置。
type LogConfig struct {
	Level string `yaml:"level"`
}

// HTTPConfig HTTP 服务配置。
type HTTPConfig struct {
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
}

// LLMConfig LLM 网关配置(§8.1)。
type LLMConfig struct {
	TimeoutTotal          time.Duration `yaml:"timeout_total"`
	TimeoutFirstToken     time.Duration `yaml:"timeout_first_token"`
	RetryMax              int           `yaml:"retry_max"`
	CircuitFailThreshold  int           `yaml:"circuit_fail_threshold"`
	CircuitOpenDuration   time.Duration `yaml:"circuit_open_duration"`
	CircuitHalfOpenProbes int           `yaml:"circuit_halfopen_probes"`
	CacheTTL              time.Duration `yaml:"cache_ttl"`
}

// AgentConfig 执行引擎配置(§8.3/§8.2)。
type AgentConfig struct {
	MaxIterations            int           `yaml:"max_iterations"`
	ToolConcurrency          int           `yaml:"tool_concurrency"`
	ToolTimeout              time.Duration `yaml:"tool_timeout"`
	ToolRetry                int           `yaml:"tool_retry"`
	ToolCircuitFailThreshold int           `yaml:"tool_circuit_fail_threshold"`
	ToolCircuitPause         time.Duration `yaml:"tool_circuit_pause"`
}

// ContextConfig 上下文管理配置(§8.4)。
type ContextConfig struct {
	MaxTokens         int    `yaml:"max_tokens"`
	RecentRounds      int    `yaml:"recent_rounds"`
	CompactKeepRounds int    `yaml:"compact_keep_rounds"`
	Estimator         string `yaml:"estimator"`
}

// EmbeddingConfig Go 侧 embedding client 配置(§8.7)。
type EmbeddingConfig struct {
	BatchSize int           `yaml:"batch_size"`
	Timeout   time.Duration `yaml:"timeout"`
	RetryMax  int           `yaml:"retry_max"`
}

// KBConfig 知识库配置(§8.5)。
type KBConfig struct {
	TopK          int `yaml:"top_k"`
	MaxChunkChars int `yaml:"max_chunk_chars"`
	OverlapChars  int `yaml:"overlap_chars"`
}

// ReportConfig 定时报告配置(§8.6)。
type ReportConfig struct {
	PollInterval      time.Duration `yaml:"poll_interval"`
	WorkerConcurrency int           `yaml:"worker_concurrency"`
	WebhookRetry      int           `yaml:"webhook_retry"`
	WebhookTimeout    time.Duration `yaml:"webhook_timeout"`
}

// StoreConfig 存储连接池配置(§8.8)。
type StoreConfig struct {
	MySQLMaxOpen  int `yaml:"mysql_max_open"`
	MySQLMaxIdle  int `yaml:"mysql_max_idle"`
	RedisPoolSize int `yaml:"redis_pool_size"`
}

// GovernanceConfig 治理配置(Phase 8):API Key 认证与 Redis 令牌桶限流。
type GovernanceConfig struct {
	RateLimit RateLimitConfig `yaml:"rate_limit"`
}

// RateLimitConfig Redis 令牌桶限流参数。
type RateLimitConfig struct {
	Rate  float64 `yaml:"rate"`  // 每秒补充令牌数
	Burst int     `yaml:"burst"` // 桶容量(突发上限)
}

// EnvConfig 来自 .env 的配置项(敏感项优先放这里)。
type EnvConfig struct {
	DeepSeekAPIKey  string
	DeepSeekBaseURL string
	DeepSeekModel   string
	EmbedServiceURL string
	AgentAPIKey     string // API Key 认证(为空则跳过认证)
	DB              DBConfig
	Redis           RedisConfig
	ServerPort      string
}

// DBConfig MySQL 连接信息。
type DBConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
}

// DSN 生成 GORM DSN。dbName 传空串表示不指定库(用于建库)。
func (d DBConfig) DSN(dbName string) string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		d.User, d.Password, d.Host, d.Port, dbName)
}

// RedisConfig Redis 连接信息。
type RedisConfig struct {
	Addr     string
	Password string
}

// Load 加载配置:先读 config.yaml(允许缺省),再读 .env(允许缺省)覆盖敏感项。
// 为什么允许缺省:本地无 .env 时仍可启动做连通检查(除认证项外均有默认值)。
func Load() (*Config, error) {
	cfg := defaultConfig()

	// 1) config.yaml(非敏感)
	if data, err := os.ReadFile("config.yaml"); err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("解析 config.yaml: %w", err)
		}
	}

	// 2) .env(敏感,覆盖)
	if err := godotenv.Load(); err != nil {
		// .env 缺失:忽略,使用默认值/空值(例如本地连通检查场景)
	}
	cfg.applyEnv()
	return cfg, nil
}

// applyEnv 用环境变量覆盖 Env 段。仅覆盖非空值,便于 .env 留空时使用默认值。
func (c *Config) applyEnv() {
	get := os.Getenv
	if v := get("DEEPSEEK_API_KEY"); v != "" {
		c.Env.DeepSeekAPIKey = v
	}
	if v := get("DEEPSEEK_BASE_URL"); v != "" {
		c.Env.DeepSeekBaseURL = v
	}
	if v := get("DEEPSEEK_MODEL"); v != "" {
		c.Env.DeepSeekModel = v
	}
	if v := get("EMBED_SERVICE_URL"); v != "" {
		c.Env.EmbedServiceURL = v
	}
	if v := get("MYSQL_HOST"); v != "" {
		c.Env.DB.Host = v
	}
	if v := get("MYSQL_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &c.Env.DB.Port)
	}
	if v := get("MYSQL_USER"); v != "" {
		c.Env.DB.User = v
	}
	if v := get("MYSQL_PASSWORD"); v != "" {
		c.Env.DB.Password = v
	}
	if v := get("MYSQL_DBNAME"); v != "" {
		c.Env.DB.DBName = v
	}
	if v := get("REDIS_ADDR"); v != "" {
		c.Env.Redis.Addr = v
	}
	if v := get("REDIS_PASSWORD"); v != "" {
		c.Env.Redis.Password = v
	}
	if v := get("SERVER_PORT"); v != "" {
		c.Env.ServerPort = v
	}
	if v := get("AGENT_API_KEY"); v != "" {
		c.Env.AgentAPIKey = v
	}
}

// defaultConfig 返回带缺省值的配置,缺省值对齐 §7/§8 的默认规格。
func defaultConfig() *Config {
	return &Config{
		Log: LogConfig{Level: "info"},
		HTTP: HTTPConfig{
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		LLM: LLMConfig{
			TimeoutTotal:          60 * time.Second,
			TimeoutFirstToken:     5 * time.Second,
			RetryMax:              2,
			CircuitFailThreshold:  5,
			CircuitOpenDuration:   30 * time.Second,
			CircuitHalfOpenProbes: 1,
			CacheTTL:              1 * time.Hour,
		},
		Agent: AgentConfig{
			MaxIterations:            10,
			ToolConcurrency:          3,
			ToolTimeout:              10 * time.Second,
			ToolRetry:                2,
			ToolCircuitFailThreshold: 3,
			ToolCircuitPause:         30 * time.Second,
		},
		Context: ContextConfig{
			MaxTokens:         8000,
			RecentRounds:      10,
			CompactKeepRounds: 5,
			Estimator:         "calibrated",
		},
		Embedding: EmbeddingConfig{
			BatchSize: 32,
			Timeout:   10 * time.Second,
			RetryMax:  2,
		},
		KB: KBConfig{
			TopK:          10,
			MaxChunkChars: 800,
			OverlapChars:  50,
		},
		Report: ReportConfig{
			PollInterval:      60 * time.Second,
			WorkerConcurrency: 3,
			WebhookRetry:      2,
			WebhookTimeout:    10 * time.Second,
		},
		Store: StoreConfig{
			MySQLMaxOpen:  50,
			MySQLMaxIdle:  10,
			RedisPoolSize: 20,
		},
		Governance: GovernanceConfig{
			RateLimit: RateLimitConfig{
				Rate:  10, // 每秒 10 令牌
				Burst: 20, // 桶容量 20
			},
		},
		Env: EnvConfig{
			DeepSeekBaseURL: "https://api.deepseek.com",
			DeepSeekModel:   "deepseek-chat",
			EmbedServiceURL: "http://127.0.0.1:8001",
			DB: DBConfig{
				Host:   "127.0.0.1",
				Port:   3306,
				User:   "root",
				DBName: "agent",
			},
			Redis:      RedisConfig{Addr: "127.0.0.1:6379"},
			ServerPort: "8080",
		},
	}
}
