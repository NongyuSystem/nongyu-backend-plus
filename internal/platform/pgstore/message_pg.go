package pgstore

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/message"
)

// MessagePGRepo 是 message.Repo 的 **PostgreSQL 实现**。
//
// 与 MessageMemoryRepo 是同一个领域接口的两个实现。内存实现用一个 map 模拟了
// 「通知的部分唯一索引」，本实现直接落到 migrations/0006_message_track.sql 的：
//
//	CREATE UNIQUE INDEX uq_messages_notification_source
//	    ON messages (recipient_id, kind, source_domain, source_object_id)
//	    WHERE kind = 'notification';
//
// 关键差别在于**并发正确性的来源**：内存实现靠一把写锁把「查键 → 合并/插入」
// 串行化；这里靠数据库的唯一索引 + ON CONFLICT DO UPDATE。两者在并发下都只
// 产生一行，但后者天然跨进程（多个服务实例同时 Emit 同键也只会合并）。
//
// 列表/未读数/批量已读的过滤口径**不在这里各写一份**：接口已导出
// message.MatchesFilter 供实现复用，本文件把它的四个条件逐条翻成 SQL 谓词，
// 并在每个谓词旁注明对应的 Go 判断（见 filterClause）。
type MessagePGRepo struct {
	pool  *pgxpool.Pool
	nowFn func() time.Time
}

// NewMessagePGRepo 构造 PG 消息仓储。
//
// pool 不得为 nil；now 可为 nil，默认 time.Now。now 只用于 Create 时补
// createdAt 的零值（与内存实现同义），通知的 createdAt 由调用方传入的 at 决定。
func NewMessagePGRepo(pool *pgxpool.Pool, now func() time.Time) *MessagePGRepo {
	if now == nil {
		now = time.Now
	}
	return &MessagePGRepo{pool: pool, nowFn: now}
}

// messageColumns 是读路径列清单，顺序与 scanMessage 一一对应。
//
// 不含 SELECT *：迁移只增不改，加列时 SELECT * 会让 Scan 参数个数静默错位。
const messageColumns = `id, recipient_id, kind, source_domain, source_object_id,
	        title, body, hits, sender_id, read_at, deleted_at, created_at`

// Create 实现 message.Repo：插入一条消息并回填 ID。
//
// 只供内部信使用；通知必须走 UpsertNotification（否则会绕开唯一键约束，
// 同键插两次直接撞 23505）。此处与内存实现保持同构：
//   - createdAt 为零值时才用注入时钟补；
//   - hits <= 0 时归一成 1（列是 NOT NULL DEFAULT 1，但显式写更可控）。
func (r *MessagePGRepo) Create(ctx context.Context, m *message.Message) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = r.nowFn()
	}
	if m.Hits <= 0 {
		m.Hits = 1
	}
	return r.pool.QueryRow(ctx, `
		INSERT INTO messages (
			recipient_id, kind, source_domain, source_object_id,
			title, body, hits, sender_id, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id, created_at`,
		m.RecipientID, string(m.Kind), m.SourceDomain, m.SourceObjectID,
		m.Title, m.Body, m.Hits, nullableInt64(m.SenderID), m.CreatedAt,
	).Scan(&m.ID, &m.CreatedAt)
}

// UpsertNotification 实现 message.Repo：按通知幂等键 upsert 一条通知。
//
// **这是消息域最核心的一条 SQL**，语义逐条对应决策-消息域模型.md Q3：
//
//   - 键不存在 → 插入一行：hits=1、read_at=NULL、created_at=at；
//   - 键已存在 → hits 累加 1、read_at 重置为 NULL（变回未读）、
//     title/body 刷新为最新快照、**created_at 保持首次写入**。
//
// 为什么 DO UPDATE 里刻意不写 created_at：列表按 created_at 倒序，
// 合并不该把一条老通知顶到最前。内存实现的注释里已把这条固定为决策，
// 这里只做等价翻译——**改它等于改领域语义**。
//
// 为什么不写 deleted_at：软删的行仍留在唯一索引里，同键再次触发会合并回同一行
// 而 deleted_at 保持非空 → 对用户仍不可见、不计未读。这与内存实现
// 「已软删的行仍留在 notifIdx 里」是同一取向（保守：删了就不再被同一来源打扰）。
//
// ON CONFLICT 的目标里必须重复部分索引的谓词 `WHERE kind = 'notification'`，
// 否则 Postgres 无法推断出 uq_messages_notification_source（报 42P10）。
func (r *MessagePGRepo) UpsertNotification(ctx context.Context, key message.NotificationKey, title, body string, at time.Time) (*message.Message, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO messages (recipient_id, kind, source_domain, source_object_id, title, body, hits, created_at)
		VALUES ($1, 'notification', $2, $3, $4, $5, 1, $6)
		ON CONFLICT (recipient_id, kind, source_domain, source_object_id) WHERE kind = 'notification'
		DO UPDATE SET
			hits = messages.hits + 1,
			read_at = NULL,
			title = EXCLUDED.title,
			body = EXCLUDED.body
		RETURNING `+messageColumns,
		key.RecipientID, key.SourceDomain, key.SourceObjectID, title, body, at)
	return scanMessage(row)
}

// FindByID 实现 message.Repo。
//
// **不过滤 deleted_at**：归属判定（非本人 → 404）与「已删视为不存在」都在服务层
// 做，repo 交回完整行。这与内存实现一致，也是把「谁是收件人」留在领域层的做法。
func (r *MessagePGRepo) FindByID(ctx context.Context, id int64) (*message.Message, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+messageColumns+` FROM messages WHERE id = $1`, id)
	m, err := scanMessage(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Errorf(apperr.CodeNotFound, "消息不存在")
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// List 实现 message.Repo。
//
// 过滤条件、排序、游标全部对齐 message.MatchesFilter / LessForOrder / AfterCursor：
//
//	MatchesFilter  ⇔  recipient_id=$n AND deleted_at IS NULL [AND kind=$n] [AND read_at IS NULL]
//	LessForOrder   ⇔  ORDER BY created_at DESC, id DESC
//	AfterCursor    ⇔  (created_at, id) < ($t, $i)
//
// 行值比较 (created_at, id) < ($t, $i) 与 AfterCursor 的分支判断等价：
// 时间不等时比时间（更早的排在后面即「之后」），相等时比 id（更小的在之后）。
func (r *MessagePGRepo) List(ctx context.Context, f message.ListFilter, after message.PageCursor, limit int) ([]*message.Message, error) {
	b := &pgArgs{}
	sql := `SELECT ` + messageColumns + ` FROM messages WHERE ` + b.filterClause(f, true)
	if !after.Zero() {
		sql += ` AND (created_at, id) < (` + b.add(after.CreatedAt) + `, ` + b.add(after.ID) + `)`
	}
	sql += ` ORDER BY created_at DESC, id DESC`
	if limit > 0 {
		sql += ` LIMIT ` + b.add(limit)
	}

	rows, err := r.pool.Query(ctx, sql, b.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*message.Message, 0, 16)
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UnreadCount 实现 message.Repo：count(read_at is null and deleted_at is null)。
//
// 与内存实现一致：UnreadOnly 开关**不参与**计数（计数本身就只数未读行），
// 但 kind 过滤仍生效——这正是三者共用同一个 ListFilter 的意义。
func (r *MessagePGRepo) UnreadCount(ctx context.Context, f message.ListFilter) (int, error) {
	b := &pgArgs{}
	where := `recipient_id = ` + b.add(f.RecipientID) +
		` AND deleted_at IS NULL AND read_at IS NULL`
	if f.Kind != "" {
		where += ` AND kind = ` + b.add(string(f.Kind))
	}
	var n int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE `+where, b.args...).Scan(&n)
	return n, err
}

// MarkRead 实现 message.Repo：单条置已读。
//
// 幂等语义（内存实现）：
//   - 行不存在 → NotFound；
//   - 已软删 或 已读 → 返回 nil，且**不覆盖**已有的 read_at。
//
// 故 WHERE 带上 `read_at IS NULL AND deleted_at IS NULL`，0 行受影响时
// 再查一次存在性来区分「不存在」与「幂等命中」。
func (r *MessagePGRepo) MarkRead(ctx context.Context, id int64, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE messages SET read_at = $2
		 WHERE id = $1 AND read_at IS NULL AND deleted_at IS NULL`, id, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	if err := r.assertExists(ctx, id); err != nil {
		return err
	}
	return nil
}

// MarkAllRead 实现 message.Repo：按筛选条件批量置已读，返回受影响行数。
//
// 只作用于「该收件人 + 未软删 + 未读 + 命中 kind」的行；UnreadOnly 开关不改变
// 结果集（目标本来就是未读行），仅为与列表/未读数共用 ListFilter。
func (r *MessagePGRepo) MarkAllRead(ctx context.Context, f message.ListFilter, at time.Time) (int64, error) {
	b := &pgArgs{}
	// unreadOnly 传 false：目标本身限定 read_at IS NULL，让开关不参与是刻意的
	// （传 true 会多一个恒真谓词，语义不变但更难看出「开关在此不生效」）。
	where := b.filterClause(f, false) + ` AND read_at IS NULL`
	setAt := b.add(at)
	tag, err := r.pool.Exec(ctx,
		`UPDATE messages SET read_at = `+setAt+` WHERE `+where, b.args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// MarkDeleted 实现 message.Repo：软删一条（幂等）。
//
// 与内存实现一致：已删的再删一次返回 nil 且不改 deleted_at。
func (r *MessagePGRepo) MarkDeleted(ctx context.Context, id int64, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE messages SET deleted_at = $2 WHERE id = $1 AND deleted_at IS NULL`, id, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	return r.assertExists(ctx, id)
}

// filterClause 把 message.MatchesFilter 翻成 SQL 谓词（参数逐个 $n 占位）。
//
// 对应关系一眼可见：
//
//	if m.RecipientID != f.RecipientID → recipient_id = $n
//	if m.DeletedAt != nil             → deleted_at IS NULL
//	if f.Kind != "" && m.Kind != f.Kind → kind = $n
//	if f.UnreadOnly && !m.Unread()    → read_at IS NULL
//
// withUnread 控制是否翻「仅未读」那一条：MarkAllRead 的目标集本身就只含未读行，
// 传 false 让 SQL 更短更好读（语义与传 true 结果相同）。
func (b *pgArgs) filterClause(f message.ListFilter, withUnread bool) string {
	out := `recipient_id = ` + b.add(f.RecipientID)
	out += ` AND deleted_at IS NULL`
	if f.Kind != "" {
		out += ` AND kind = ` + b.add(string(f.Kind))
	}
	if withUnread && f.UnreadOnly {
		out += ` AND read_at IS NULL`
	}
	return out
}

// assertExists 判断一行是否存在（**含已软删**），不存在则返回 NotFound。
//
// 仅供「更新 0 行受影响」后区分「幂等命中」与「真的没有这一行」，
// 于是错误码与内存实现完全一致。
func (r *MessagePGRepo) assertExists(ctx context.Context, id int64) error {
	var one int
	err := r.pool.QueryRow(ctx, `SELECT 1 FROM messages WHERE id = $1`, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.Errorf(apperr.CodeNotFound, "消息不存在")
	}
	return err
}

// scanMessage 把一行扫成领域结构体。
//
// 可空列（sender_id / read_at / deleted_at）经 pgtype 中转，避免依赖 pgx 对
// **T 的指针包装行为；sender_id 在领域里是「0 表示无发送者」（通知恒为 0），
// 故 NULL 与 0 在此归一。
func scanMessage(row rowScanner) (*message.Message, error) {
	var (
		m        message.Message
		kind     string
		senderID pgtype.Int8
		readAt   pgtype.Timestamptz
		deleted  pgtype.Timestamptz
	)
	err := row.Scan(
		&m.ID, &m.RecipientID, &kind, &m.SourceDomain, &m.SourceObjectID,
		&m.Title, &m.Body, &m.Hits, &senderID, &readAt, &deleted, &m.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	m.Kind = message.Kind(kind)
	if senderID.Valid {
		m.SenderID = senderID.Int64
	}
	m.ReadAt = pgTimePtr(readAt)
	m.DeletedAt = pgTimePtr(deleted)
	return &m, nil
}

// nullableInt64 把「0 表示无」的 int64 映射成 SQL NULL（sender_id 列）。
//
// 通知的 sender_id 必须落 NULL 而不是 0：列上没有 CHECK 兜底，
// 一旦写成 0，「有没有发送者」这件事就再也分不清了。
func nullableInt64(v int64) pgtype.Int8 {
	if v == 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: v, Valid: true}
}

// 编译期确认。
var _ message.Repo = (*MessagePGRepo)(nil)
