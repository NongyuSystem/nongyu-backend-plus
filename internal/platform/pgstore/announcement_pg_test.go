// Package pgstore_test 是 pgstore 的**外部测试包**。
//
// 为什么用 pgstore_test 而不是 pgstore：外部测试包只依赖被测包的**导出**能力，
// 从根上避免「测试用到了只有内部才知道的字段」，也避免 import 环（本文件的
// 辅助函数要 import platform 拿迁移入口）。
//
// 数据库隔离策略（**每个测试一个独立 schema**）：
//   - 未设置 DATABASE_URL → 全部 t.Skip（CI 与纯单测环境不需要 Postgres）；
//   - 设置了 → 为每个测试建 `t_pgstore_xxx` schema，在该 schema 上跑
//     platform.Migrate 建表，连接池用带 search_path 的 DSN；
//   - 测试结束 DROP SCHEMA ... CASCADE。
//
// 为什么不用「跑完 TRUNCATE」：测试包可能并行、也可能与本仓其它包的 PG
// 集成测试并发跑，共享 schema 下 TRUNCATE 会互删数据（本仓 platform 包已
// 踩过「单独跑通过、连跑失败」）。独立 schema 把污染可能性归零。
package pgstore_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/content/announcement"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/platform/pgstore"
)

// schemaSeq 保证同一次运行里的 schema 名互不重复。
var schemaSeq atomic.Int64

// newPGTestPool 为本测试建一个独立 schema、跑完全部迁移，返回指向它的连接池。
//
// 返回 nil 表示环境没有配置 DATABASE_URL（此时测试已被 Skip，调用方直接 return）。
func newPGTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	base := os.Getenv("DATABASE_URL")
	if base == "" {
		t.Skip("未设置 DATABASE_URL，跳过 Postgres 集成测试")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ① 管理连接：只用来建/删 schema。用**不带 search_path** 的原始 DSN，
	//    否则 DROP SCHEMA 会落进某个测试 schema 里。
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Skipf("Postgres 连接池创建失败，跳过：%v", err)
		return nil
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("Postgres 不可达，跳过：%v", err)
		return nil
	}

	schema := testSchemaName(t)
	if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoteTestIdent(schema)+" CASCADE"); err != nil {
		admin.Close()
		t.Fatalf("清理旧 schema %s 失败：%v", schema, err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoteTestIdent(schema)); err != nil {
		admin.Close()
		t.Fatalf("建 schema %s 失败：%v", schema, err)
	}

	// ② 在该 schema 上跑迁移。Migrate 自带连接池，search_path 由 DSN 透传，
	//    因此 migrations 里的 CREATE TABLE 只会落进本测试的 schema。
	isolated, err := platform.WithSearchPath(base, schema)
	if err != nil {
		admin.Close()
		t.Fatalf("拼隔离 DSN 失败：%v", err)
	}
	if err := platform.Migrate(ctx, isolated); err != nil {
		admin.Close()
		t.Fatalf("迁移失败：%v", err)
	}

	// ③ 业务连接池：同样带 search_path，被测仓储只看得见自己的 schema。
	pool, err := pgxpool.New(ctx, isolated)
	if err != nil {
		admin.Close()
		t.Fatalf("建业务连接池失败：%v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// DROP CASCADE：schema 里的表只属本测试，删干净是安全的。
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoteTestIdent(schema)+" CASCADE")
		admin.Close()
	})
	return pool
}

// testSchemaName 由测试名派生一个合法、可读、不超 63 字节的 schema 名。
//
// Postgres 标识符上限 63；测试名可能很长，故截断到 40 再拼序号。
func testSchemaName(t *testing.T) string {
	t.Helper()
	var sb strings.Builder
	for _, r := range strings.ToLower(t.Name()) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
	}
	name := "t_pgstore_" + sb.String()
	if len(name) > 40 {
		name = name[:40]
	}
	return fmt.Sprintf("%s_%d", name, schemaSeq.Add(1))
}

// quoteTestIdent 给标识符加双引号（schema 名由本文件生成，仅字母数字下划线）。
func quoteTestIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// newAnnouncementRepo 返回一个已迁移好、指向独立 schema 的 PG 公告仓储，
// 以及同一个 schema 的连接池（断言「库里到底几行」这类独立证据时要用）。
//
// now 用固定时刻：断言 created_at/updated_at 时需要可预测的值。
func newAnnouncementRepo(t *testing.T) (*pgstore.AnnouncementPGRepo, *pgxpool.Pool) {
	t.Helper()
	pool := newPGTestPool(t)
	if pool == nil {
		return nil, nil // 已 Skip
	}
	return pgstore.NewAnnouncementPGRepo(pool, fixedNow), pool
}

// fixedNow 是被注入的「当前时刻」，固定值让断言不依赖真实时间。
func fixedNow() time.Time {
	return time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
}

// ── 公告：基本 CRUD 与软删 ─────────────────────────────────────────

// TestAnnouncementPGRepo_CreateFindUpdate 覆盖：
// 创建回填 ID 与时间、audience_roles 的 INTEGER[] 往返、
// 编辑覆盖可写字段且保留 createdAt、软删后 Find/Update 都视为不存在。
func TestAnnouncementPGRepo_CreateFindUpdate(t *testing.T) {
	repo, _ := newAnnouncementRepo(t)
	if repo == nil {
		return
	}
	ctx := context.Background()

	publishedAt := time.Date(2026, 11, 1, 9, 0, 0, 123456000, time.UTC)
	a := &announcement.Announcement{
		Title:           "选课通知",
		Body:            "本周开始选课",
		Status:          announcement.StatusPublished,
		PublishedAt:     &publishedAt,
		Pinned:          true,
		PublishRevision: 1,
		Audience: announcement.Audience{
			Scope:   announcement.AudienceRole,
			Roles:   []int{0, 1},
			College: "",
			Grade:   "",
		},
	}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if a.ID <= 0 {
		t.Fatalf("Create 应回填 ID，实际 %d", a.ID)
	}
	if !a.CreatedAt.Equal(fixedNow()) || !a.UpdatedAt.Equal(fixedNow()) {
		t.Fatalf("Create 应写入注入时钟：createdAt=%v updatedAt=%v", a.CreatedAt, a.UpdatedAt)
	}

	got, err := repo.FindByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	// 微秒精度：这个 PublishedAt 的纳秒位正好是整微秒，往返应完全相等。
	if got.PublishedAt == nil || !got.PublishedAt.Equal(publishedAt) {
		t.Fatalf("publishedAt 往返失真：want %v got %v", publishedAt, got.PublishedAt)
	}
	// audience_roles 是 INTEGER[]，走 pgx 的数组编解码，必须保序保个数。
	if len(got.Audience.Roles) != 2 || got.Audience.Roles[0] != 0 || got.Audience.Roles[1] != 1 {
		t.Fatalf("audience_roles 往返失真：%v", got.Audience.Roles)
	}
	if got.Audience.Scope != announcement.AudienceRole {
		t.Fatalf("audience_scope 往返失真：%v", got.Audience.Scope)
	}
	if !got.Pinned || got.PublishRevision != 1 || got.Status != announcement.StatusPublished {
		t.Fatalf("标量字段往返失真：pinned=%v rev=%d status=%v", got.Pinned, got.PublishRevision, got.Status)
	}

	// 编辑：整行覆盖可写字段，createdAt 必须保持首次写入。
	createdAt := got.CreatedAt
	got.Title = "选课通知（已修订）"
	got.Pinned = false
	got.Audience = announcement.Audience{Scope: announcement.AudienceAll}
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	after, err := repo.FindByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("FindByID after Update: %v", err)
	}
	if after.Title != "选课通知（已修订）" || after.Pinned {
		t.Fatalf("Update 未生效：title=%q pinned=%v", after.Title, after.Pinned)
	}
	if !after.CreatedAt.Equal(createdAt) {
		t.Fatalf("Update 不应改 createdAt：want %v got %v", createdAt, after.CreatedAt)
	}
	// 从「按角色」改成「全员」后，roles 必须落成空数组（列是 NOT NULL）。
	if len(after.Audience.Roles) != 0 {
		t.Fatalf("audience_roles 应被清空，实际 %v", after.Audience.Roles)
	}

	// 软删：Find 视为不存在，返回 content.CodeNotFound。
	if err := repo.MarkDeleted(ctx, a.ID, fixedNow()); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	if _, err := repo.FindByID(ctx, a.ID); err == nil {
		t.Fatal("软删后 FindByID 应返回 NotFound")
	}
	// 幂等：再删一次仍成功（内存实现同样 return nil）。
	if err := repo.MarkDeleted(ctx, a.ID, fixedNow().Add(time.Hour)); err != nil {
		t.Fatalf("MarkDeleted 应幂等：%v", err)
	}
	// 已软删的行不可编辑。
	if err := repo.Update(ctx, got); err == nil {
		t.Fatal("软删后 Update 应返回 NotFound")
	}
	// 不存在的行：NotFound（与已删不区分失败文案）。
	if _, err := repo.FindByID(ctx, 999999); err == nil {
		t.Fatal("不存在的 id 应返回 NotFound")
	}
}

// ── 公告：排序与游标翻页 ──────────────────────────────────────────

// TestAnnouncementPGRepo_PinnedOrderCursorPaging 覆盖验收点：
//   - 排序 = 置顶优先、再 publishedAt 倒序、再 id 倒序；
//   - limit=1 逐页翻完，**不重复、不遗漏**；
//   - 置顶与非置顶交错时游标三元组不能丢 pinned 维度。
//
// 期望顺序不手写：直接用 announcement.LessForOrder 排序后比对，
// 这样「PG 排序」与「领域约定的排序」是同一份判据（实现漂移会立刻红）。
func TestAnnouncementPGRepo_PinnedOrderCursorPaging(t *testing.T) {
	repo, _ := newAnnouncementRepo(t)
	if repo == nil {
		return
	}
	ctx := context.Background()

	base := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	// 造 6 条：两条置顶（不同发布时间）、四条普通（其中两条同刻，考验 id 兜底）。
	mk := func(title string, pinned bool, at time.Time) *announcement.Announcement {
		t2 := at
		a := &announcement.Announcement{
			Title:           title,
			Body:            "正文",
			Status:          announcement.StatusPublished,
			PublishedAt:     &t2,
			Pinned:          pinned,
			PublishRevision: 1,
			Audience:        announcement.Audience{Scope: announcement.AudienceAll},
		}
		if err := repo.Create(ctx, a); err != nil {
			t.Fatalf("Create %s: %v", title, err)
		}
		return a
	}

	want := []*announcement.Announcement{
		mk("pinned-old", true, base.Add(1*time.Hour)),
		mk("pinned-new", true, base.Add(3*time.Hour)),
		mk("plain-newest", false, base.Add(2*time.Hour)),
		mk("plain-same-a", false, base.Add(30*time.Minute)),
		mk("plain-same-b", false, base.Add(30*time.Minute)),
		mk("plain-oldest", false, base.Add(10*time.Minute)),
	}
	// 用领域排序算出期望顺序（这是「真源」，不是从实现里重算的）。
	sorted := append([]*announcement.Announcement(nil), want...)
	sortAnnouncements(sorted)

	wantIDs := make([]int64, len(sorted))
	for i, a := range sorted {
		wantIDs[i] = a.ID
	}

	// 一页取全，验证排序。
	all, err := repo.ListPublished(ctx, announcement.PageCursor{}, 100)
	if err != nil {
		t.Fatalf("ListPublished: %v", err)
	}
	gotIDs := make([]int64, len(all))
	for i, a := range all {
		gotIDs[i] = a.ID
	}
	if fmt.Sprint(gotIDs) != fmt.Sprint(wantIDs) {
		t.Fatalf("排序不符：want %v got %v", wantIDs, gotIDs)
	}

	// limit=1 逐页翻完：不重复、不遗漏、顺序一致。
	var paged []int64
	seen := map[int64]bool{}
	cursor := announcement.PageCursor{}
	for i := 0; i < len(sorted)+2; i++ { // +2 用于确认「到底后返回空」
		page, err := repo.ListPublished(ctx, cursor, 1)
		if err != nil {
			t.Fatalf("翻页第 %d 次失败：%v", i, err)
		}
		if len(page) == 0 {
			break
		}
		last := page[len(page)-1]
		paged = append(paged, last.ID)
		if seen[last.ID] {
			t.Fatalf("游标翻页出现重复行 id=%d（第 %d 页）", last.ID, i)
		}
		seen[last.ID] = true
		cursor = announcement.PageCursor{
			Pinned:      last.Pinned,
			PublishedAt: announcement.PublishedOrZero(last),
			ID:          last.ID,
		}
	}
	if fmt.Sprint(paged) != fmt.Sprint(wantIDs) {
		t.Fatalf("翻页结果与一次取全不一致：paged %v want %v", paged, wantIDs)
	}
}

// TestAnnouncementPGRepo_ListPublishedExcludesUnpublished 覆盖：
// 读者侧只出 published；管理端出草稿与撤回；草稿的 published_at 为 NULL
// 时仍按「零值时间」参与排序（对应内存实现的 PublishedOrZero）。
func TestAnnouncementPGRepo_ListPublishedExcludesUnpublished(t *testing.T) {
	repo, _ := newAnnouncementRepo(t)
	if repo == nil {
		return
	}
	ctx := context.Background()

	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	pub := &announcement.Announcement{
		Title: "已发布", Body: "b", Status: announcement.StatusPublished,
		PublishedAt: &at, PublishRevision: 1,
		Audience: announcement.Audience{Scope: announcement.AudienceAll},
	}
	draft := &announcement.Announcement{
		Title: "草稿", Body: "b", Status: announcement.StatusDraft,
		Audience: announcement.Audience{Scope: announcement.AudienceAll},
	}
	revoked := &announcement.Announcement{
		Title: "已撤回", Body: "b", Status: announcement.StatusRevoked,
		PublishedAt: &at, PublishRevision: 1,
		Audience: announcement.Audience{Scope: announcement.AudienceAll},
	}
	for _, a := range []*announcement.Announcement{pub, draft, revoked} {
		if err := repo.Create(ctx, a); err != nil {
			t.Fatalf("Create %s: %v", a.Title, err)
		}
	}

	published, err := repo.ListPublished(ctx, announcement.PageCursor{}, 100)
	if err != nil {
		t.Fatalf("ListPublished: %v", err)
	}
	if len(published) != 1 || published[0].ID != pub.ID {
		t.Fatalf("读者侧应只含已发布的一条，实际 %d 条", len(published))
	}

	// 管理端：三条都在，且草稿（published_at IS NULL）排在最后。
	adm, err := repo.ListAll(ctx, announcement.PageCursor{}, 100)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(adm) != 3 {
		t.Fatalf("管理端应含 3 条（含草稿与撤回），实际 %d", len(adm))
	}
	if adm[len(adm)-1].ID != draft.ID {
		t.Fatalf("published_at 为 NULL 的草稿应排在最后，实际最后一条是 %q", adm[len(adm)-1].Title)
	}
	// 管理端也要能按游标续页（这是多出一段 UPDATE 分支：游标时间为零 → NULL）。
	rest, err := repo.ListAll(ctx, announcement.PageCursor{
		Pinned: adm[0].Pinned, PublishedAt: announcement.PublishedOrZero(adm[0]), ID: adm[0].ID,
	}, 100)
	if err != nil {
		t.Fatalf("ListAll 续页: %v", err)
	}
	if len(rest) != 2 {
		t.Fatalf("管理端续页应为 2 条，实际 %d", len(rest))
	}
}

// sortAnnouncements 用领域导出的 LessForOrder 排序（测试侧的真源判定）。
//
// 刻意不在这里写比较函数：排序是领域约定，测试也必须读同一份定义，
// 否则「PG 排序错了」和「测试期望也一起错了」会互相抵消。
func sortAnnouncements(list []*announcement.Announcement) {
	sortSlice(list, func(a, b *announcement.Announcement) bool {
		return announcement.LessForOrder(a, b)
	})
}

// sortSlice 是按「a 是否排在 b 之前」排序的通用小工具。
//
// 用泛型只为免掉两个测试文件各写一遍 sort.Slice 的样板；
// 比较函数仍由调用方从领域包导出函数取，不在这里实现业务排序。
func sortSlice[T any](list []T, less func(a, b T) bool) {
	sort.SliceStable(list, func(i, j int) bool { return less(list[i], list[j]) })
}

// countRows 跑一条 count 查询（用于「表里到底几行」这类独立证据）。
func countRows(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var n int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("count 查询失败（%s）：%v", sql, err)
	}
	return n
}
