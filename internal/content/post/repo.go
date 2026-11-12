package post

import (
	"context"
	"time"
)

// Repo 是帖子的持久化接口。
//
// 接口在域内、实现放 internal/platform/pgstore（README 架构约束第 4 条）。
// 归属（谁能改/删）与编辑窗口在**服务层**判定，repo 不做权限过滤——
// 否则「谁是作者」这种领域判断会泄进存储层。
type Repo interface {
	// Create 插入一条帖子并回填 ID。
	Create(ctx context.Context, p *Post) error
	// Update 按 ID 覆盖可写字段（标题/正文/附件/EditedAt/UpdatedAt）。
	Update(ctx context.Context, p *Post) error
	// FindByID 取一条（含已软删，供归属与编辑窗口判定）；不存在返回 not_found。
	FindByID(ctx context.Context, id int64) (*Post, error)
	// List 按 board 过滤（零值表示不过滤）+ publishedAt 倒序 + 游标分页，
	// 只返回未软删的行。读者侧用它。
	List(ctx context.Context, board Board, after PageCursor, limit int) ([]*Post, error)

	// ListAll 是管理端列表：跨板块、与 List 同排序与游标口径。
	//
	// 单列一个方法而不是给 List 传「零值 board 表示不过滤」：读者侧与管理侧
	// 混用一个入口时，很容易出现「管理端误传到零值就变成看全部未删帖」这种
	// 静默越权；两个方法名把意图写进签名里。
	ListAll(ctx context.Context, after PageCursor, limit int) ([]*Post, error)
	// MarkDeleted 软删帖子（幂等）。
	MarkDeleted(ctx context.Context, id int64, at time.Time) error

	// CreateReply 插入一条留言并回填 ID。
	CreateReply(ctx context.Context, r *Reply) error
	// ListReplies 返回某帖下未软删的留言，publishedAt 升序（先来先看）。
	ListReplies(ctx context.Context, postID int64, limit int) ([]*Reply, error)
	// FindReplyByID 取一条留言（含已软删），供靶子校验与归属判定。
	FindReplyByID(ctx context.Context, id int64) (*Reply, error)

	// AddReaction 建立一条点赞/收藏。同键已存在时返回 created=false（幂等 no-op）。
	AddReaction(ctx context.Context, r *Reaction) (created bool, err error)
	// RemoveReaction 删除一条点赞/收藏。不存在时返回 removed=false（幂等）。
	//
	// 参数顺序与 AddReaction 的字段次序保持一致（靶子 → 反应 → 用户），
	// 避免调用方在四个相邻参数间搞错 targetID 与 userID。
	RemoveReaction(ctx context.Context, tt TargetType, targetID int64, rt ReactionKind, userID int64) (removed bool, err error)
	// ReactionCounts 返回某靶子的点赞数、收藏数。
	ReactionCounts(ctx context.Context, tt TargetType, targetID int64) (likes, favorites int, err error)
	// HasReaction 判断某用户是否对某靶子有某反应。参数顺序同 RemoveReaction。
	HasReaction(ctx context.Context, tt TargetType, targetID int64, rt ReactionKind, userID int64) (bool, error)
}

// PageCursor 是帖子列表的游标。
//
// 排序键 (publishedAt, id) 二元组：发布时间倒序，同刻 ID 大的在前。
// 带 ID 兜底，避免同一微秒内多条被跳过或重复。
type PageCursor struct {
	PublishedAt time.Time
	ID          int64
}

// Zero 判断游标是否为零值（从最新开始）。
func (c PageCursor) Zero() bool { return c.ID == 0 && c.PublishedAt.IsZero() }

// LessForOrder 判断 a 是否排在 b 之前（发布时间倒序、同刻 ID 倒序）。
//
// 导出供 pgstore 复用：排序是领域约定，内存实现与将来的 PG 实现
// 必须同一判据，不许各写一遍。
func LessForOrder(a, b *Post) bool {
	if !a.PublishedAt.Equal(b.PublishedAt) {
		return a.PublishedAt.After(b.PublishedAt)
	}
	return a.ID > b.ID
}

// AfterCursor 判断 a 是否严格排在游标之后。
func AfterCursor(a *Post, c PageCursor) bool {
	if c.Zero() {
		return true
	}
	if !a.PublishedAt.Equal(c.PublishedAt) {
		return a.PublishedAt.Before(c.PublishedAt)
	}
	return a.ID < c.ID
}
