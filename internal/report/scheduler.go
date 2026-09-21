package report

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"agent-platform/internal/model"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

// Scheduler 定时报告调度器:每 pollInterval 扫描到期任务,乐观锁抢占后异步执行(§8.6)。
type Scheduler struct {
	db           *gorm.DB
	executor     *Executor
	pollInterval time.Duration
	runSem       chan struct{} // 并发执行上限
}

// NewScheduler 构造调度器。
func NewScheduler(db *gorm.DB, executor *Executor, pollInterval time.Duration, runConcurrency int) *Scheduler {
	if pollInterval <= 0 {
		pollInterval = time.Minute
	}
	return &Scheduler{
		db:           db,
		executor:     executor,
		pollInterval: pollInterval,
		runSem:       make(chan struct{}, runConcurrency),
	}
}

// Run 主循环,直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	slog.Info("报告调度器启动", "poll_interval", s.pollInterval.String())
	for {
		select {
		case <-ctx.Done():
			slog.Info("报告调度器退出")
			return
		case <-ticker.C:
			s.PollOnce(ctx)
		}
	}
}

// PollOnce 手动触发一次扫描(供测试与启动兜底)。
func (s *Scheduler) PollOnce(ctx context.Context) {
	now := time.Now()
	var tasks []model.ReportTask
	if err := s.db.WithContext(ctx).
		Where("status = ? AND next_run_at IS NOT NULL AND next_run_at <= ?", "active", now).
		Find(&tasks).Error; err != nil {
		slog.Error("扫描报告任务失败", "err", err)
		return
	}
	for i := range tasks {
		s.runTask(ctx, &tasks[i])
	}
}

// runTask 乐观锁抢占:仅当 next_run_at 仍为旧值才推进,避免并发/多实例重复执行(§8.6)。
func (s *Scheduler) runTask(ctx context.Context, task *model.ReportTask) {
	now := time.Now()
	schedule, err := cron.ParseStandard(task.CronExpr)
	if err != nil {
		slog.Error("cron 表达式非法,暂停任务", "task_id", task.ID, "err", err)
		s.db.WithContext(ctx).Model(&model.ReportTask{}).Where("id=?", task.ID).
			Update("status", "paused")
		return
	}
	next := schedule.Next(now)

	res := s.db.WithContext(ctx).Model(&model.ReportTask{}).
		Where("id = ? AND next_run_at = ?", task.ID, task.NextRunAt).
		Updates(map[string]any{"next_run_at": next, "last_run_at": &now})
	if res.Error != nil {
		slog.Error("推进任务 next_run_at 失败", "task_id", task.ID, "err", res.Error)
		return
	}
	if res.RowsAffected == 0 {
		slog.Debug("任务已被其他调度抢占,跳过", "task_id", task.ID)
		return
	}

	// 创建 report_run(pending)并异步执行
	run := model.ReportRun{
		TaskID:    task.ID,
		RunID:     genID(),
		Status:    "pending",
		StartedAt: &now,
	}
	if err := s.db.WithContext(ctx).Create(&run).Error; err != nil {
		slog.Error("创建 report_run 失败", "task_id", task.ID, "err", err)
		return
	}
	slog.Info("报告任务触发", "task_id", task.ID, "run_id", run.RunID)
	go s.executeRun(ctx, task, &run)
}

// executeRun 执行报告并更新 run 状态/内容/用量。
func (s *Scheduler) executeRun(ctx context.Context, task *model.ReportTask, run *model.ReportRun) {
	s.runSem <- struct{}{} // 限流
	defer func() { <-s.runSem }()

	s.db.WithContext(ctx).Model(&model.ReportRun{}).Where("id=?", run.ID).
		Update("status", "running")

	result, err := s.executor.Execute(ctx, task)
	finished := time.Now()
	updates := map[string]any{"finished_at": &finished}
	if err != nil {
		updates["status"] = "failed"
		updates["error"] = err.Error()
		slog.Error("报告执行失败", "task_id", task.ID, "run_id", run.RunID, "err", err)
	} else {
		updates["status"] = "success"
		updates["content"] = result.Content
		if usageJSON, e := json.Marshal(result.Usage); e == nil {
			updates["token_usage"] = usageJSON
		}
		slog.Info("报告执行完成", "task_id", task.ID, "run_id", run.RunID, "status", "success")
	}
	if err := s.db.WithContext(ctx).Model(&model.ReportRun{}).Where("id=?", run.ID).
		Updates(updates).Error; err != nil {
		slog.Error("更新 report_run 失败", "run_id", run.RunID, "err", err)
	}
}

// genID 生成随机 run id。
func genID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
