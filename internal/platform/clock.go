package platform

// 时钟能力（原 caps_mock.go 的 MockClock / RealClock 段）。
//
// 拆出来的理由：RealClock 是**生产实现**（装配层 15 处注入它），
// 而 MockClock 只是测试替身。两者同住一个 caps_mock.go 是误导。

import (
	"sync"
	"time"
)

// MockClock 是 Clock 的可设定实现，供测试注入固定时刻。
type MockClock struct {
	mu  sync.RWMutex
	now time.Time
}

// NewMockClock 构造时钟；传入零值时回落到系统当前时间（保证 Now 非零）。
func NewMockClock(t time.Time) *MockClock {
	if t.IsZero() {
		t = time.Now()
	}
	return &MockClock{now: t}
}

// Now 实现 Clock。
func (c *MockClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

// Set 重设当前时刻。
func (c *MockClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// RealClock 是 Clock 的真实实现：直接读系统时间，供装配层注入。
type RealClock struct{}

// NewRealClock 构造真实时钟。
func NewRealClock() *RealClock { return &RealClock{} }

// Now 实现 Clock。
func (*RealClock) Now() time.Time { return time.Now() }
