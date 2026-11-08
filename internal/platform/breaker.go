package platform

import (
	"sync"
	"time"
)

// 熔断器：Redis 持续不可用时，避免每个请求都去等连接超时。
//
// 三态：
//   - Closed：正常放行，成功即清零失败计数；
//   - Open：连续失败达到阈值后打开，调用方直接走降级，**不再触碰 Redis**（快速失败）；
//   - HalfOpen：冷却期满后放行一次探测；成功回 Closed，失败回 Open。
//
// 与降级（fallback）的分工：降级决定「Redis 挂了怎么办」，熔断决定「什么时候不再尝试」。
// 两者配合才能在 Redis 长时间不可用时既不丢功能、也不被拖慢。

// BreakerState 是熔断器状态。
type BreakerState int

const (
	// BreakerClosed 正常放行。
	BreakerClosed BreakerState = iota
	// BreakerOpen 已熔断，直接降级。
	BreakerOpen
	// BreakerHalfOpen 半开：放行一次探测。
	BreakerHalfOpen
)

func (s BreakerState) String() string {
	switch s {
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// breaker 是并发安全的熔断器实现。
type breaker struct {
	mu        sync.Mutex
	state     BreakerState
	failures  int
	threshold int
	cooldown  time.Duration
	openedAt  time.Time
}

func newBreaker(threshold int, cooldown time.Duration) *breaker {
	if threshold <= 0 {
		threshold = 5
	}
	if cooldown <= 0 {
		cooldown = 5 * time.Second
	}
	return &breaker{state: BreakerClosed, threshold: threshold, cooldown: cooldown}
}

// allow 判断本次是否放行到 Redis；放行时若处于半开，会记录探测。
func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case BreakerOpen:
		if time.Since(b.openedAt) >= b.cooldown {
			b.state = BreakerHalfOpen
			return true // 放行一次探测
		}
		return false
	case BreakerHalfOpen:
		// 已在探测中，其余调用继续降级。
		return false
	default:
		return true
	}
}

// onSuccess 记录一次成功：任何状态下都回到 Closed 并清零。
func (b *breaker) onSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = BreakerClosed
	b.failures = 0
}

// onFailure 记录一次失败：达到阈值则打开熔断。
func (b *breaker) onFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.state == BreakerHalfOpen || b.failures >= b.threshold {
		b.state = BreakerOpen
		b.openedAt = time.Now()
	}
}

func (b *breaker) currentState() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	// 冷却期满后对外报告为半开（下一次调用会真正转入）。
	if b.state == BreakerOpen && time.Since(b.openedAt) >= b.cooldown {
		return BreakerHalfOpen
	}
	return b.state
}

// BreakerState 返回缓存层熔断器的当前状态（运维观测与测试用）。
func (c *RedisCache) BreakerState() BreakerState {
	return c.breaker.currentState()
}
