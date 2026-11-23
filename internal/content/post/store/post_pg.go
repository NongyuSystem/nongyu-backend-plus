package store

// 帖子域的 PostgreSQL 仓储。
//
// 与 PostMemoryRepo 同一接口（post.Repo），同一排序与游标判据；区别只在于
// 「唯一性由谁保证」：
//   - 内存版靠 map 键 + 互斥锁；
//   - 本实现靠 post_reactions 的复合唯一索引 uq_post_reactions_target_user。
//
// 这不是实现细节的差异，而是**正确性归属**的差异：进程内锁在多副本部署下
// 等于没有，数据库唯一索引才是真正的闸门。所以 AddReaction 走
// INSERT ... ON CONFLICT DO NOTHING RETURNING id，由「有没有返回行」判定
// created，绝不先 SELECT 再 INSERT（那是竞态）。

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"fmt"
	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/content/post"
	"strings"
)

// PostPGRepo 是 post.Repo 的 PostgreSQL 实现。
//
// 本类型只做持久化：归属判定、编辑窗口、附件 key 校验都在服务层，
// 与内存实现的分工完全一致。
type PostPGRepo struct {
	pool  *pgxpool.Pool
	nowFn func() time.Time
}

// NewPostPGRepo 构造 PG 仓储。nowFn 可为 nil（默认 time.Now）。
//
// 保留 nowFn 不是为了「让测试可注入时钟」这么泛的理由，而是为了让
// Create/CreateReply/MarkDeleted 写进库的时间戳与内存实现取同一个时钟源——
// 装配层把 Clock 传进来，两条路径的时间语义才不会漂移。
func NewPostPGRepo(pool *pgxpool.Pool, now func() time.Time) *PostPGRepo {
	if now == nil {
		now = time.Now
	}
	return &PostPGRepo{pool: pool, nowFn: now}
}

// postColumns 是 posts 表读出映射到 post.Post 的列序。
//
// 抽成常量而不是每处手写：列序与 scanPost 的 Scan 参数必须严格对齐，
// 两处各写一份迟早会出现「多扫一列」这种只在运行时炸的错误。
const postColumns = `
	id, board, title, body, attachments, detail, published_at, edited_at,
	deleted_at, author_id, created_at, updated_at`

// Create 实现 post.Repo。ID / 时间戳由数据库回填后写回入参。
func (r *PostPGRepo) Create(ctx context.Context, p *post.Post) error {
	now := r.nowFn()
	publishedAt := p.PublishedAt
	if publishedAt.IsZero() {
		publishedAt = now
	}
	// attachments 是 TEXT[]：nil 与空数组在语义上都是「没附件」，
	// 但 NOT NULL 列不接受 NULL，统一收敛成空切片。
	attachments := p.Attachments
	if attachments == nil {
		attachments = []string{}
	}
	// detail 是 NOT NULL JSONB：空对象而不是 NULL，与列默认值保持一致。
	detail, err := post.NormalizeDetailJSON(p.Detail)
	if err != nil {
		return err
	}

	err = r.pool.QueryRow(ctx, `
		INSERT INTO posts (board, title, body, attachments, detail, author_id,
		                   published_at, edited_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9,$9)
		RETURNING id, published_at, created_at, updated_at`,
		string(p.Board), p.Title, p.Body, attachments, detail, p.AuthorID,
		publishedAt, p.EditedAt, now,
	).Scan(&p.ID, &p.PublishedAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return err
	}
	return nil
}

// Update 实现 post.Repo：只覆盖标题/正文/附件/EditedAt，并推进 UpdatedAt。
//
// 已软删的行视为不存在（返回 not_found）：软删之后再编辑是用户不可见的状态，
// 让它成功只会把「删掉的帖子还能改」这种怪现象埋进数据里。
// publishedAt 与 authorID 刻意不在 SET 里——改了会让排序键与归属漂移。
func (r *PostPGRepo) Update(ctx context.Context, p *post.Post) error {
	attachments := p.Attachments
	if attachments == nil {
		attachments = []string{}
	}
	detail, err := post.NormalizeDetailJSON(p.Detail)
	if err != nil {
		return err
	}
	now := r.nowFn()

	tag, err := r.pool.Exec(ctx, `
		UPDATE posts
		   SET title = $2, body = $3, attachments = $4, detail = $5::jsonb,
		       edited_at = $6, updated_at = $7
		 WHERE id = $1 AND deleted_at IS NULL`,
		p.ID, p.Title, p.Body, attachments, detail, p.EditedAt, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.Errorf(apperr.CodeNotFound, "帖子不存在")
	}
	p.UpdatedAt = now
	return nil
}

// FindByID 实现 post.Repo（含已软删行，供归属与编辑窗口判定）。
func (r *PostPGRepo) FindByID(ctx context.Context, id int64) (*post.Post, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+postColumns+` FROM posts WHERE id = $1`, id)
	p, err := scanPost(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Errorf(apperr.CodeNotFound, "帖子不存在")
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// List 实现 post.Repo：按板块过滤（零值不过滤）+ publishedAt 倒序 + 游标分页。
func (r *PostPGRepo) List(ctx context.Context, board post.Board, after post.PageCursor, limit int) ([]*post.Post, error) {
	if board == "" {
		return r.ListAll(ctx, after, limit)
	}
	return r.listPosts(ctx, after, limit, string(board), 0)
}

// ListAll 实现 post.Repo：跨板块（管理端）。
func (r *PostPGRepo) ListAll(ctx context.Context, after post.PageCursor, limit int) ([]*post.Post, error) {
	return r.listPosts(ctx, after, limit, "", 0)
}

// ListByDetail 实现 post.Repo（按 detail 字段精确匹配）。
//
// 用 JSONB 包含运算符 `@>` 而不是 `detail->>'k' = v`：
//   - `@>` 能命中 GIN 索引 (idx_posts_detail_gin)，`->>` 不能；
//   - `@>` 的语义就是「JSON 里包含这些键值对」，与我们的 AND 语义一致。
//
// 但 `@>` 是**大小写敏感**的，而内存版用 EqualFold。为了让两套实现行为
// 一致，这里对每个条件同时接受原值与全小写值两种写法——
// 见 lowerVariants：`C语言` 会同时匹配 `C语言` 与 `c语言`。
// 这是「索引友好」与「大小写不敏感」之间的折中：不建表达式索引（
// 那要改表结构），而是让查询侧多给几个候选值。
func (r *PostPGRepo) ListByDetail(ctx context.Context, board post.Board, match map[string]string, after post.PageCursor, limit int) ([]*post.Post, error) {
	if len(match) == 0 {
		// 没有任何条件时不返回全表：那等于绕过了这个接口的存在理由
		//（它是「按字段查」，不是「列全部」）。
		return []*post.Post{}, nil
	}

	// 构造 OR 的多个 @> 条件：每个条件一组「原值 + 小写值」。
	// 例如 match={teacherName:"王老师"} → $(detail @> '{"teacherName":"王老师"}' OR detail @> '{"teacherName":"王老师"}')
	args := []any{string(board)}
	var conds []string
	idx := 2
	for k, v := range match {
		var ors []string
		for _, variant := range lowerVariants(v) {
			obj, err := json.Marshal(map[string]string{k: variant})
			if err != nil {
				return nil, err
			}
			ors = append(ors, fmt.Sprintf("detail @> $%d::jsonb", idx))
			args = append(args, string(obj))
			idx++
		}
		conds = append(conds, "("+strings.Join(ors, " OR ")+")")
	}

	sql := `SELECT ` + postColumns + ` FROM posts
	         WHERE deleted_at IS NULL
	           AND ($1 = '' OR board = $1)
	           AND ` + strings.Join(conds, " AND ")
	if !after.Zero() {
		sql += fmt.Sprintf(`
	           AND (published_at, id) < ($%d, $%d)`, idx, idx+1)
		args = append(args, after.PublishedAt, after.ID)
		idx += 2
	}
	sql += `
	         ORDER BY published_at DESC, id DESC`
	if limit > 0 {
		sql += fmt.Sprintf(`
	         LIMIT $%d`, idx)
		args = append(args, limit)
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*post.Post, 0)
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// lowerVariants 返回一个值的「原样」与「全小写」两种写法（去重）。
//
// 用途见 ListByDetail 的说明：`@>` 大小写敏感，而业务上要求不敏感，
// 于是查询侧同时给两个候选。对纯中文值两者相同，会自动去重成一条。
func lowerVariants(v string) []string {
	v = strings.TrimSpace(v)
	lower := strings.ToLower(v)
	if lower == v {
		return []string{v}
	}
	return []string{v, lower}
}

// ListByAuthor 实现 post.Repo（「我的帖子」）。
//
// authorID <= 0 时**返回空列表而不是回落到全站**：这是隐私边界，
// 宁可返回空，也不要因为一次脏参数把所有人的帖子露出去。
func (r *PostPGRepo) ListByAuthor(ctx context.Context, authorID int64, board post.Board, after post.PageCursor, limit int) ([]*post.Post, error) {
	if authorID <= 0 {
		return []*post.Post{}, nil
	}
	return r.listPosts(ctx, after, limit, string(board), authorID)
}

// listPosts 是 List / ListAll / ListByAuthor 的共用实现。
//
// 两个可选条件都用「参数恒在、条件恒写」的方式承载：
//   - $1 board：空串表示不过滤；
//   - $2 authorID：<=0 表示不过滤（ListByAuthor 已在入口挡住 <=0）。
//
// 这样 SQL 的 WHERE 形状是固定的，新增条件时不用改写已有占位符序号。
// 游标用**行值比较** (published_at, id) < ($n, $m)：它与
// ORDER BY published_at DESC, id DESC 是同一个全序，天然满足「严格排在
// 游标之后」；写成 OR 展开也对，但行值比较让排序键与过滤条件同形状，
// 改排序时不会只改一半。
func (r *PostPGRepo) listPosts(ctx context.Context, after post.PageCursor, limit int, board string, authorID int64) ([]*post.Post, error) {
	sql := `SELECT ` + postColumns + ` FROM posts
	         WHERE deleted_at IS NULL
	           AND ($1 = '' OR board = $1)
	           AND ($2 <= 0 OR author_id = $2)`
	args := []any{board, authorID}

	if !after.Zero() {
		// $3/$4 是游标（两个值）。
		sql += `
	           AND (published_at, id) < ($3, $4)`
		args = append(args, after.PublishedAt, after.ID)
	}
	sql += `
	         ORDER BY published_at DESC, id DESC`
	if limit > 0 {
		// LIMIT 的序号 = 已有参数个数 + 1；拼的只是 `$N` 这个数字，
		// 值一律走参数绑定。
		sql += `
	         LIMIT $` + strconv.Itoa(len(args)+1)
		args = append(args, limit)
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*post.Post, 0)
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkDeleted 实现 post.Repo（幂等：已删或不存在都不报错以外的副作用）。
//
// 与内存实现对齐：目标行不存在 → not_found；已软删 → no-op 成功。
func (r *PostPGRepo) MarkDeleted(ctx context.Context, id int64, at time.Time) error {
	var exists bool
	err := r.pool.QueryRow(ctx, `SELECT true FROM posts WHERE id = $1`, id).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.Errorf(apperr.CodeNotFound, "帖子不存在")
	}
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		UPDATE posts SET deleted_at = $2, updated_at = $2
		 WHERE id = $1 AND deleted_at IS NULL`, id, at)
	return err
}

// CreateReply 实现 post.Repo。
func (r *PostPGRepo) CreateReply(ctx context.Context, rp *post.Reply) error {
	publishedAt := rp.PublishedAt
	if publishedAt.IsZero() {
		publishedAt = r.nowFn()
	}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO post_replies (post_id, body, author_id, published_at)
		VALUES ($1,$2,$3,$4)
		RETURNING id, published_at`,
		rp.PostID, rp.Body, rp.AuthorID, publishedAt,
	).Scan(&rp.ID, &rp.PublishedAt)
	if err != nil {
		return err
	}
	return nil
}

// ListReplies 实现 post.Repo：publishedAt 升序、同刻 ID 升序（先来先看）。
func (r *PostPGRepo) ListReplies(ctx context.Context, postID int64, limit int) ([]*post.Reply, error) {
	sql := `
		SELECT id, post_id, body, author_id, published_at, deleted_at
		  FROM post_replies
		 WHERE post_id = $1 AND deleted_at IS NULL
		 ORDER BY published_at ASC, id ASC`
	args := []any{postID}
	if limit > 0 {
		sql += ` LIMIT $2`
		args = append(args, limit)
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*post.Reply, 0)
	for rows.Next() {
		var rp post.Reply
		if err := rows.Scan(&rp.ID, &rp.PostID, &rp.Body, &rp.AuthorID,
			&rp.PublishedAt, &rp.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, &rp)
	}
	return out, rows.Err()
}

// FindReplyByID 实现 post.Repo（含已软删行）。
func (r *PostPGRepo) FindReplyByID(ctx context.Context, id int64) (*post.Reply, error) {
	var rp post.Reply
	err := r.pool.QueryRow(ctx, `
		SELECT id, post_id, body, author_id, published_at, deleted_at
		  FROM post_replies WHERE id = $1`, id,
	).Scan(&rp.ID, &rp.PostID, &rp.Body, &rp.AuthorID, &rp.PublishedAt, &rp.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Errorf(apperr.CodeNotFound, "留言不存在")
	}
	if err != nil {
		return nil, err
	}
	return &rp, nil
}

// MarkReplyDeleted 实现 post.Repo（幂等软删）。
//
// `WHERE deleted_at IS NULL` 让重复删除不改动任何行，
// 也不会把原本的删除时间覆盖成当前时间（审计价值）。
func (r *PostPGRepo) MarkReplyDeleted(ctx context.Context, id int64, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE post_replies SET deleted_at = $2
		 WHERE id = $1 AND deleted_at IS NULL`, id, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// 区分「不存在」与「已删」：已删按幂等成功处理。
		var exists bool
		if err := r.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM post_replies WHERE id = $1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return apperr.Errorf(apperr.CodeNotFound, "留言不存在")
		}
	}
	return nil
}

// MarkRepliesDeletedByPost 实现 post.Repo（级联软删某帖下全部未删留言）。
//
// 一条 UPDATE 原子完成，避免逐条删的 N+1 与半状态。
func (r *PostPGRepo) MarkRepliesDeletedByPost(ctx context.Context, postID int64, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE post_replies SET deleted_at = $2
		 WHERE post_id = $1 AND deleted_at IS NULL`, postID, at)
	return err
}

// AddReaction 实现 post.Repo：同键已存在返回 created=false。
//
// 关键点：**唯一性判定交给数据库**。
//
//		INSERT ... ON CONFLICT DO NOTHING RETURNING id
//
//	  - 插入成功 → 有返回行 → created=true；
//	  - 键已存在 → ON CONFLICT 吞掉、无返回行 → pgx.ErrNoRows → created=false。
//
// 这里不能写成「先 SELECT 判断再 INSERT」：两个并发请求会同时看到「不存在」，
// 然后一个成功一个撞唯一索引报错——要么把 23505 当业务错误（脏），
// 要么重试（慢且仍可能失败）。ON CONFLICT DO NOTHING + RETURNING 是原子的，
// 且不需要把冲突错误转译成业务语义。
func (r *PostPGRepo) AddReaction(ctx context.Context, rn *post.Reaction) (bool, error) {
	var id int64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO post_reactions (target_type, target_id, reaction, user_id, created_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (target_type, target_id, reaction, user_id) DO NOTHING
		RETURNING id`,
		string(rn.TargetType), rn.TargetID, string(rn.Kind), rn.UserID, r.nowFn(),
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// 没有返回行 = 冲突被 DO NOTHING 吞掉 = 这条反应本来就在。
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// RemoveReaction 实现 post.Repo（不存在时 removed=false，幂等）。
//
// 直接用 RowsAffected 判定，不先 SELECT：删除本身就是「存在性」的原子回答。
func (r *PostPGRepo) RemoveReaction(ctx context.Context, tt post.TargetType, targetID int64, rt post.ReactionKind, userID int64) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM post_reactions
		 WHERE target_type = $1 AND target_id = $2 AND reaction = $3 AND user_id = $4`,
		string(tt), targetID, string(rt), userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ReactionCounts 实现 post.Repo：一次查询同时取点赞数与收藏数。
//
// 用 COUNT(*) FILTER (WHERE ...) 而不是两条 SELECT：
// 两条查询会在写并发下给出**互相不一致**的快照（一条读到点赞前、一条读到之后），
// 界面上就会出现「likes+favorites 都对但总数不对」这种没法解释的现象。
// 单条聚合天然是同一个快照。
func (r *PostPGRepo) ReactionCounts(ctx context.Context, tt post.TargetType, targetID int64) (int, int, error) {
	var likes, favorites int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE reaction = $3),
		       COUNT(*) FILTER (WHERE reaction = $4)
		  FROM post_reactions
		 WHERE target_type = $1 AND target_id = $2`,
		string(tt), targetID, string(post.ReactionLike), string(post.ReactionFavorite),
	).Scan(&likes, &favorites)
	if err != nil {
		return 0, 0, err
	}
	return likes, favorites, nil
}

// HasReaction 实现 post.Repo。
func (r *PostPGRepo) HasReaction(ctx context.Context, tt post.TargetType, targetID int64, rt post.ReactionKind, userID int64) (bool, error) {
	var one int
	err := r.pool.QueryRow(ctx, `
		SELECT 1 FROM post_reactions
		 WHERE target_type = $1 AND target_id = $2 AND reaction = $3 AND user_id = $4
		 LIMIT 1`,
		string(tt), targetID, string(rt), userID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// postRowScanner 是 *pgx.Row 与 pgx.Rows 的公共读面，让 scanPost 两处复用。
//
// 本包内别的域各自有同形状的接口（announcement_pg.go 的 rowScanner）；
// 刻意不复用它们：域之间不该为了省三行代码而互相依赖私有符号，
// 那个符号一旦被改，坏的是另一个域的文件。
type postRowScanner interface {
	Scan(dest ...any) error
}

// scanPost 按 postColumns 的列序扫一行。attachments 落库是 TEXT[]。
func scanPost(s postRowScanner) (*post.Post, error) {
	var p post.Post
	var board string
	// detail 用 jsonb 列；pgx 能直接扫进 map[string]any，无需手工 Unmarshal。
	var detail []byte
	if err := s.Scan(&p.ID, &board, &p.Title, &p.Body, &p.Attachments, &detail,
		&p.PublishedAt, &p.EditedAt, &p.DeletedAt, &p.AuthorID,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Board = post.Board(board)
	if p.Attachments == nil {
		p.Attachments = []string{}
	}
	if len(detail) > 0 {
		if err := json.Unmarshal(detail, &p.Detail); err != nil {
			return nil, err
		}
	}
	if p.Detail == nil {
		p.Detail = map[string]any{}
	}
	return &p, nil
}

// 编译期确认。
var _ post.Repo = (*PostPGRepo)(nil)
