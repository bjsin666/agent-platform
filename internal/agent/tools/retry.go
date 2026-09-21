package tools

import (
	"context"
	"math/rand"
	"time"
)

// backoff 指数退避 + 随机 jitter。
func backoff(attempt int) time.Duration {
	base := 50 * time.Millisecond
	nominal := base << (attempt - 1)
	return nominal + time.Duration(rand.Int63n(int64(nominal)))
}

// sleep 可被 ctx 中断的睡眠,返回 false 表示 ctx 已取消。
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
