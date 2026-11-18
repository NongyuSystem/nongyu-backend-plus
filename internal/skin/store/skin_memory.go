package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"nongyu-backend-plus/internal/skin"
)

// SkinMemoryRepo 是 skin.Repo 的内存实现。
//
// 放 platform 子包而非业务域内（README 架构约束第 4 条），与
// AnnouncementMemoryRepo / PostMemoryRepo 同构。用途：
//   - 装配层在没有 DATABASE_URL 时用它，让端点可跑（骨架期与本地开发）；
//   - 皮肤域 HTTP 集成测用它，不必起 PG。
//
// 它不是 PG 的等价物：不做持久化；但「版本不可变」「全局单指针」两条
// 语义用互斥锁严格复现，避免内存与 PG 行为分叉导致测试假绿。
type SkinMemoryRepo struct {
	mu      sync.Mutex
	packs   map[string]*skin.Pack // 键：packId/version
	seq     int64
	current *skin.Current
	nowFn   func() time.Time
}

// NewSkinMemoryRepo 构造内存仓储。nowFn 可为 nil（默认 time.Now）。
func NewSkinMemoryRepo(nowFn func() time.Time) *SkinMemoryRepo {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &SkinMemoryRepo{packs: map[string]*skin.Pack{}, nowFn: nowFn}
}

func refKey(packID, version string) string { return packID + "/" + version }

// Create 实现 skin.Repo。
func (r *SkinMemoryRepo) Create(_ context.Context, p *skin.Pack) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := refKey(p.PackID, p.Version)
	if _, exists := r.packs[key]; exists {
		return skin.ErrVersionExists
	}
	r.seq++
	cp := *p
	cp.ID = r.seq
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = r.nowFn()
	}
	r.packs[key] = &cp
	// 回填给调用方（与 PG 的 RETURNING 同语义）。
	p.ID = cp.ID
	p.CreatedAt = cp.CreatedAt
	return nil
}

// GetByRef 实现 skin.Repo。返回副本，避免调用方改写仓储内部状态。
func (r *SkinMemoryRepo) GetByRef(_ context.Context, packID, version string) (*skin.Pack, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.packs[refKey(packID, version)]
	if !ok {
		return nil, skin.ErrVersionNotFound
	}
	cp := *p
	return &cp, nil
}

// List 实现 skin.Repo（按创建时间倒序，新的在前）。
func (r *SkinMemoryRepo) List(_ context.Context, limit int) ([]*skin.Pack, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	all := make([]*skin.Pack, 0, len(r.packs))
	for _, p := range r.packs {
		cp := *p
		all = append(all, &cp)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ID > all[j].ID
		}
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// Current 实现 skin.Repo。
func (r *SkinMemoryRepo) Current(_ context.Context) (*skin.Current, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil {
		return nil, skin.ErrNoCurrentPointer
	}
	cp := *r.current
	return &cp, nil
}

// Activate 实现 skin.Repo。指针必须指向已登记的包（与 PG 外键同语义）。
func (r *SkinMemoryRepo) Activate(_ context.Context, packID, version string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.packs[refKey(packID, version)]
	if !ok {
		return skin.ErrVersionNotFound
	}
	now := r.nowFn()
	cp := &skin.Current{
		PackID: p.PackID, Version: p.Version,
		SchemaVersion: p.SchemaVersion, MinAppVersion: p.MinAppVersion,
		DisplayName: p.DisplayName, ObjectKey: p.ObjectKey,
		PackSHA256: p.PackSHA256, PackSize: p.PackSize,
		ManifestJSON: p.ManifestJSON, UpdatedAt: now,
	}
	r.current = cp
	return nil
}

// 编译期确认满足接缝。
var _ skin.Repo = (*SkinMemoryRepo)(nil)
