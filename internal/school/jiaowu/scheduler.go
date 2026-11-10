package jiaowu

import (
	"context"
	"hash/fnv"
	"math/rand"
	"sort"
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
	// slotGap 是相邻错峰槽之间的基础延迟。
	//
	// 为什么要有它：16 个槽位如果同一瞬间全发出去，对学校侧仍是一次 16 并发
	// 的尖峰。按槽位拉开间距后，请求在时间轴上摊平，既降 TPS 峰值，也让流量
	// 形态更接近「不同用户各自在不同时间登录」，而不是一批机器同时打。
	slotGap = 2 * time.Second
	// jitterMax 是每个槽位上的额外随机延迟上限。
	//
	// 槽位本身是 userID 的确定性哈希（同一用户永远落同一槽，行为稳定、不像
	// 扫描器）；但若完全确定，同一批用户每轮都会落在完全相同的时间点，长期看
	// 反而是一条可被识别的规律。加一层真随机抖动打破这种周期性。
	jitterMax = 3 * time.Second
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
	// sleep 是可注入的等待函数（测试传 no-op，生产用真实 sleep）。
	// 注入而不是直接 time.Sleep：错峰延迟会有秒级等待，单测不能真等。
	sleep func(ctx context.Context, d time.Duration) bool
	// jitter 返回 [0, jitterMax) 的随机抖动；可注入以便测试确定化。
	jitter func() time.Duration
}

// NewRenewScheduler 构造续期调度器；初始观测周期取观察期上限。
// 不接收 HTTPDoer：出站只能经 Service 的闸口，调度器不持有任何出站能力。
func NewRenewScheduler(tickets platform.TicketStore, clock platform.Clock) *RenewScheduler {
	s := &RenewScheduler{
		tickets:  tickets,
		clock:    clock,
		observed: observationWindow,
		stopChan: make(chan struct{}),
	}
	s.sleep = realSleep
	s.jitter = realJitter
	return s
}

// WithPacing 注入自定义的等待与抖动函数（主要给测试用；传 nil 保持默认）。
func (s *RenewScheduler) WithPacing(sleep func(ctx context.Context, d time.Duration) bool, jitter func() time.Duration) *RenewScheduler {
	if sleep != nil {
		s.sleep = sleep
	}
	if jitter != nil {
		s.jitter = jitter
	}
	return s
}

// realSleep 是可被 ctx 打断的等待；返回 false 表示 ctx 已取消。
func realSleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// realJitter 返回 [0, jitterMax) 内的随机时长。
func realJitter() time.Duration {
	if jitterMax <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(jitterMax)))
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

// RunOnce 执行一轮续期扫描：用观测周期算候选窗口 → Plan 挑选 → 按错峰槽位
// 分批交给 exec。这是 Plan/PlanningWindow 的**生产调用点**，让「观测 → 周期
// → 候选 → 错峰」闭环真正运转。
//
// 错峰怎么落地：Plan 已把每个候选散列到 0..staggerSlots-1 的槽位。这里按槽位
// 分组，**槽位之间**等 slotGap（+ 随机抖动），槽位内连续执行。这样一轮 64 个
// 候选不会在同一瞬间全打出去，峰值被摊平到约 16 个时间点。
//
// 返回本轮交给 exec 的候选数；exec 为 nil 时只统计、不执行（此时也不等待）。
// ctx 取消会中断剩余的等待并返回已处理数。
func (s *RenewScheduler) RunOnce(ctx context.Context, exec RenewExecutor) (int, error) {
	s.runOnceMu.Lock()
	defer s.runOnceMu.Unlock()

	now := s.clock.Now()
	plan, err := s.Plan(ctx, s.PlanningWindow(now), renewBatchLimit)
	if err != nil {
		return 0, err
	}
	if exec == nil {
		return len(plan), nil
	}

	// 按槽位分组（槽位由 Plan 用 userID 哈希算出，同一用户永远同槽）。
	buckets := make(map[int64][]string, staggerSlots)
	for _, item := range plan {
		buckets[item.Offset] = append(buckets[item.Offset], item.UserID)
	}
	// 槽位按序号升序处理，保证同一轮内顺序可预期。
	slots := make([]int64, 0, len(buckets))
	for k := range buckets {
		slots = append(slots, k)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })

	done := 0
	for i, slot := range slots {
		// 第一个槽位不等待；之后每个槽位前等 slotGap + 抖动。
		if i > 0 {
			wait := slotGap
			if s.jitter != nil {
				wait += s.jitter()
			}
			if !s.sleep(ctx, wait) {
				// ctx 已取消：停止本轮剩余候选，不再对外发请求。
				return done, nil
			}
		}
		for _, userID := range buckets[slot] {
			exec(ctx, userID)
			done++
		}
	}
	return done, nil
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
