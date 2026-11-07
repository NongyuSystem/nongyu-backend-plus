package jiaowu

import (
	"context"
	"hash/fnv"
	"sync"
	"time"

	"nongyu-backend-plus/internal/platform"
)

// 观测驱动续期（#17 / #7 决议）：不猜未公开的 TTL，用「成功时刻 / 首次失败时刻」
// 反推保守周期；观察期以长期凭据已知窗口留出余量作为初始上限。
//
// 调度器只做「挑候选 + 错峰 + 记录观测 + 定时驱动」，**不自行发换票请求**
// （避免绕过出站闸口、也避免静默重试刷屏触发风控）；真正执行换票由注入的
// RenewExecutor 经 Service 的闸口完成。

const (
	// observationWindow 是观察期初始上限：9 天（10 天长期凭据窗口留 1 天余量）。
	observationWindow = 9 * 24 * time.Hour
	// minObservedPeriod 是周期下界，防止反复失败把周期压到接近零。
	minObservedPeriod = time.Hour
	// staggerSlots 是把候选散列到的错峰槽位数。
	staggerSlots = 16
	// renewBatchLimit 是单轮续期扫描最多处理的候选数，避免一次扫全表。
	renewBatchLimit = 64
)

// RenewItem 是续期计划里的一项：用户 + 相对调度偏移。
type RenewItem struct {
	UserID string
	Offset int64
}

// RenewExecutor 是真正执行一名用户续期的回调，由 Service 提供（经出站闸口）。
// 调度器本身不发请求，只把错峰后的候选交给它。
type RenewExecutor func(ctx context.Context, userID string)

// RenewScheduler 从外置存储挑「即将过期」的候选、按用户散列错峰，并维护观测周期。
type RenewScheduler struct {
	mu        sync.Mutex
	tickets   platform.TicketStore
	clock     platform.Clock
	observed  time.Duration
	lastOK    time.Time
	lastFail  time.Time
	stopOnce  sync.Once
	stopChan  chan struct{}
	runOnceMu sync.Mutex
}

// NewRenewScheduler 构造续期调度器；初始观测周期取观察期上限。
// 不接收 HTTPDoer：出站只能经 Service 的闸口，调度器不持有任何出站能力。
func NewRenewScheduler(tickets platform.TicketStore, clock platform.Clock) *RenewScheduler {
	return &RenewScheduler{
		tickets:  tickets,
		clock:    clock,
		observed: observationWindow,
		stopChan: make(chan struct{}),
	}
}

// Plan 挑出过期时刻早于 before 的候选（最多 limit 条），按 userID 散列到错峰槽位。
// 候选来自 platform.TicketStore.ListExpiringSoon（外置、进程无状态）。
func (s *RenewScheduler) Plan(ctx context.Context, before time.Time, limit int) ([]RenewItem, error) {
	refs, err := s.tickets.ListExpiringSoon(ctx, before, limit)
	if err != nil {
		return nil, err
	}
	out := make([]RenewItem, 0, len(refs))
	for _, ref := range refs {
		h := fnv.New32a()
		_, _ = h.Write([]byte(ref.UserID))
		out = append(out, RenewItem{
			UserID: ref.UserID,
			Offset: int64(h.Sum32() % staggerSlots),
		})
	}
	return out, nil
}

// ObservedPeriod 返回当前观测到的保守续期周期。
func (s *RenewScheduler) ObservedPeriod() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observed
}

// RecordSuccess 记录一次换票成功时刻；周期取「距上次成功」的正向间隔，夹在
// (0, observationWindow] 内，绝不硬编码未知超时。
func (s *RenewScheduler) RecordSuccess(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.lastOK.IsZero() && at.After(s.lastOK) {
		if d := at.Sub(s.lastOK); d > 0 && d < s.observed {
			s.observed = d
		}
	}
	if s.observed < minObservedPeriod {
		s.observed = minObservedPeriod
	}
	if s.observed > observationWindow {
		s.observed = observationWindow
	}
	s.lastOK = at
}

// PlanningWindow 返回本次续期扫描的候选窗口：以现在为基准、向前看一个观测周期。
// Plan 消费它，使「观测反推」真正参与挑选候选，而不是死配置。
func (s *RenewScheduler) PlanningWindow(now time.Time) time.Time {
	return now.Add(s.ObservedPeriod())
}

// RecordFailure 记录首次失败时刻；此后周期保守收紧（至少缩短一半、夹在
// [minObservedPeriod, observationWindow] 内），避免在同一失效点反复尝试。
func (s *RenewScheduler) RecordFailure(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastFail = at
	s.observed = s.observed / 2
	if s.observed < minObservedPeriod {
		s.observed = minObservedPeriod
	}
	if s.observed > observationWindow {
		s.observed = observationWindow
	}
}

// RunOnce 执行一轮续期扫描：用观测周期算候选窗口 → Plan 挑选 → 逐个交给 exec。
// 这是 Plan/PlanningWindow 的**生产调用点**，让「观测 → 周期 → 候选」闭环真正运转。
// 返回本轮交给 exec 的候选数；exec 为 nil 时只统计、不执行。
func (s *RenewScheduler) RunOnce(ctx context.Context, exec RenewExecutor) (int, error) {
	s.runOnceMu.Lock()
	defer s.runOnceMu.Unlock()

	now := s.clock.Now()
	plan, err := s.Plan(ctx, s.PlanningWindow(now), renewBatchLimit)
	if err != nil {
		return 0, err
	}
	for _, item := range plan {
		if exec != nil {
			exec(ctx, item.UserID)
		}
	}
	return len(plan), nil
}

// Run 拉起定时续期循环，返回在 ctx 取消或 Stop() 后关闭的 done channel。
// 每轮只调一次 RunOnce；失败仅记录、不重试刷屏（下个 tick 再试）。
func (s *RenewScheduler) Run(ctx context.Context, interval time.Duration, exec RenewExecutor) <-chan struct{} {
	done := make(chan struct{})
	if interval <= 0 {
		interval = minObservedPeriod
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stopChan:
				return
			case <-ticker.C:
				// 单轮失败不扩散、不重试；等待下一个 tick。
				_, _ = s.RunOnce(ctx, exec)
			}
		}
	}()
	return done
}

// Stop 幂等地停止定时循环。
func (s *RenewScheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stopChan) })
}

// Clock 暴露给上层按调度偏移驱动。
func (s *RenewScheduler) Clock() platform.Clock { return s.clock }
