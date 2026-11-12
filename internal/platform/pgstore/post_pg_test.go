// Package pgstore_test 是 PG 仓储的外部测试包（只用导出 API）。
//
// 隔离方案：每个测试文件用自己的 schema（CREATE SCHEMA + SET search_path），
// 跑前在该 schema 里执行权威迁移建表。这样：
//   - 与其它包（identity / platform / server 等）的集成测试并行安全；
//   - 每个测试可以从空表开始，不必靠「清理行」维持干净状态。
//
// 未设置 DATABASE_URL 时全部 t.Skip：纯单测环境（CI 无 PG）不依赖数据库。
package pgstore_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/content/post"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/platform/pgstore"
)

// pgTestSchemaPrefix 是测试 schema 的公共前缀，便于一次性清理。
const pgTestSchemaPrefix = "t_pgstore_"

// newPostTrackPool 建一个指向**独立 schema** 的连接池，并在其中执行全部迁移建表。
//
// schema 名由测试名派生：同一个包反复跑、或两个测试并行跑，
// 都不会互相看到对方的行。测试结束 DROP SCHEMA CASCADE。
//
// 名字带 PostTrack 前缀而不是通用的 newPGTestPool：外部测试包里
// announcement_pg_test.go 已有一个同签名的 newPGTestPool（它只建公告/消息域需要的东西），
// 两个同包同签名的函数会直接 redeclared。各自按域命名，互不干扰。
func newPostTrackPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	base := os.Getenv("DATABASE_URL")
	if base == "" {
		t.Skip("未设置 DATABASE_URL，跳过 Postgres 集成测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 管理连接：不带 search_path，用来建/删 schema。
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Skipf("Postgres 不可达，跳过：%v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("Postgres 不可达，跳过：%v", err)
	}

	schema := pgTestSchemaPrefix + pgTestSchemaName(t.Name())
	if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgTestQuoteIdent(schema)+" CASCADE"); err != nil {
		admin.Close()
		t.Fatalf("清理旧 schema %s：%v", schema, err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgTestQuoteIdent(schema)); err != nil {
		admin.Close()
		t.Fatalf("建 schema %s：%v", schema, err)
	}

	// 在隔离 schema 上跑权威迁移（migrations/*.sql），不手写 DDL：
	// 表结构与生产同源，测试才能发现「仓储假设了列不存在」这类问题。
	migDSN, err := platform.WithSearchPath(base, schema)
	if err != nil {
		admin.Close()
		t.Fatalf("拼迁移 DSN：%v", err)
	}
	if err := platform.Migrate(ctx, migDSN); err != nil {
		admin.Close()
		t.Fatalf("在 schema %s 上执行迁移：%v", schema, err)
	}

	pool, err := pgxpool.New(ctx, migDSN)
	if err != nil {
		admin.Close()
		t.Fatalf("建测试连接池：%v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		c, cc := context.WithTimeout(context.Background(), 15*time.Second)
		defer cc()
		_, _ = admin.Exec(c, "DROP SCHEMA IF EXISTS "+pgTestQuoteIdent(schema)+" CASCADE")
		admin.Close()
	})
	return pool
}

// sanitizeSchemaName 把 t.Name() 变成合法 schema 片段。
//
// Postgres 标识符上限 63，前缀已占 10；这里截到 40 并只留 [a-z0-9_]。
func pgTestSchemaName(name string) string {
	lower := strings.ToLower(name)
	var b strings.Builder
	for _, r := range lower {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := b.String()
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// quoteIdent 给标识符加引号（schema 名由测试名派生，理论上安全，仍显式引用）。
func pgTestQuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// ---- 帖子域测试 ----

// TestPostPGRepo_SoftDeleteAndListFilter 验收：
//   - 软删的帖子不出现在 List / ListAll；
//   - List 按板块过滤，ListAll 跨板块；
//   - FindByID 仍能取到已软删行（归属判定需要）。
func TestPostPGRepo_SoftDeleteAndListFilter(t *testing.T) {
	pool := newPostTrackPool(t)
	repo := pgstore.NewPostPGRepo(pool, pgTestClock(pgTestBaseTime))
	ctx := context.Background()

	// 三个板块组合：反馈 2 条、留言 1 条。
	a := mustPgPost(t, repo, post.BoardFeedback, "反馈甲", 1)
	b := mustPgPost(t, repo, post.BoardFeedback, "反馈乙", 2)
	c := mustPgPost(t, repo, post.BoardCourtyard, "留言甲", 3)

	// 软删一条反馈。
	if err := repo.MarkDeleted(ctx, b.ID, pgTestBaseTime.Add(time.Minute)); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	// 幂等：再删一次仍返回 nil。
	if err := repo.MarkDeleted(ctx, b.ID, pgTestBaseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("重复 MarkDeleted 应幂等成功，实际 %v", err)
	}

	feedback, err := repo.List(ctx, post.BoardFeedback, post.PageCursor{}, 0)
	if err != nil {
		t.Fatalf("List(feedback): %v", err)
	}
	if len(feedback) != 1 || feedback[0].ID != a.ID {
		t.Fatalf("反馈板块应只剩未删的 %d，实际 %v", a.ID, pgPostIDs(feedback))
	}

	courtyard, err := repo.List(ctx, post.BoardCourtyard, post.PageCursor{}, 0)
	if err != nil {
		t.Fatalf("List(courtyard): %v", err)
	}
	if len(courtyard) != 1 || courtyard[0].ID != c.ID {
		t.Fatalf("留言板块应只有 %d，实际 %v", c.ID, pgPostIDs(courtyard))
	}

	all, err := repo.ListAll(ctx, post.PageCursor{}, 0)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAll 应有 2 条未删帖，实际 %v", pgPostIDs(all))
	}

	// 已软删的行仍可按 ID 取到：服务层要用它判归属与「是否已删」。
	got, err := repo.FindByID(ctx, b.ID)
	if err != nil {
		t.Fatalf("FindByID(已软删): %v", err)
	}
	if got.DeletedAt == nil {
		t.Fatalf("FindByID 应带回 deletedAt，实际 nil")
	}

	// 不存在的 ID → not_found。
	if _, err := repo.FindByID(ctx, 99999999); err == nil {
		t.Fatalf("不存在的帖子应返回错误")
	}

	// 已软删的帖子不能再被 Update（软删后编辑是用户不可见的状态）。
	if err := repo.Update(ctx, &post.Post{ID: b.ID, Title: "改", Body: "改"}); err == nil {
		t.Fatalf("已软删帖子 Update 应失败")
	}
}

// TestPostPGRepo_CursorNoDuplicate 验收游标翻页：逐页取完不重不漏。
//
// 关键点是**同刻多条**：published_at 全相同，只靠 published_at 比较会跳过/重复，
// 必须靠 (published_at, id) 行值比较兜底。
func TestPostPGRepo_CursorNoDuplicate(t *testing.T) {
	pool := newPostTrackPool(t)
	repo := pgstore.NewPostPGRepo(pool, pgTestClock(pgTestBaseTime))
	ctx := context.Background()

	const total = 7
	// 全部用**同一个** publishedAt：最容易暴露游标只比时间不比 id 的 bug。
	for i := 0; i < total; i++ {
		mustPgPostAt(t, repo, post.BoardFeedback, "同刻帖", int64(i+1), pgTestBaseTime)
	}

	seen := map[int64]bool{}
	cur := post.PageCursor{}
	for page := 0; page < total+2; page++ {
		got, err := repo.List(ctx, post.BoardFeedback, cur, 2)
		if err != nil {
			t.Fatalf("第 %d 页 List: %v", page, err)
		}
		if len(got) == 0 {
			break
		}
		for _, p := range got {
			if seen[p.ID] {
				t.Fatalf("游标翻页出现重复 id=%d（第 %d 页）", p.ID, page)
			}
			seen[p.ID] = true
		}
		last := got[len(got)-1]
		cur = post.PageCursor{PublishedAt: last.PublishedAt, ID: last.ID}
	}
	if len(seen) != total {
		t.Fatalf("翻页共取到 %d 条，期望 %d（漏了）", len(seen), total)
	}
}

// TestPostPGRepo_AddReactionConcurrentSingleRow 验收：真并发 AddReaction 只产生一行。
//
// 这是本仓储最值得守住的一条：唯一性必须来自数据库唯一索引，
// 不是来自「先查后插」。64 个 goroutine 同时插同一个键，
// 应当**没有报错**、恰好 1 个拿到 created=true，且表里恰好 1 行。
//
// 三条断言是分开的，因为它们各自对应一种不同的坏实现：
//   - 出现 23505 唯一冲突 → 实现是「先 SELECT 再 INSERT」，慢一步的请求直接撞索引；
//   - created=true 多于 1 个 → 实现没做任何去重；
//   - 表里多于 1 行 → 去重在应用层做、但并发下漏了。
//
// 并发度取 64 而不是 20 是实测结论：20 个 goroutine 时「先查后插」也能
// 侥幸全不冲突（测试假通过，实测跑 8 次全绿）；64 个才稳定暴露，
// 实测 5 次全部抓到 23505。
func TestPostPGRepo_AddReactionConcurrentSingleRow(t *testing.T) {
	pool := newPostTrackPool(t)
	repo := pgstore.NewPostPGRepo(pool, pgTestClock(pgTestBaseTime))
	ctx := context.Background()

	const targetID = int64(4242)
	const userID = int64(77)
	const goroutines = 64

	var wg sync.WaitGroup
	created := make([]bool, goroutines)
	errs := make([]error, goroutines)
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // 尽量让它们同时开跑
			created[i], errs[i] = repo.AddReaction(ctx, &post.Reaction{
				TargetType: post.TargetPost,
				TargetID:   targetID,
				Kind:       post.ReactionLike,
				UserID:     userID,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	createdCount := 0
	for i := 0; i < goroutines; i++ {
		if errs[i] != nil {
			t.Fatalf("并发 AddReaction 第 %d 个失败（期望幂等不报错）：%v", i, errs[i])
		}
		if created[i] {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("并发 AddReaction 应恰好 1 个返回 created=true，实际 %d 个", createdCount)
	}

	// 物理行数也必须恰好 1：created 计数对但表里两行同样是 bug。
	var rows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM post_reactions
		  WHERE target_type = $1 AND target_id = $2 AND reaction = $3 AND user_id = $4`,
		string(post.TargetPost), targetID, string(post.ReactionLike), userID).Scan(&rows); err != nil {
		t.Fatalf("数反应行：%v", err)
	}
	if rows != 1 {
		t.Fatalf("post_reactions 应恰好 1 行，实际 %d 行", rows)
	}

	// 计数也应为 1。
	likes, favs, err := repo.ReactionCounts(ctx, post.TargetPost, targetID)
	if err != nil {
		t.Fatalf("ReactionCounts: %v", err)
	}
	if likes != 1 || favs != 0 {
		t.Fatalf("计数应为 likes=1 favorites=0，实际 likes=%d favorites=%d", likes, favs)
	}
}

// TestPostPGRepo_Reaction_CRUDAndCounts 验收反应的基本语义与两类计数。
func TestPostPGRepo_Reaction_CRUDAndCounts(t *testing.T) {
	pool := newPostTrackPool(t)
	repo := pgstore.NewPostPGRepo(pool, pgTestClock(pgTestBaseTime))
	ctx := context.Background()

	const targetID = int64(1001)
	// 3 个用户点赞、2 个用户收藏（其中 u1 同时点赞与收藏）。
	for _, u := range []int64{1, 2, 3} {
		if created, err := repo.AddReaction(ctx, &post.Reaction{
			TargetType: post.TargetPost, TargetID: targetID,
			Kind: post.ReactionLike, UserID: u,
		}); err != nil || !created {
			t.Fatalf("AddReaction like u=%d created=%v err=%v", u, created, err)
		}
	}
	for _, u := range []int64{1, 2} {
		if created, err := repo.AddReaction(ctx, &post.Reaction{
			TargetType: post.TargetPost, TargetID: targetID,
			Kind: post.ReactionFavorite, UserID: u,
		}); err != nil || !created {
			t.Fatalf("AddReaction favorite u=%d created=%v err=%v", u, created, err)
		}
	}

	// 重复点赞：created=false，计数不变。
	created, err := repo.AddReaction(ctx, &post.Reaction{
		TargetType: post.TargetPost, TargetID: targetID,
		Kind: post.ReactionLike, UserID: 1,
	})
	if err != nil {
		t.Fatalf("重复 AddReaction: %v", err)
	}
	if created {
		t.Fatalf("重复点赞应返回 created=false")
	}

	likes, favs, err := repo.ReactionCounts(ctx, post.TargetPost, targetID)
	if err != nil {
		t.Fatalf("ReactionCounts: %v", err)
	}
	if likes != 3 || favs != 2 {
		t.Fatalf("计数应为 likes=3 favorites=2，实际 likes=%d favorites=%d", likes, favs)
	}

	// HasReaction 区分 kind：u3 点赞但没收藏。
	if ok, err := repo.HasReaction(ctx, post.TargetPost, targetID, post.ReactionLike, 3); err != nil || !ok {
		t.Fatalf("u3 应已点赞，ok=%v err=%v", ok, err)
	}
	if ok, err := repo.HasReaction(ctx, post.TargetPost, targetID, post.ReactionFavorite, 3); err != nil || ok {
		t.Fatalf("u3 不应收藏，ok=%v err=%v", ok, err)
	}

	// 靶子类型隔离：comment 靶子的计数不受 post 影响。
	if likes, favs, err := repo.ReactionCounts(ctx, post.TargetComment, targetID); err != nil || likes != 0 || favs != 0 {
		t.Fatalf("comment 靶子计数应为 0/0，实际 %d/%d err=%v", likes, favs, err)
	}

	// RemoveReaction 幂等：删两次，第二次 removed=false。
	removed, err := repo.RemoveReaction(ctx, post.TargetPost, targetID, post.ReactionLike, 3)
	if err != nil || !removed {
		t.Fatalf("首次删除应 removed=true，实际 removed=%v err=%v", removed, err)
	}
	removed, err = repo.RemoveReaction(ctx, post.TargetPost, targetID, post.ReactionLike, 3)
	if err != nil || removed {
		t.Fatalf("重复删除应 removed=false，实际 removed=%v err=%v", removed, err)
	}

	likes, favs, _ = repo.ReactionCounts(ctx, post.TargetPost, targetID)
	if likes != 2 || favs != 2 {
		t.Fatalf("删除后计数应为 likes=2 favorites=2，实际 likes=%d favorites=%d", likes, favs)
	}
}

// TestPostPGRepo_RepliesOrdering 验收留言按 publishedAt 升序、同刻按 id 升序。
func TestPostPGRepo_RepliesOrdering(t *testing.T) {
	pool := newPostTrackPool(t)
	repo := pgstore.NewPostPGRepo(pool, pgTestClock(pgTestBaseTime))
	ctx := context.Background()

	p := mustPgPost(t, repo, post.BoardCourtyard, "有留言的帖子", 9)

	// 三条同刻 + 一条更早：同刻按 id 升序，最早的那条排最前。
	early := &post.Reply{PostID: p.ID, Body: "最早", AuthorID: 1,
		PublishedAt: pgTestBaseTime.Add(-time.Hour)}
	if err := repo.CreateReply(ctx, early); err != nil {
		t.Fatalf("CreateReply(early): %v", err)
	}
	var sameMoment []int64
	for i := 0; i < 3; i++ {
		rp := &post.Reply{PostID: p.ID, Body: "同刻", AuthorID: int64(i + 2),
			PublishedAt: pgTestBaseTime}
		if err := repo.CreateReply(ctx, rp); err != nil {
			t.Fatalf("CreateReply(同刻 %d): %v", i, err)
		}
		sameMoment = append(sameMoment, rp.ID)
	}

	got, err := repo.ListReplies(ctx, p.ID, 0)
	if err != nil {
		t.Fatalf("ListReplies: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("应有 4 条留言，实际 %d", len(got))
	}
	if got[0].ID != early.ID {
		t.Fatalf("最早的留言应排第一，实际首条 id=%d（期望 %d）", got[0].ID, early.ID)
	}
	for i, wantID := range sameMoment {
		if got[i+1].ID != wantID {
			t.Fatalf("同刻留言应按 id 升序：第 %d 位期望 %d，实际 %d", i+1, wantID, got[i+1].ID)
		}
	}

	// limit 生效。
	if limited, err := repo.ListReplies(ctx, p.ID, 2); err != nil || len(limited) != 2 {
		t.Fatalf("limit=2 应返回 2 条，实际 %d err=%v", len(limited), err)
	}

	// 软删的留言不出现在列表里，但仍能按 ID 取到。
	_, err = pool.Exec(ctx, `UPDATE post_replies SET deleted_at = now() WHERE id = $1`, early.ID)
	if err != nil {
		t.Fatalf("软删留言：%v", err)
	}
	got, _ = repo.ListReplies(ctx, p.ID, 0)
	if len(got) != 3 {
		t.Fatalf("软删后应剩 3 条，实际 %d", len(got))
	}
	found, err := repo.FindReplyByID(ctx, early.ID)
	if err != nil {
		t.Fatalf("FindReplyByID(已软删): %v", err)
	}
	if found.DeletedAt == nil {
		t.Fatalf("FindReplyByID 应带回 deletedAt")
	}
	if _, err := repo.FindReplyByID(ctx, 99999999); err == nil {
		t.Fatalf("不存在的留言应返回错误")
	}
}

// TestPostPGRepo_AttachmentsRoundTrip 验收 TEXT[] 往返（含空数组）。
func TestPostPGRepo_AttachmentsRoundTrip(t *testing.T) {
	pool := newPostTrackPool(t)
	repo := pgstore.NewPostPGRepo(pool, pgTestClock(pgTestBaseTime))
	ctx := context.Background()

	p := &post.Post{
		Board: post.BoardFeedback, Title: "带附件", Body: "正文",
		Attachments: []string{"posts/a.jpg", "posts/b.png"},
		AuthorID:    5,
	}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.FindByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if len(got.Attachments) != 2 || got.Attachments[0] != "posts/a.jpg" || got.Attachments[1] != "posts/b.png" {
		t.Fatalf("附件往返不一致：%v", got.Attachments)
	}

	// nils 附件 → 落库空数组 → 读回空切片（不是 nil）。
	p2 := &post.Post{Board: post.BoardFeedback, Title: "无附件", Body: "正文", AuthorID: 5}
	if err := repo.Create(ctx, p2); err != nil {
		t.Fatalf("Create(no attachments): %v", err)
	}
	got2, err := repo.FindByID(ctx, p2.ID)
	if err != nil {
		t.Fatalf("FindByID(no attachments): %v", err)
	}
	if len(got2.Attachments) != 0 {
		t.Fatalf("无附件应为空，实际 %v", got2.Attachments)
	}
}

// TestPostPGRepo_Update 验收 Update 只改可写字段、保留 publishedAt/authorID。
func TestPostPGRepo_Update(t *testing.T) {
	pool := newPostTrackPool(t)
	repo := pgstore.NewPostPGRepo(pool, pgTestClock(pgTestBaseTime))
	ctx := context.Background()

	p := mustPgPost(t, repo, post.BoardFeedback, "原标题", 11)
	origPublished := p.PublishedAt

	editedAt := pgTestBaseTime.Add(time.Hour)
	upd := &post.Post{
		ID: p.ID, Board: post.BoardCourtyard, // board 不在可写集里，应被忽略
		Title: "新标题", Body: "新正文",
		Attachments: []string{"posts/new.jpg"},
		EditedAt:    &editedAt,
		AuthorID:    999, // 不可改
	}
	if err := repo.Update(ctx, upd); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := repo.FindByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.Title != "新标题" || got.Body != "新正文" || len(got.Attachments) != 1 {
		t.Fatalf("可写字段未更新：%+v", got)
	}
	if !got.PublishedAt.Equal(origPublished) {
		t.Fatalf("publishedAt 不应被 Update 改动：%v → %v", origPublished, got.PublishedAt)
	}
	if got.AuthorID != 11 {
		t.Fatalf("authorID 不应被 Update 改动，实际 %d", got.AuthorID)
	}
	if got.Board != post.BoardFeedback {
		t.Fatalf("board 不应被 Update 改动，实际 %q", got.Board)
	}
	if got.EditedAt == nil || !got.EditedAt.Equal(editedAt) {
		t.Fatalf("editedAt 应为 %v，实际 %v", editedAt, got.EditedAt)
	}
}
