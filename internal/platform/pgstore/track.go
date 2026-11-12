package pgstore

import (
	"context"
	"sync"
	"time"

	"nongyu-backend-plus/internal/track"
)

// TrackMemoryRepo 是 track.Repo 的内存实现。
//
// 放 platform 子包而非业务域内（README 架构约束第 4 条），与其它域同构。
//
// 两点值得注意：
//   - **eventId 幂等**：重复 id 不算失败，只计入 duplicated；
//   - **并发生成时安全**：缓冲的后台协程是单个，但 Summary 可能并发读，
//     所以读写都要锁。
type TrackMemoryRepo struct {
	mu     sync.Mutex
	events map[string]track.Event // key = eventID
	// order 保留写入顺序（便于排查；PG 实现里对应 created_at）。
	order []string
}

// NewTrackMemoryRepo 构造内存仓储。
func NewTrackMemoryRepo() *TrackMemoryRepo {
	return &TrackMemoryRepo{events: map[string]track.Event{}}
}

// BatchInsert 实现 track.Repo（按 eventID 幂等）。
func (r *TrackMemoryRepo) BatchInsert(_ context.Context, events []track.Event) (int, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var inserted, duplicated int
	for _, ev := range events {
		if _, exists := r.events[ev.EventID]; exists {
			duplicated++
			continue
		}
		r.events[ev.EventID] = ev
		r.order = append(r.order, ev.EventID)
		inserted++
	}
	return inserted, duplicated, nil
}

// CountByType 实现 track.Repo。
func (r *TrackMemoryRepo) CountByType(_ context.Context, from, to time.Time) (map[track.EventType]int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := map[track.EventType]int{}
	for _, ev := range r.events {
		if ev.ReceivedAt.Before(from) || ev.ReceivedAt.After(to) {
			continue
		}
		out[ev.EventType]++
	}
	return out, nil
}

// Count 返回总条数（测试与观测用）。
func (r *TrackMemoryRepo) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// Events 返回全部事件的快照（测试用）。
func (r *TrackMemoryRepo) Events() []track.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]track.Event, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.events[id])
	}
	return out
}

// 编译期确认。
var _ track.Repo = (*TrackMemoryRepo)(nil)
