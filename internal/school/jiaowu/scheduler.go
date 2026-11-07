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
// 本调度器只做「挑候选 + 错峰 + 记录观测」，不主动向学校发换票请求（避免静默重试
// 刷屏触发风控）；真正执行换票由上层在合适时机、经 Service 的闸口完成。

const (
	// observationWindow 是观察期初始上限：9 天（10 天长期凭据窗口留 1 天余量）。
	observationWindow = 9 * 24 * time.Hour
	// staggerSlots 是把候选散列到的错峰槽位数。
	staggerSlots = 16
)

// RenewItem 是续期计划里的一项：用户 + 相对调度偏移。
type RenewItem struct {
	UserID string
	Offset int64
}

// RenewScheduler 从外置存储挑「即将过期」的候选、按用户散列错峰，并维护观测周期。
type RenewScheduler struct {
	mu       sync.Mutex
	tickets  platform.TicketStore
	clock    platform.Clock
	doer     platform.HTTPDoer
	observed time.Duration
	lastOK   time.Time
	lastFail time.Time
}

// NewRenewScheduler 构造续期调度器；初始观测周期取观察期上限。
func NewRenewScheduler(tickets platform.TicketStore, clock platform.Clock, doer platform.HTTPDoer) *RenewScheduler {
	return &RenewScheduler{
		tickets:  tickets,
		clock:    clock,
		doer:     doer,
		observed: observationWindow,
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
	if s.observed > observationWindow {
		s.observed = observationWindow
	}
	s.lastOK = at
}

// RecordFailure 记录首次失败时刻；此后周期保守收紧（至少缩短一半、且不超过
// 观察期初始上限），避免在同一失效点反复尝试。
func (s *RenewScheduler) RecordFailure(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastFail = at
	s.observed = s.observed / 2
	if s.observed <= 0 {
		s.observed = time.Hour
	}
	if s.observed > observationWindow {
		s.observed = observationWindow
	}
}

// Clock 暴露给上层按调度偏移驱动（本调度器不自行起 goroutine/ticker）。
func (s *RenewScheduler) Clock() platform.Clock { return s.clock }
