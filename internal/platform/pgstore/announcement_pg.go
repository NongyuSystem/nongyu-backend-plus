package pgstore

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/content"
	"nongyu-backend-plus/internal/content/announcement"
)

// AnnouncementPGRepo 是 announcement.Repo 的 **PostgreSQL 实现**。
//
// 与 AnnouncementMemoryRepo 是同一个领域接口的两个实现，**行为必须逐条对齐**
// （软删语义、游标语义、排序定义、幂等性）。因此本文件刻意不自己定义排序：
// 排序键 (pinned DESC, publishedAt DESC, id DESC) 是领域约定，SQL 里的
// ORDER BY 与 announcement.LessForOrder 一一对应（见下方对照注释）。
//
// 表结构见 migrations/0005_content.sql。三条与实现强相关的库级事实：
//   - status 有 CHECK 闭集；published 必须有 published_at（CHECK）；
//   - audience_roles 是 INTEGER[]（不是逗号串），受众三件有自洽性 CHECK；
//   - 软删走 deleted_at（NULL=未删），从不物理删除。
//
// 与内存实现的一处**实现性差异**（行为等价，已记入回报「存疑点」）：
// Update 不把新的 updatedAt 写回入参结构体——内存实现同样只在副本上盖时间戳，
// 故此处保持同构，避免「两个实现返回的 updatedAt 新旧不一致」。
type AnnouncementPGRepo struct {
	pool  *pgxpool.Pool
	nowFn func() time.Time
}

// NewAnnouncementPGRepo 构造 PG 公告仓储。
//
// pool 不得为 nil（装配层只在配了 DATABASE_URL 时构造本类型）；
// now 可为 nil，默认 time.Now。保留注入时钟的理由与内存实现一致：
// 测试要用固定时刻断言 created_at / updated_at。
func NewAnnouncementPGRepo(pool *pgxpool.Pool, now func() time.Time) *AnnouncementPGRepo {
	if now == nil {
		now = time.Now
	}
	return &AnnouncementPGRepo{pool: pool, nowFn: now}
}

// announcementColumns 是读路径的列清单；顺序与 scanAnnouncement 一一对应。
//
// 列清单写死而不是 SELECT *：迁移只增不改，若将来加列，SELECT * 会让
// Scan 的参数个数与新列错位，报错点在运行时且难定位。
const announcementColumns = `id, title, body, status, published_at, effective_from, expires_at,
	        pinned, publish_revision, audience_scope, audience_roles,
	        audience_college, audience_grade, created_at, updated_at, deleted_at`

// Create 实现 announcement.Repo。
//
// 与内存实现同构：ID 由库生成（BIGSERIAL），createdAt/updatedAt 一律取注入时钟，
// **忽略入参里的时间**（内存实现也是这样覆盖的）。
func (r *AnnouncementPGRepo) Create(ctx context.Context, a *announcement.Announcement) error {
	now := r.nowFn()
	return r.pool.QueryRow(ctx, `
		INSERT INTO announcements (
			title, body, status, published_at, effective_from, expires_at,
			pinned, publish_revision, audience_scope, audience_roles,
			audience_college, audience_grade, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)
		RETURNING id, created_at, updated_at`,
		a.Title, a.Body, string(a.Status),
		timePtrToPG(a.PublishedAt), timePtrToPG(a.EffectiveFrom), timePtrToPG(a.ExpiresAt),
		a.Pinned, a.PublishRevision, string(a.Audience.Scope), intRoles(a.Audience.Roles),
		a.Audience.College, a.Audience.Grade, now,
	).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
}

// Update 实现 announcement.Repo：按 ID 覆盖可写字段。
//
// 语义与内存实现逐条对齐：
//   - 不存在 **或已软删** 都返回 content.CodeNotFound（两者刻意不区分）；
//   - 可写字段是「整行覆盖」（含 status / publishedAt / publishRevision），
//     因为服务层的 Publish/Revoke 就是先 FindByID 改字段再整行写回；
//   - createdAt 保持首次写入不变；updatedAt 取注入时钟。
//
// 刻意**不写 deleted_at**：Update 只在未软删的行上生效（WHERE deleted_at IS NULL），
// 复活一条软删公告不是本接口的能力。
func (r *AnnouncementPGRepo) Update(ctx context.Context, a *announcement.Announcement) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE announcements SET
			title = $2, body = $3, status = $4, published_at = $5,
			effective_from = $6, expires_at = $7, pinned = $8,
			publish_revision = $9, audience_scope = $10, audience_roles = $11,
			audience_college = $12, audience_grade = $13, updated_at = $14
		 WHERE id = $1 AND deleted_at IS NULL`,
		a.ID, a.Title, a.Body, string(a.Status), timePtrToPG(a.PublishedAt),
		timePtrToPG(a.EffectiveFrom), timePtrToPG(a.ExpiresAt), a.Pinned,
		a.PublishRevision, string(a.Audience.Scope), intRoles(a.Audience.Roles),
		a.Audience.College, a.Audience.Grade, r.nowFn())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return content.Errorf(content.CodeNotFound, "公告不存在")
	}
	// **回写入参的 updatedAt**（与内存实现一致）：服务层会把 a 直接当响应
	// 返回，不回写会让客户端拿到旧时间。用写入时的同一个 nowFn() 取值，
	// 保证「库里存的」与「回给客户端的」是同一时刻。
	a.UpdatedAt = r.nowFn()
	return nil
}

// FindByID 实现 announcement.Repo：**含已软删的行**。
//
// 不做可见性过滤（草稿、已撤回都返回），是否允许读者看到由服务层判定。
func (r *AnnouncementPGRepo) FindByID(ctx context.Context, id int64) (*announcement.Announcement, error) {
	// 软删视作不存在：与 list() / Update / MarkDeleted 的口径一致（都带
	// deleted_at IS NULL）。此前这里只按 id 查，软删后仍返回实体，导致
	// "软删后 FindByID 应返回 NotFound" 长期失败。
	row := r.pool.QueryRow(ctx,
		`SELECT `+announcementColumns+` FROM announcements WHERE id = $1 AND deleted_at IS NULL`, id)
	a, err := scanAnnouncement(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, content.Errorf(content.CodeNotFound, "公告不存在")
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// ListPublished 实现 announcement.Repo：只返回已发布且未软删的行。
//
// 有效期与受众过滤在服务层做（受众判定依赖读者画像，属领域判断，
// 不该泄进存储层）——这条分工与内存实现完全一致。
func (r *AnnouncementPGRepo) ListPublished(ctx context.Context, after announcement.PageCursor, limit int) ([]*announcement.Announcement, error) {
	return r.list(ctx, after, limit, true)
}

// ListAll 实现 announcement.Repo：管理端列表，含草稿与已撤回。
func (r *AnnouncementPGRepo) ListAll(ctx context.Context, after announcement.PageCursor, limit int) ([]*announcement.Announcement, error) {
	return r.list(ctx, after, limit, false)
}

// list 是两个列表方法的共同实现。
//
// 游标与排序的对应关系（**这是本文件最需要看对的一段**）：
//
//	announcement.LessForOrder   ⇔  ORDER BY pinned DESC, published_at DESC, id DESC
//	announcement.AfterCursor    ⇔  (pinned, published_at, id) < ($p, $t, $i)
//
// 为什么行值比较就够了：Postgres 的布尔序是 false < true，而投影里 pinned 是
// DESC，所以「排在游标之后」正好等价于三元组字典序严格小于游标——
// 这与 AfterCursor 里「a.Pinned != c.Pinned 时只要 a.Pinned=false 且 c.Pinned=true」
// 是同一件事，不必拆成三段 OR。
//
// print_at 的 NULL：草稿没有发布时间。内存实现用 PublishedOrZero（零值时间）
// 参与比较与排序，故这里也把 NULL 归一到「零值时间」的量级：
//   - 管理端列表用 COALESCE(published_at, '-infinity')，零值游标传 NULL
//     （`COALESCE($2::timestamptz, '-infinity')`），于是 NULL 行彼此按 id 比较；
//   - 读者侧列表有 CHECK 保证 published 必有 published_at，故直接用裸列，
//     以便命中 migrations/0005 里的部分索引 idx_announcements_published_order。
func (r *AnnouncementPGRepo) list(ctx context.Context, after announcement.PageCursor, limit int, publishedOnly bool) ([]*announcement.Announcement, error) {
	// pubExpr 是「参与排序与游标比较的发布时间表达式」。
	pubExpr := "published_at"
	if !publishedOnly {
		pubExpr = "COALESCE(published_at, '-infinity'::timestamptz)"
	}

	b := &pgArgs{}
	sql := `SELECT ` + announcementColumns + ` FROM announcements WHERE deleted_at IS NULL`
	if publishedOnly {
		sql += ` AND status = 'published'`
	}
	if !after.Zero() {
		// 游标时间：读者侧传裸列的量级（零值 → 零值，与内存实现一致）；
		// 管理端把零值转成 NULL，交给 COALESCE 归一成 '-infinity'。
		var cursorAt any = after.PublishedAt
		if !publishedOnly && after.PublishedAt.IsZero() {
			cursorAt = nil
		}
		sql += ` AND (pinned, ` + pubExpr + `, id) < (` +
			b.add(after.Pinned) + `, COALESCE(` + b.add(cursorAt) + `::timestamptz, '-infinity'::timestamptz), ` +
			b.add(after.ID) + `)`
	}
	// 排序表达式必须与游标比较里的表达式一致，否则翻页边界会错位。
	sql += ` ORDER BY pinned DESC, ` + pubExpr + ` DESC, id DESC`
	// limit <= 0 表示不限条数（与内存实现的 `if limit > 0 && ...` 同义）。
	if limit > 0 {
		sql += ` LIMIT ` + b.add(limit)
	}

	rows, err := r.pool.Query(ctx, sql, b.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*announcement.Announcement, 0, 16)
	for rows.Next() {
		a, err := scanAnnouncement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// MarkDeleted 实现 announcement.Repo：软删（幂等）。
//
// 幂等语义与内存实现一致：已删的再删一次返回 nil，且**不改 updated_at**
// （内存实现命中 `a.DeletedAt != nil` 时直接 return nil）。故这里只更新
// 「尚未软删」的行，0 行受影响时再区分「不存在」与「已删」。
func (r *AnnouncementPGRepo) MarkDeleted(ctx context.Context, id int64, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE announcements SET deleted_at = $2, updated_at = $2
		 WHERE id = $1 AND deleted_at IS NULL`, id, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	// 0 行受影响：要么行不存在，要么已经软删。多一次只读查询换取与内存
	// 实现一致的错误语义（已删 → 幂等成功；不存在 → NotFound）。
	var exists bool
	err = r.pool.QueryRow(ctx, `SELECT true FROM announcements WHERE id = $1`, id).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Errorf(content.CodeNotFound, "公告不存在")
	}
	if err != nil {
		return err
	}
	return nil
}

// ── 扫描与类型映射 ────────────────────────────────────────────────

// rowScanner 是 pgx.Row 与 pgx.Rows 的公共 Scan 签名。
//
// 抽出来是为了让「单行 FindByID」与「多行 list」共用同一份扫描逻辑——
// 列清单与 Scan 参数一旦分两处写，加列时必然漏改一处。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanAnnouncement 把一行扫成领域结构体。
//
// 可空时间列一律经 pgtype.Timestamptz 中转：直接扫进 **time.Time 依赖
// pgx 的指针解引用包装，显式中转后行为确定、读取处也不必关心 NULL。
func scanAnnouncement(row rowScanner) (*announcement.Announcement, error) {
	var (
		a         announcement.Announcement
		status    string
		scope     string
		roles     []int32
		published pgtype.Timestamptz
		effective pgtype.Timestamptz
		expires   pgtype.Timestamptz
		deleted   pgtype.Timestamptz
	)
	err := row.Scan(
		&a.ID, &a.Title, &a.Body, &status,
		&published, &effective, &expires,
		&a.Pinned, &a.PublishRevision, &scope, &roles,
		&a.Audience.College, &a.Audience.Grade,
		&a.CreatedAt, &a.UpdatedAt, &deleted,
	)
	if err != nil {
		return nil, err
	}
	a.Status = announcement.Status(status)
	a.PublishedAt = pgTimePtr(published)
	a.EffectiveFrom = pgTimePtr(effective)
	a.ExpiresAt = pgTimePtr(expires)
	a.DeletedAt = pgTimePtr(deleted)
	a.Audience.Scope = announcement.AudienceScope(scope)
	a.Audience.Roles = rolesFromInt32(roles)
	return &a, nil
}

// intRoles 把 []int 映射成写库用的 []int32（列类型 INTEGER[]）。
//
// nil / 空切片都必须写成空数组 `{}` 而不是 NULL：列是 NOT NULL DEFAULT '{}'，
// 写 NULL 会直接违反约束。pgx 对 nil 切片编码为 NULL、对空切片编码为 '{}'，
// 所以这里必须显式归一。
func intRoles(roles []int) []int32 {
	out := make([]int32, len(roles))
	for i, r := range roles {
		out[i] = int32(r)
	}
	return out
}

// rolesFromInt32 把库里的 INTEGER[] 转回领域用的 []int。
//
// 空数组归成 nil：领域结构体的零值是 nil，JSON 侧 `omitempty` 依赖这一点，
// 交回 `[]int{}` 会让「无角色」序列化成 `"roles":[]`，与内存实现不一致。
func rolesFromInt32(roles []int32) []int {
	if len(roles) == 0 {
		return nil
	}
	out := make([]int, len(roles))
	for i, r := range roles {
		out[i] = int(r)
	}
	return out
}

// timePtrToPG 把可空时间转成 pgx 可编码的 timestamptz（nil → NULL）。
func timePtrToPG(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// pgTimePtr 把扫描到的 timestamptz 转成可空时间（NULL → nil）。
//
// 每次返回新指针：调用方可能改它，不能让多个字段共享同一块内存。
func pgTimePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// ── 参数拼装 ──────────────────────────────────────────────────────

// pgArgs 是「按顺序追加参数并返回 $n 占位符」的小工具。
//
// 为什么不手工数 $1/$2：公告列表的可选条件（游标、limit）会让占位符编号
// 随分支漂移，手写极易出现「加了 WHERE 忘了把 LIMIT 的编号 +1」，而那种
// 错误在只有一条数据时不会暴露。**任何值都不拼进 SQL 文本**，只拼占位符。
type pgArgs struct {
	args []any
}

// add 追加一个参数并返回它的占位符（从 $1 起）。
func (b *pgArgs) add(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

// 编译期确认：PG 实现满足领域接口（接口漂移在编译期暴露，而不是运行时）。
var _ announcement.Repo = (*AnnouncementPGRepo)(nil)
