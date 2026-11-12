// Package pgstore 放业务域的存储实现。
//
// README 架构约束第 4 条：「`internal/<业务域>` 内部自带 handler / service /
// repo **接口**；存储**实现**放 `internal/platform` 或其子包」。
// 本包就是那个「实现放这里」——业务域只定义接口，不持有任何实现。
//
// 命名：本包按存储后端命名（pg 前缀），而不是按域命名。这样将来接 Redis /
// 对象存储的实现也各有其包，业务域始终只见接口。
package pgstore

import (
	"context"
	"sort"
	"sync"
	"time"

	"nongyu-backend-plus/internal/content"
	"nongyu-backend-plus/internal/content/announcement"
)

// AnnouncementMemoryRepo 是 announcement.Repo 的**内存实现**。
//
// 放在这里而不是公告域内，是因为它是一份「存储实现」——哪怕只是内存的。
// 早先把 MemoryRepo 写在 `internal/content/announcement/` 里，直接违反了
// 架构约束第 4 条（同一批的 MemoryObjectStore 却放在 platform，先例自相矛盾），
// 已迁到这里。
//
// 它的用途：
//   - 装配层在没有 DATABASE_URL 时用它，让端点可跑（骨架期与本地开发）；
//   - 业务域的 HTTP 集成测用它，不必起 PG。
//
// 它不是 PG 的等价物：不做事务、不做并发控制之外的持久化。PG 实现待落地，
// 迁移脚本 migrations/0005_content.sql 已就位。
type AnnouncementMemoryRepo struct {
	mu    sync.RWMutex
	rows  map[int64]*announcement.Announcement
	seq   int64
	nowFn func() time.Time
}

// NewAnnouncementMemoryRepo 构造内存仓储。nowFn 可为 nil（默认 time.Now）。
func NewAnnouncementMemoryRepo(nowFn func() time.Time) *AnnouncementMemoryRepo {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &AnnouncementMemoryRepo{rows: map[int64]*announcement.Announcement{}, nowFn: nowFn}
}

// Create 实现 announcement.Repo。
func (r *AnnouncementMemoryRepo) Create(_ context.Context, a *announcement.Announcement) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	a.ID = r.seq
	now := r.nowFn()
	a.CreatedAt, a.UpdatedAt = now, now
	r.rows[a.ID] = cloneAnnouncement(a)
	return nil
}

// Update 实现 announcement.Repo。
func (r *AnnouncementMemoryRepo) Update(_ context.Context, a *announcement.Announcement) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.rows[a.ID]
	if !ok || old.DeletedAt != nil {
		return content.Errorf(content.CodeNotFound, "公告不存在")
	}
	cp := cloneAnnouncement(a)
	cp.CreatedAt = old.CreatedAt
	cp.UpdatedAt = r.nowFn()
	r.rows[a.ID] = cp
	// **回写到入参**：服务层会把 a 直接当响应返回，只更新副本
	// 会让客户端拿到旧 updatedAt（看起来像「保存没生效」）。
	a.CreatedAt, a.UpdatedAt = cp.CreatedAt, cp.UpdatedAt
	return nil
}

// FindByID 实现 announcement.Repo。
func (r *AnnouncementMemoryRepo) FindByID(_ context.Context, id int64) (*announcement.Announcement, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.rows[id]
	if !ok || a.DeletedAt != nil {
		return nil, content.Errorf(content.CodeNotFound, "公告不存在")
	}
	return cloneAnnouncement(a), nil
}

// ListPublished 实现 announcement.Repo。
func (r *AnnouncementMemoryRepo) ListPublished(ctx context.Context, after announcement.PageCursor, limit int) ([]*announcement.Announcement, error) {
	return r.list(ctx, after, limit, true)
}

// ListAll 实现 announcement.Repo。
func (r *AnnouncementMemoryRepo) ListAll(ctx context.Context, after announcement.PageCursor, limit int) ([]*announcement.Announcement, error) {
	return r.list(ctx, after, limit, false)
}

func (r *AnnouncementMemoryRepo) list(_ context.Context, after announcement.PageCursor, limit int, publishedOnly bool) ([]*announcement.Announcement, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*announcement.Announcement, 0, len(r.rows))
	for _, a := range r.rows {
		if a.DeletedAt != nil {
			continue
		}
		if publishedOnly && a.Status != announcement.StatusPublished {
			continue
		}
		if !announcement.AfterCursor(a, after) {
			continue
		}
		all = append(all, cloneAnnouncement(a))
	}
	sort.SliceStable(all, func(i, j int) bool {
		return announcement.LessForOrder(all[i], all[j])
	})
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// MarkDeleted 实现 announcement.Repo。
func (r *AnnouncementMemoryRepo) MarkDeleted(_ context.Context, id int64, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.rows[id]
	if !ok {
		return content.Errorf(content.CodeNotFound, "公告不存在")
	}
	if a.DeletedAt != nil {
		return nil // 幂等
	}
	t := at
	a.DeletedAt = &t
	a.UpdatedAt = at
	return nil
}

// cloneAnnouncement 深拷贝一条公告，避免调用方改到仓储内部状态。
func cloneAnnouncement(a *announcement.Announcement) *announcement.Announcement {
	cp := *a
	if a.PublishedAt != nil {
		t := *a.PublishedAt
		cp.PublishedAt = &t
	}
	if a.EffectiveFrom != nil {
		t := *a.EffectiveFrom
		cp.EffectiveFrom = &t
	}
	if a.ExpiresAt != nil {
		t := *a.ExpiresAt
		cp.ExpiresAt = &t
	}
	if a.DeletedAt != nil {
		t := *a.DeletedAt
		cp.DeletedAt = &t
	}
	if a.Audience.Roles != nil {
		cp.Audience.Roles = append([]int(nil), a.Audience.Roles...)
	}
	return &cp
}

// 编译期确认。
var _ announcement.Repo = (*AnnouncementMemoryRepo)(nil)
