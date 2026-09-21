package report

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/model"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestSchedulerIntegration 端到端:真实 MySQL + 假 LLM + 本地 webhook。
// 任务 next_run_at 置为过去 -> PollOnce 触发 -> 报告执行 -> webhook 收到。
// 默认跳过;设置 KB_TEST_INTEGRATION=1 且 MYSQL_DSN 可用时执行。
func TestSchedulerIntegration(t *testing.T) {
	if os.Getenv("KB_TEST_INTEGRATION") != "1" {
		t.Skip("设置 KB_TEST_INTEGRATION=1 运行调度器集成测试")
	}
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		t.Skip("未设置 MYSQL_DSN")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连接 MySQL: %v", err)
	}

	// 本地 webhook 接收端
	var received chan string = make(chan string, 1)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		select {
		case received <- string(buf):
		default:
		}
		w.WriteHeader(200)
	}))
	defer webhook.Close()

	exec, _ := newReportExecutor(t, `{"title":"调度报告","summary":"s","sections":[]}`, webhook.URL)
	sched := NewScheduler(db, exec, time.Hour, 2)

	// 建任务,next_run_at 置为过去
	past := time.Now().Add(-time.Minute)
	notify, _ := json.Marshal(map[string]string{"url": webhook.URL})
	task := model.ReportTask{
		Name:           "集成调度",
		CronExpr:       "*/1 * * * *",
		KbIDs:          json.RawMessage(`[1]`),
		PromptTemplate: "生成摘要",
		NotifyChannel:  "webhook",
		NotifyConfig:   notify,
		Status:         "active",
		NextRunAt:      &past,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务: %v", err)
	}
	defer db.Delete(&task)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sched.PollOnce(ctx)

	// 等待 webhook 收到报告
	select {
	case body := <-received:
		if !strings.Contains(body, "调度报告") {
			t.Fatalf("webhook 内容错误: %s", body)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("等待 webhook 超时")
	}

	// 轮询等待 report_run 完成(notify 与状态更新之间有窗口)
	var run model.ReportRun
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := db.Where("task_id=?", task.ID).Order("id DESC").First(&run).Error; err != nil {
			t.Fatalf("查询 report_run: %v", err)
		}
		if run.Status == "success" || run.Status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("report_run 未在时限内完成,当前 status=%s", run.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if run.Status != "success" || run.Content == "" {
		t.Fatalf("report_run 状态错误: status=%s", run.Status)
	}
	if len(run.TokenUsage) == 0 {
		t.Fatalf("report_run 应记录 token_usage")
	}

	// 校验 next_run_at 已推进(乐观锁生效)
	var updated model.ReportTask
	db.First(&updated, task.ID)
	if updated.NextRunAt == nil || !updated.NextRunAt.After(past) {
		t.Fatalf("next_run_at 未推进")
	}
}
