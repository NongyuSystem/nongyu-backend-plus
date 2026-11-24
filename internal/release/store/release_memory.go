package store

import (
	"context"
	"sync"
	"time"

	"nongyu-backend-plus/internal/release"
)

// ReleaseMemoryRepo 是 release.Repo 的内存实现。
//
// 用途：装配层在没有 DATABASE_URL 时用它（本地开发/骨架期），以及域的
// HTTP 集成测用它——不必起 PG。
//
// 它不是 PG 的等价物：不做持久化；但「同一 (appId, versionCode) 不可重复」
// 与「最新 = versionCode 最大」两条语义用互斥锁严格执行，避免内存与 PG
// 行为分叉导致测试假绿。
type ReleaseMemoryRepo struct {
	mu       sync.Mutex
	rows     map[string]*release.Release // 键：appId + "/" + versionCode
	seq      int64
	nowFn    func() time.Time
}

// NewReleaseMemoryRepo 构造内存仓储。nowFn 可为 nil（默认 time.Now）。
func NewReleaseMemoryRepo(nowFn func() time.Time) *ReleaseMemoryRepo {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &ReleaseMemoryRepo{rows: map[string]*release.Release{}, nowFn: nowFn}
}

func refKey(appID string, versionCode int64) string {
	return appID + "/" + itoa(versionCode)
}

// itoa 避免为一处拼接引入 strconv（保持 store 包依赖最小）。
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Create 实现 release.Repo。
func (r *ReleaseMemoryRepo) Create(_ context.Context, rec *release.Release) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := refKey(rec.AppID, rec.VersionCode)
	if _, exists := r.rows[key]; exists {
		return release.ErrReleaseExists
	}
	r.seq++
	cp := *rec
	cp.ID = r.seq
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = r.nowFn()
	}
	r.rows[key] = &cp
	// 回填给调用方（与 PG 的 RETURNING 同语义）。
	rec.ID = cp.ID
	rec.CreatedAt = cp.CreatedAt
	return nil
}

// GetByRef 实现 release.Repo。返回副本，避免调用方改写仓储内部状态。
func (r *ReleaseMemoryRepo) GetByRef(_ context.Context, appID string, versionCode int64) (*release.Release, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.rows[refKey(appID, versionCode)]
	if !ok {
		return nil, release.ErrReleaseNotFound
	}
	cp := *rec
	return &cp, nil
}

// Latest 实现 release.Repo：返回 versionCode 最大的那条。
func (r *ReleaseMemoryRepo) Latest(_ context.Context, appID string) (*release.Release, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var best *release.Release
	for _, rec := range r.rows {
		if rec.AppID != appID {
			continue
		}
		if best == nil || rec.VersionCode > best.VersionCode {
			cp := *rec
			best = &cp
		}
	}
	if best == nil {
		return nil, release.ErrNoRelease
	}
	return best, nil
}
