package message

import (
	"context"
	"time"
)

// Repo 是站内消息的持久化接口。
//
// 接口定义在域内、实现放 internal/platform/pgstore（README 架构约束第 4 条）。
// 方法集收窄到本域真正需要的动作；「非本人 → 404」这类归属判定在**服务层**做
// （FindByID 返回完整行、不做 owner 过滤），避免把「谁是收件人」这种领域判断
// 泄进存储层，同时也让 repo 能被只关心存储的实现直接复用。
type Repo interface {
	// Create 插入一条消息（供内部信使用），成功后回填 ID。
	// 通知必须走 UpsertNotification，不能经 Create 建。
	Create(ctx context.Context, m *Message) error

	// UpsertNotification 按通知幂等键 upsert 一条通知。
	//
	// 语义（决策-消息域模型.md Q3 / 决策-通知派生规则.md Q4）：
	//   - 键不存在：插入一行，Hits=1、ReadAt=nil、CreatedAt=at；
	//   - 键已存在：Hits 累加 1、ReadAt 重置为 nil（变回未读）、刷新 Title/Body 快照，
	//     **不产生第二行**（部分唯一索引 (recipientId, kind, sourceDomain, sourceObjectId)）。
	//
	// 实现必须用互斥锁 / 数据库唯一约束保证并发下也是合并而非两行。
	UpsertNotification(ctx context.Context, key NotificationKey, title, body string, at time.Time) (*Message, error)

	// FindByID 按 ID 取一条（含已软删的行，供归属判定用）；不存在返回 apperr.CodeNotFound。
	// 是否属于当前收件人、是否已删由服务层判定。
	FindByID(ctx context.Context, id int64) (*Message, error)

	// List 按「只返回本人 + 可选 kind / 仅未读过滤 + 未软删」返回一页，
	// createdAt 倒序（同刻按 ID 倒序），从 after 之后继续（after 为零值表示从最新开始）。
	List(ctx context.Context, f ListFilter, after PageCursor, limit int) ([]*Message, error)

	// UnreadCount 返回 count(readAt is null and deletedAt is null)，可按 kind 过滤。
	// 口径固定自 决策-消息域模型.md Q4；软删的未读不计入。
	UnreadCount(ctx context.Context, f ListFilter) (int, error)

	// MarkRead 把单条置为已读（写 readAt）；仅作用于未软删的行。
	// 已读 / 已删视为成功（幂等）；不存在返回 apperr.CodeNotFound。
	MarkRead(ctx context.Context, id int64, at time.Time) error

	// MarkAllRead 按当前筛选条件批量置已读，返回本次受影响行数。
	// 只作用于「该收件人 + 未软删 + 未读 + 命中 kind 过滤」的行。
	MarkAllRead(ctx context.Context, f ListFilter, at time.Time) (int64, error)

	// MarkDeleted 软删一条（写 deletedAt）；已删视为成功（幂等）。
	// 不存在返回 apperr.CodeNotFound。
	MarkDeleted(ctx context.Context, id int64, at time.Time) error
}

// ListFilter 是列表 / 未读数 / 全部已读共用的筛选条件。
//
// 「POST /messages/read-all 按当前筛选条件批量置已读」里的「当前筛选条件」
// 就是 GET /messages 的同一组参数（kind + 仅未读），因此三者共用这一个结构体，
// 避免三处各拼一份过滤逻辑导致口径漂移。
type ListFilter struct {
	// RecipientID 必填：所有查询一律限定「只返回本人」，由服务层注入当前会话用户。
	RecipientID int64
	// Kind 为空表示不过滤；非空时只取该形态。
	Kind Kind
	// UnreadOnly 为真只取未读（readAt is null）。对 read-all 而言，置已读的目标
	// 本身就是未读行，这个开关不改变结果集，仅为与列表口径共用而保留。
	UnreadOnly bool
}

// PageCursor 是消息列表的游标。
//
// 排序键是 (createdAt, id) 二元组：createdAt 新的在前，同刻按 ID 大的在前。
// 只带 createdAt 会在「同一毫秒/微秒内插入多行」时跳过或重复行，故带 ID 兜底。
type PageCursor struct {
	CreatedAt time.Time
	ID        int64
}

// Zero 判断游标是否为零值（表示「从最新开始」）。
func (c PageCursor) Zero() bool { return c.ID == 0 && c.CreatedAt.IsZero() }

// LessForOrder 判断 a 是否应排在 b 之前（供排序用）。
//
// 导出是为了让 platform 子包里的实现能复用同一套排序定义——排序是**领域约定**
// （createdAt 倒序、再按 ID 倒序），不该在两个实现里各写一遍。
func LessForOrder(a, b *Message) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID > b.ID
}

// AfterCursor 判断 a 是否严格排在游标之后（用于「从游标继续」）。
//
// 导出理由同 LessForOrder：内存实现与 PG 实现都要用同一套游标语义，
// 否则两边的翻页边界会在细节上分叉。
func AfterCursor(a *Message, c PageCursor) bool {
	if c.Zero() {
		return true
	}
	if !a.CreatedAt.Equal(c.CreatedAt) {
		return a.CreatedAt.Before(c.CreatedAt)
	}
	return a.ID < c.ID
}

// MatchesFilter 判断一条消息是否命中列表/未读数/全部已读的筛选条件。
//
// 导出供 pgstore 实现复用，保证「只返回本人、未软删、kind 过滤、仅未读」
// 这四条口径在内存实现与将来的 PG 实现里完全一致。
func MatchesFilter(m *Message, f ListFilter) bool {
	if m.RecipientID != f.RecipientID {
		return false
	}
	if m.DeletedAt != nil {
		return false
	}
	if f.Kind != "" && m.Kind != f.Kind {
		return false
	}
	if f.UnreadOnly && !m.Unread() {
		return false
	}
	return true
}
