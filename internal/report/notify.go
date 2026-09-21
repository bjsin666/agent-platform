package report

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"time"

	"agent-platform/internal/model"
)

// notify 按通知渠道投递报告。
// console:本地调试渠道,结构化日志输出,不依赖外部服务(演示/面试友好)
// webhook:POST 到配置的 URL,失败重试 webhookRetry 次
func (x *Executor) notify(ctx context.Context, task *model.ReportTask, content string) error {
	switch task.NotifyChannel {
	case "console", "":
		slog.Info("报告已生成(console 渠道)",
			"task_id", task.ID, "task_name", task.Name, "content", content)
		return nil
	default:
		return x.notifyWebhook(ctx, task, content)
	}
}

// notifyWebhook 把报告 POST 到 webhook,失败重试 webhookRetry 次。
func (x *Executor) notifyWebhook(ctx context.Context, task *model.ReportTask, content string) error {
	var cfg struct {
		URL string `json:"url"`
	}
	if len(task.NotifyConfig) > 0 {
		if err := json.Unmarshal(task.NotifyConfig, &cfg); err != nil {
			return fmt.Errorf("解析通知配置: %w", err)
		}
	}
	if cfg.URL == "" {
		return fmt.Errorf("任务 %s 未配置 webhook URL", task.Name)
	}

	payload, err := json.Marshal(map[string]any{
		"task_id":   task.ID,
		"task_name": task.Name,
		"content":   content,
	})
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := 0; attempt <= x.webhookRetry; attempt++ {
		if attempt > 0 {
			if !sleepCtx(ctx, 500*time.Millisecond+time.Duration(rand.Int63n(300))*time.Millisecond) {
				return ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: x.webhookTimeout}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("webhook 返回 %d", resp.StatusCode)
			continue
		}
		lastErr = fmt.Errorf("webhook 请求失败: %w", err)
	}
	return fmt.Errorf("webhook 通知失败(已重试 %d 次): %w", x.webhookRetry, lastErr)
}

// sleepCtx 可被 ctx 中断的睡眠。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
