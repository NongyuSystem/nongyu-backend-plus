package announcement

import (
	"context"
	"sort"
	"sync"
	"time"

	"nongyu-backend-plus/internal/content"
)

// Repo 是公告的持久化接口。
//
// 接口定义在域内、实现放 platform（或其子包），这是 README 架构约束第 4 条。
// 方法集刻意收窄到本域真正需要的六个——列表的受众过滤在**服务层**做，
// 因为受众判定依赖读者画像（角色/学院/年级），让它进 repo 会把「谁是读者」
// 这种领域判断泄进存储层。
type Repo interface {
	// Create 插入一条公告，成功后回填 ID。
	Create(ctx context.Context, a *Announcement) error
	// Update 按 ID 覆盖可写字段（不存在返回 content.CodeNotFound）。
	Update(ctx context.Context, a *Announcement) error
	// FindByID 按 ID 取一条（含草稿与已撤回；软删的视为不存在）。
	// 是否允许读者看到由服务层判定，repo 不做可见性过滤。
	FindByID(ctx context.Context, id int64) (*Announcement, error)
	// ListPublished 按「置顶优先、再按发布时间倒序、再按 ID 倒序」返回已发布公告，
	// 从 after 之后继续（after 为零值表示从最新开始）。
	// 只返回 status=published 且未软删的行；有效期与受众由服务层过滤。
	ListPublished(ctx context.Context, after PageCursor, limit int) ([]*Announcement, error)
	// ListAll 是管理端列表：含草稿与已撤回，同样的排序与游标语义。
	ListAll(ctx context.Context, after PageCursor, limit int) ([]*Announcement, error)
	// MarkDeleted 软删（写 deleted_at；已删的视为成功，保持幂等）。
	MarkDeleted(ctx context.Context, id int64, at time.Time) error
}

// PageCursor 是公告列表的游标。
//
// 为什么游标要带三个字段：排序键是 (pinned, publishedAt, id) 三元组，
// 只带 publishedAt 会在「置顶公告与普通公告交错」时跳过或重复行。
// Pinned 用「是否置顶」的布尔语义（true 排在前面），比较时按 false < true。
type PageCursor struct {
	Pinned      bool
	PublishedAt time.Time
	ID          int64
}

// Zero 判断游标是否为零值（表示「从最新开始」）。
func (c PageCursor) Zero() bool { return c.ID == 0 && c.PublishedAt.IsZero() }

// lessForOrder 判断 a 是否应排在 b 之前（供 sort.SliceStable 的 less 用）。
//
// 排序定义：pinned 为真在前；同 pinned 下 publishedAt 新的在前；再同则 ID 大的在前。
func lessForOrder(a, b *Announcement) bool {
	if a.Pinned != b.Pinned {
		// 置顶的排前面。这里必须返回 a.Pinned 而不是它的反：
		// 早期版本写成 !a.Pinned，导致「置顶」反而被排到最后（被
		// TestPublicLatest_PinnedWins 抓到）。
		return a.Pinned
	}
	at, bt := publishedOrZero(a), publishedOrZero(b)
	if !at.Equal(bt) {
		return at.After(bt)
	}
	return a.ID > b.ID
}

func publishedOrZero(a *Announcement) time.Time {
	if a.PublishedAt == nil {
		return time.Time{}
	}
	return *a.PublishedAt
}

// afterCursor 判断 a 是否严格排在游标之后（用于「从游标继续」）。
func afterCursor(a *Announcement, c PageCursor) bool {
	if c.Zero() {
		return true
	}
	// 置顶为 true 的排在 false 之前；游标 pinned=true 时，false 的都在后面。
	if a.Pinned != c.Pinned {
		return !a.Pinned && c.Pinned
	}
	at := publishedOrZero(a)
	if !at.Equal(c.PublishedAt) {
		return at.Before(c.PublishedAt)
	}
	return a.ID < c.ID
}

// MemoryRepo 是 Repo 的内存实现，供测试与骨架期使用。
type MemoryRepo struct {
	mu    sync.RWMutex
	rows  map[int64]*Announcement
	seq   int64
	nowFn func() time.Time
}

// NewMemoryRepo 构造内存仓储。nowFn 可为 nil（默认 time.Now）。
func NewMemoryRepo(nowFn func() time.Time) *MemoryRepo {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &MemoryRepo{rows: map[int64]*Announcement{}, nowFn: nowFn}
}

// Create 实现 Repo。
func (r *MemoryRepo) Create(_ context.Context, a *Announcement) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	a.ID = r.seq
	now := r.nowFn()
	a.CreatedAt, a.UpdatedAt = now, now
	r.rows[a.ID] = cloneAnnouncement(a)
	return nil
}

// Update 实现 Repo。
func (r *MemoryRepo) Update(_ context.Context, a *Announcement) error {
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
	return nil
}

// FindByID 实现 Repo。
func (r *MemoryRepo) FindByID(_ context.Context, id int64) (*Announcement, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.rows[id]
	if !ok || a.DeletedAt != nil {
		return nil, content.Errorf(content.CodeNotFound, "公告不存在")
	}
	return cloneAnnouncement(a), nil
}

// ListPublished 实现 Repo。
func (r *MemoryRepo) ListPublished(ctx context.Context, after PageCursor, limit int) ([]*Announcement, error) {
	return r.list(ctx, after, limit, true)
}

// ListAll 实现 Repo。
func (r *MemoryRepo) ListAll(ctx context.Context, after PageCursor, limit int) ([]*Announcement, error) {
	return r.list(ctx, after, limit, false)
}

func (r *MemoryRepo) list(_ context.Context, after PageCursor, limit int, publishedOnly bool) ([]*Announcement, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*Announcement, 0, len(r.rows))
	for _, a := range r.rows {
		if a.DeletedAt != nil {
			continue
		}
		if publishedOnly && a.Status != StatusPublished {
			continue
		}
		if !afterCursor(a, after) {
			continue
		}
		all = append(all, cloneAnnouncement(a))
	}
	sort.SliceStable(all, func(i, j int) bool { return lessForOrder(all[i], all[j]) })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// MarkDeleted 实现 Repo。
func (r *MemoryRepo) MarkDeleted(_ context.Context, id int64, at time.Time) error {
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
func cloneAnnouncement(a *Announcement) *Announcement {
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
var _ Repo = (*MemoryRepo)(nil)
