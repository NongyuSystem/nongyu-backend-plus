package pgstore

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
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/content/post"
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
	id, board, title, body, attachments, published_at, edited_at,
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

	err := r.pool.QueryRow(ctx, `
		INSERT INTO posts (board, title, body, attachments, author_id,
		                   published_at, edited_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)
		RETURNING id, published_at, created_at, updated_at`,
		string(p.Board), p.Title, p.Body, attachments, p.AuthorID,
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
	now := r.nowFn()

	tag, err := r.pool.Exec(ctx, `
		UPDATE posts
		   SET title = $2, body = $3, attachments = $4,
		       edited_at = $5, updated_at = $6
		 WHERE id = $1 AND deleted_at IS NULL`,
		p.ID, p.Title, p.Body, attachments, p.EditedAt, now)
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
	return r.listPosts(ctx, after, limit, string(board))
}

// ListAll 实现 post.Repo：跨板块（管理端）。
func (r *PostPGRepo) ListAll(ctx context.Context, after post.PageCursor, limit int) ([]*post.Post, error) {
	return r.listPosts(ctx, after, limit, "")
}

// listPosts 是 List / ListAll 的共用实现。
//
// board 为空串表示不过滤。游标用**行值比较** (published_at, id) < ($n, $m)：
// 这与 ORDER BY published_at DESC, id DESC 是同一个全序，天然满足
// 「严格排在游标之后」的语义；写成 published_at < $1 OR (published_at = $1 AND id < $2)
// 也对，但行值比较让「排序键」和「过滤条件」在 SQL 里是同一个形状，
// 改排序时不会只改一半。
func (r *PostPGRepo) listPosts(ctx context.Context, after post.PageCursor, limit int, board string) ([]*post.Post, error) {
	// 三态而不是两个分支：有板块 + 有游标 / 有板块 + 无游标 / 无板块（游标两种都覆盖）。
	// 用 $1 是否为空来动态决定 board 条件，保持只发一条 SQL（参数化，不拼字符串）。
	sql := `SELECT ` + postColumns + ` FROM posts
	         WHERE deleted_at IS NULL
	           AND ($1 = '' OR board = $1)`
	args := []any{board}
	if !after.Zero() {
		sql += `
	           AND (published_at, id) < ($2, $3)`
		args = append(args, after.PublishedAt, after.ID)
	}
	sql += `
	         ORDER BY published_at DESC, id DESC`
	if limit > 0 {
		// 占位符序号随调用形态变化（有没有游标），这里按参数个数算，
		// 拼的只是 `$N` 这个数字，值一律走参数绑定。
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
	if err := s.Scan(&p.ID, &board, &p.Title, &p.Body, &p.Attachments,
		&p.PublishedAt, &p.EditedAt, &p.DeletedAt, &p.AuthorID,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Board = post.Board(board)
	if p.Attachments == nil {
		p.Attachments = []string{}
	}
	return &p, nil
}

// 编译期确认。
var _ post.Repo = (*PostPGRepo)(nil)
