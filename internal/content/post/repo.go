package post

import (
	"context"
	"time"
)

// Repo 是帖子的持久化接口。
//
// 接口在域内、实现放本域 store/ 子包（README 架构约束第 4 条）。
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

	// ListByAuthor 返回某作者自己的帖子（「我的帖子」），与 List 同排序、
	// 同游标、同「只返回未软删」口径；board 为空表示跨板块。
	//
	// 为什么单列方法而不是给 List 加 authorID 参数（传 0 表示不按作者过滤）：
	// 参数化之后，「传 0 意外看到全站帖子」就成了一次静默越权——而 mine 是
	// 用户可见的隐私边界。方法名把「这是谁的列表」写进签名，调用方不可能
	// 因为漏传参数而越界。
	ListByAuthor(ctx context.Context, authorID int64, board Board, after PageCursor, limit int) ([]*Post, error)
	// MarkDeleted 软删帖子（幂等）。
	MarkDeleted(ctx context.Context, id int64, at time.Time) error

	// ListByDetail 按 detail 里的字段精确匹配（供「查某老师的评价」这类专用接口）。
	//
	//   - board 非空时限定类型（评价查询会传 teacher_review / course_review）；
	//   - match 是「字段名 → 期望值」，全部条件都要满足（AND）；
	//   - 值按**字符串精确匹配**（大小写不敏感），不做模糊。
	//
	// 为什么单列一个方法而不是给 List 加参数：detail 的键来自规则表，
	// 是**数据**而不是编译期已知的字段名。用 map 传条件让新增筛选项
	// 不需要改接口；但「哪些键允许用来过滤」由服务层白名单控制，
	// 否则调用方可以拿 detail 里任意键做筛选（等于把内部结构暴露成 API）。
	ListByDetail(ctx context.Context, board Board, match map[string]string, after PageCursor, limit int) ([]*Post, error)

	// CreateReply 插入一条留言并回填 ID。
	CreateReply(ctx context.Context, r *Reply) error
	// ListReplies 返回某帖下未软删的留言，publishedAt 升序（先来先看）。
	ListReplies(ctx context.Context, postID int64, limit int) ([]*Reply, error)
	// FindReplyByID 取一条留言（含已软删），供靶子校验与归属判定。
	FindReplyByID(ctx context.Context, id int64) (*Reply, error)
	// MarkReplyDeleted 软删一条留言（幂等）。
	//
	// 只改 deleted_at，不删行：点赞等反应记录以 target_id 引用它，
	// 硬删会留下悬空引用。读路径（ListReplies / 点赞校验）已经会跳过已删行。
	MarkReplyDeleted(ctx context.Context, id int64, at time.Time) error
	// MarkRepliesDeletedByPost 把某帖下**全部未删留言**软删（管理端删帖时级联）。
	//
	// 为什么不用逐条 FindReplyByID + MarkReplyDeleted：
	// 那是 N+1 次往返，且中间失败会留下「帖子删了、评论还在」的半状态。
	// 一条 UPDATE 是原子的。
	MarkRepliesDeletedByPost(ctx context.Context, postID int64, at time.Time) error

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
// 导出供 store 实现复用：排序是领域约定，内存实现与将来的 PG 实现
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
