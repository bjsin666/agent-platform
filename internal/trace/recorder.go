// Package trace 记录 Agent/DAG 执行的追踪事件(§8.9)。
package trace

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/model"

	"gorm.io/gorm"
)

// Event trace 事件。EventType: agent | llm | tool | node。
type Event struct {
	RunID        string // 一次执行的根 ID(如一次 AgentRun)
	TraceID      string // 追踪链 ID
	SpanID       string // 当前 span
	ParentSpanID string // 父 span
	EventType    string
	Name         string
	Input        string
	Output       string
	LatencyMS    int64
	Status       string // success | error | timeout
	CreatedAt    time.Time
}

// Recorder trace 记录器:内存环形保留 + 可选异步落库 tool_events(Phase 8)。
type Recorder struct {
	mu     sync.Mutex
	events []Event // 内存保留(近期可查)

	db *gorm.DB // 非空时异步落库
	ch chan Event
}

// NewRecorder 创建记录器。
func NewRecorder() *Recorder {
	return &Recorder{}
}

// StartPersist 启动异步落库 worker:事件经缓冲通道批量写入 tool_events。
// db 为空时调用不生效(纯内存模式)。
func (r *Recorder) StartPersist(ctx context.Context, db *gorm.DB) {
	if db == nil {
		return
	}
	r.mu.Lock()
	r.db = db
	r.ch = make(chan Event, 256)
	r.mu.Unlock()
	go r.persistLoop(ctx)
	slog.Info("trace 异步落库已启动")
}

// Record 记录一条事件,自动补时间戳;启用落库时同时入队。
func (r *Recorder) Record(e Event) {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	r.mu.Lock()
	r.events = append(r.events, e)
	ch := r.ch
	r.mu.Unlock()
	if ch != nil {
		select {
		case ch <- e:
		default: // 队列满:内存已存,落库丢弃(不阻塞主链路)
		}
	}
}

// persistLoop 批量落库:每 1s 或攒够 100 条刷一次。
func (r *Recorder) persistLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var batch []Event
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := r.insertBatch(ctx, batch); err != nil {
			slog.Warn("trace 落库失败", "count", len(batch), "err", err)
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case e := <-r.ch:
			batch = append(batch, e)
			if len(batch) >= 100 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// insertBatch 批量写入 tool_events。
func (r *Recorder) insertBatch(ctx context.Context, events []Event) error {
	rows := make([]model.ToolEvent, 0, len(events))
	for _, e := range events {
		rows = append(rows, model.ToolEvent{
			RunID:        e.RunID,
			TraceID:      e.TraceID,
			SpanID:       e.SpanID,
			ParentSpanID: e.ParentSpanID,
			EventType:    e.EventType,
			Name:         e.Name,
			Input:        strings.ToValidUTF8(e.Input, ""), // 兜底:外部内容可能含非法 UTF-8,清掉避免 1366
			Output:       strings.ToValidUTF8(e.Output, ""),
			LatencyMS:    e.LatencyMS,
			Status:       e.Status,
			CreatedAt:    e.CreatedAt,
		})
	}
	return r.db.WithContext(ctx).CreateInBatches(rows, 100).Error
}

// Query 按 RunID 返回全部事件(按记录顺序)。
func (r *Recorder) Query(runID string) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, e := range r.events {
		if e.RunID == runID {
			out = append(out, e)
		}
	}
	return out
}
