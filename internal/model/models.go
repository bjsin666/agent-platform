// Package model 定义 9 张业务表的 GORM 模型(见 plan0.md §6)。
package model

import (
	"encoding/json"
	"time"
)

// User 用户(租户内)。APIKeyHash 仅存哈希,明文绝不落库。
type User struct {
	ID         uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	TenantID   string    `gorm:"size:64;index" json:"tenant_id"`
	Name       string    `gorm:"size:128" json:"name"`
	APIKeyHash string    `gorm:"size:128" json:"-"`
	CreatedAt  time.Time `json:"created_at"`
}

// TableName 指定表名。
func (User) TableName() string { return "users" }

// KbDocument 知识库文档。Status: processing | ready | failed。
type KbDocument struct {
	ID         uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	TenantID   string    `gorm:"size:64;index" json:"tenant_id"`
	Title      string    `gorm:"size:255" json:"title"`
	Source     string    `gorm:"size:255" json:"source"` // 文件名/来源
	Status     string    `gorm:"size:32;default:processing" json:"status"`
	ChunkCount int       `json:"chunk_count"`
	CreatedAt  time.Time `json:"created_at"`
}

// TableName 指定表名。
func (KbDocument) TableName() string { return "kb_documents" }

// KbChunk 知识库文本块。Embedding 存 float32 序列化字节(encoding/binary)。
// content 上的 FULLTEXT 索引用于关键词检索路(§8.5)。
type KbChunk struct {
	ID        uint64          `gorm:"primaryKey;autoIncrement" json:"id"`
	DocID     uint64          `gorm:"index" json:"doc_id"`
	Seq       int             `json:"seq"`
	Content   string          `gorm:"type:text;index:idx_content,class:FULLTEXT" json:"content"`
	Embedding []byte          `gorm:"type:blob" json:"-"` // float32 序列化
	Meta      json.RawMessage `gorm:"type:json" json:"meta"`
	CreatedAt time.Time       `json:"created_at"`
}

// TableName 指定表名。
func (KbChunk) TableName() string { return "kb_chunks" }

// Session 会话。Status: active | archived。
type Session struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	TenantID  string    `gorm:"size:64;index" json:"tenant_id"`
	UserID    uint64    `gorm:"index" json:"user_id"`
	Title     string    `gorm:"size:255" json:"title"`
	Status    string    `gorm:"size:16;default:active" json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName 指定表名。
func (Session) TableName() string { return "sessions" }

// Message 会话消息。Role: user | assistant | system | tool。ToolCalls 存 LLM 返回的 tool_calls。
type Message struct {
	ID        uint64          `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID uint64          `gorm:"index" json:"session_id"`
	Role      string          `gorm:"size:16" json:"role"`
	Content   string          `gorm:"type:text" json:"content"`
	ToolCalls json.RawMessage `gorm:"type:json" json:"tool_calls"`
	Tokens    int             `json:"tokens"`
	CreatedAt time.Time       `json:"created_at"`
}

// TableName 指定表名。
func (Message) TableName() string { return "messages" }

// ReportTask 定时报告任务。KbIDs 为 []uint64 JSON;NotifyConfig 为通知配置 JSON。
type ReportTask struct {
	ID             uint64          `gorm:"primaryKey;autoIncrement" json:"id"`
	TenantID       string          `gorm:"size:64;index" json:"tenant_id"`
	Name           string          `gorm:"size:128" json:"name"`
	CronExpr       string          `gorm:"size:128" json:"cron_expr"`
	KbIDs          json.RawMessage `gorm:"type:json" json:"kb_ids"`
	PromptTemplate string          `gorm:"type:text" json:"prompt_template"`
	NotifyChannel  string          `gorm:"size:32" json:"notify_channel"` // webhook
	NotifyConfig   json.RawMessage `gorm:"type:json" json:"notify_config"`
	Status         string          `gorm:"size:16;default:active" json:"status"` // active | paused
	LastRunAt      *time.Time      `json:"last_run_at"`
	NextRunAt      *time.Time      `json:"next_run_at"`
	CreatedAt      time.Time       `json:"created_at"`
}

// TableName 指定表名。
func (ReportTask) TableName() string { return "report_tasks" }

// ReportRun 报告执行记录。TokenUsage 为 {prompt,completion,cost} JSON。
type ReportRun struct {
	ID         uint64          `gorm:"primaryKey;autoIncrement" json:"id"`
	TaskID     uint64          `gorm:"index" json:"task_id"`
	RunID      string          `gorm:"size:64" json:"run_id"`
	Status     string          `gorm:"size:16;default:pending" json:"status"` // pending | running | success | failed
	Content    string          `gorm:"type:longtext" json:"content"`
	TokenUsage json.RawMessage `gorm:"type:json" json:"token_usage"`
	Error      string          `gorm:"type:text" json:"error"`
	StartedAt  *time.Time      `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
	CreatedAt  time.Time       `json:"created_at"`
}

// TableName 指定表名。
func (ReportRun) TableName() string { return "report_runs" }

// UsageStat 用量统计(每日聚合,Phase 8 使用)。(tenant_id, date) 唯一,便于 upsert。
type UsageStat struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	TenantID         string    `gorm:"size:64;uniqueIndex:idx_tenant_date" json:"tenant_id"`
	Date             string    `gorm:"size:10;uniqueIndex:idx_tenant_date" json:"date"` // YYYY-MM-DD
	LLMCalls         int       `json:"llm_calls"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	Cost             float64   `json:"cost"`
	LatencyP95       int64     `json:"latency_p95"` // ms
	CreatedAt        time.Time `json:"created_at"`
}

// TableName 指定表名。
func (UsageStat) TableName() string { return "usage_stats" }

// ToolEvent trace 事件(§8.9)。EventType: tool | llm | agent | node。
type ToolEvent struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	RunID        string    `gorm:"size:64;index" json:"run_id"`
	TraceID      string    `gorm:"size:64;index" json:"trace_id"`
	SpanID       string    `gorm:"size:64" json:"span_id"`
	ParentSpanID string    `gorm:"size:64" json:"parent_span_id"`
	EventType    string    `gorm:"size:32" json:"event_type"`
	Name         string    `gorm:"size:128" json:"name"`
	Input        string    `gorm:"type:text" json:"input"`
	Output       string    `gorm:"type:longtext" json:"output"`
	LatencyMS    int64     `json:"latency_ms"`
	Status       string    `gorm:"size:16" json:"status"` // success | error | timeout
	CreatedAt    time.Time `json:"created_at"`
}

// TableName 指定表名。
func (ToolEvent) TableName() string { return "tool_events" }
