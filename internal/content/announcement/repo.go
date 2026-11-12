package announcement

import (
	"context"
	"time"
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

// LessForOrder 判断 a 是否应排在 b 之前（供排序用）。
//
// 导出是为了让 platform 子包里的实现能复用同一套排序定义——排序是**领域约定**
// （置顶优先、再按发布时间倒序、再按 ID 倒序），不该在两个实现里各写一遍。
//
// 排序定义：pinned 为真在前；同 pinned 下 publishedAt 新的在前；再同则 ID 大的在前。
func LessForOrder(a, b *Announcement) bool {
	if a.Pinned != b.Pinned {
		return a.Pinned
	}
	at, bt := PublishedOrZero(a), PublishedOrZero(b)
	if !at.Equal(bt) {
		return at.After(bt)
	}
	return a.ID > b.ID
}

// PublishedOrZero 取发布时间，草稿返回零值时间。
func PublishedOrZero(a *Announcement) time.Time {
	if a.PublishedAt == nil {
		return time.Time{}
	}
	return *a.PublishedAt
}

// AfterCursor 判断 a 是否严格排在游标之后（用于「从游标继续」）。
//
// 导出理由同 LessForOrder：内存实现与 PG 实现都要用同一套游标语义，
// 否则两边的翻页边界会在细节上分叉。
func AfterCursor(a *Announcement, c PageCursor) bool {
	if c.Zero() {
		return true
	}
	// 置顶为 true 的排在 false 之前；游标 pinned=true 时，false 的都在后面。
	if a.Pinned != c.Pinned {
		return !a.Pinned && c.Pinned
	}
	at := PublishedOrZero(a)
	if !at.Equal(c.PublishedAt) {
		return at.Before(c.PublishedAt)
	}
	return a.ID < c.ID
}
