package llm

import (
	"sync"
	"time"
)

// cbState 熔断器状态。
type cbState int

const (
	cbClosed   cbState = iota // 正常放行
	cbOpen                    // 熔断,拒绝请求
	cbHalfOpen                // 半开,放行少量探测请求
)

// circuitBreaker 熔断器:连续 threshold 次失败 -> open 保持 openDuration
// -> 到期进入 half-open 放行 halfOpenProbes 个探测请求,探测成功恢复 closed,失败重新 open。
// 为什么自实现:依赖库过重,且该语义简单明确(§8.1)。
type circuitBreaker struct {
	mu             sync.Mutex
	state          cbState
	failCount      int
	threshold      int
	openDuration   time.Duration
	halfOpenProbes int
	probesLeft     int
	openedAt       time.Time
}

// newCircuitBreaker 构造熔断器。
func newCircuitBreaker(threshold int, openDuration time.Duration, halfOpenProbes int) *circuitBreaker {
	return &circuitBreaker{
		state:          cbClosed,
		threshold:      threshold,
		openDuration:   openDuration,
		halfOpenProbes: halfOpenProbes,
	}
}

// allow 是否允许发请求。
func (cb *circuitBreaker) allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	now := time.Now()
	switch cb.state {
	case cbOpen:
		if now.Before(cb.openedAt.Add(cb.openDuration)) {
			return false
		}
		// open 到期:转入 half-open,放行探测请求
		cb.state = cbHalfOpen
		cb.probesLeft = cb.halfOpenProbes
		fallthrough
	case cbHalfOpen:
		if cb.probesLeft <= 0 {
			return false
		}
		cb.probesLeft--
		return true
	default: // cbClosed
		return true
	}
}

// success 请求成功:closed 时清零失败计数;half-open 探测成功则恢复 closed。
func (cb *circuitBreaker) success() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case cbClosed:
		cb.failCount = 0
	case cbHalfOpen:
		cb.state = cbClosed
		cb.failCount = 0
	}
}

// failure 请求失败:closed 累计到阈值则 open;half-open 探测失败则重新 open。
func (cb *circuitBreaker) failure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case cbClosed:
		cb.failCount++
		if cb.failCount >= cb.threshold {
			cb.state = cbOpen
			cb.openedAt = time.Now()
		}
	case cbHalfOpen:
		cb.state = cbOpen
		cb.openedAt = time.Now()
	}
}
