package platform_test

// issue #23 / Spec 验收第 3 条（迁移可执行且幂等）的验收测试。
//
// seam：公共能力 platform.Migrate(ctx, dsn)。测试只通过这个入口驱动「迁移」这一
// 外部能力，不读取 migrations/ 文件、不调用内部函数——实现换成版本表也能存活。
//
// 期望值来自独立事实源：migrations/ 下两份 SQL 里的**表名字面量**（0001 audit_log /
// 0002 users、user_profiles、login_methods、sessions），不是从被实现代码里重算出来的。
// 观察通道是 information_schema（迁移的全部语义就是「库里多了这些表」），
// 用 pgxpool 直连查询，与被实现的 Migrate 内部如何拼 SQL 无关。
//
// 环境：TestMain 会把 DATABASE_URL 改写为本包专属 schema（testmain_test.go），
// 因此这里的「空库」是「本包独立 schema 里的空表集」，与其它包并行互不干扰。
// 未设置 DATABASE_URL 时跳过（纯单测环境不依赖 Postgres）。

import (
	"context"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/platform"
)

// migrationTables 是 migrations/*.sql 里声明的全部表名（独立事实源：SQL 字面量）。
var migrationTables = []string{
	"audit_log",
	"login_methods",
	"sessions",
	"user_profiles",
	"users",
}

// migrationDSN 返回本包隔离后的 DSN（TestMain 已改写 DATABASE_URL）。
func migrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过 Postgres 迁移集成测试")
	}
	return dsn
}

// migrationSchema 从隔离 DSN 的 search_path 参数读出本包的 schema 名。
func migrationSchema(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("解析 DSN 失败：%v", err)
	}
	s := u.Query().Get("search_path")
	if s == "" {
		t.Fatalf("隔离 DSN 应带 search_path，实际 %q", dsn)
	}
	return s
}

// migrationPool 建一条独立的直连（与被实现的 Migrate 内部连接池无关）。
func migrationPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("建直连失败：%v", err)
	}
	return pool
}

// dropMigrationTables 清空本包 schema 里的迁移表，模拟「空库」。
// 用 CASCADE 一次带走依赖对象；这些表只由本包测试使用，删干净是安全且必要的。
func dropMigrationTables(t *testing.T, dsn string) {
	t.Helper()
	pool := migrationPool(t, dsn)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, name := range migrationTables {
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+name+" CASCADE"); err != nil {
			t.Fatalf("清理表 %s 失败：%v", name, err)
		}
	}
}

// existingMigrationTables 返回当前 schema 里已存在的迁移表名（升序）。
// 走 information_schema 系统目录，是被测迁移行为的可观察结果，不依赖实现内部。
func existingMigrationTables(t *testing.T, dsn, schema string) []string {
	t.Helper()
	pool := migrationPool(t, dsn)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := pool.Query(ctx,
		`SELECT table_name FROM information_schema.tables
		  WHERE table_schema = $1 AND table_type = 'BASE TABLE'`, schema)
	if err != nil {
		t.Fatalf("查 information_schema 失败：%v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("扫表名失败：%v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表名失败：%v", err)
	}
	sort.Strings(got)
	return got
}

// TestMigrate_BuildsTablesOnEmptySchema 是验收第 3 条的前半：
// 在空库（本包独立 schema、迁移表全不存在）上执行一次 Migrate，
// migrations/ 里声明的全部表必须被建出来。
func TestMigrate_BuildsTablesOnEmptySchema(t *testing.T) {
	dsn := migrationDSN(t)
	schema := migrationSchema(t, dsn)
	dropMigrationTables(t, dsn)

	if before := existingMigrationTables(t, dsn, schema); len(before) != 0 {
		t.Fatalf("前置条件失败：空库不应有迁移表，实际 %v", before)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := platform.Migrate(ctx, dsn); err != nil {
		t.Fatalf("空库首次 Migrate 应成功：%v", err)
	}

	got := existingMigrationTables(t, dsn, schema)
	for _, want := range migrationTables {
		if !contains(got, want) {
			t.Fatalf("Migrate 后应存在表 %q，实际表集 %v", want, got)
		}
	}
}

// TestMigrate_IdempotentOnSecondRun 是验收第 3 条的后半：
// 连续执行第二次 Migrate 不报错，且表集不变——「部署不必手工敲 SQL」的前提。
// 还在两次执行之间写入一条数据，确认幂等重跑不会把已有数据抹掉。
func TestMigrate_IdempotentOnSecondRun(t *testing.T) {
	dsn := migrationDSN(t)
	schema := migrationSchema(t, dsn)
	dropMigrationTables(t, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := platform.Migrate(ctx, dsn); err != nil {
		t.Fatalf("首次 Migrate：%v", err)
	}

	// 在两次迁移之间放一条用户数据：幂等重跑不得丢数据。
	store, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsn})
	if err != nil {
		t.Fatalf("建连接失败：%v", err)
	}
	const studentNo = "620230001"
	if _, err := store.Create(ctx, studentNo, platform.RoleUser); err != nil {
		t.Fatalf("写入测试用户失败：%v", err)
	}
	// 用例结束删掉这条数据，不给同 schema 的其它测试留残留（表本身保留，
	// 它们与 EnsureTestSchema 建的形态一致，后续测试可继续用）。
	t.Cleanup(func() {
		s, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsn})
		if err != nil {
			return
		}
		defer s.Close()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if u, err := s.ByStudentNo(c, studentNo); err == nil {
			_ = s.DeleteUser(c, u.ID)
		}
	})
	_ = store.Close()

	// 第二次执行：不报错。
	if err := platform.Migrate(ctx, dsn); err != nil {
		t.Fatalf("第二次 Migrate 应幂等不报错：%v", err)
	}

	got := existingMigrationTables(t, dsn, schema)
	if strings.Join(got, ",") != strings.Join(migrationTables, ",") {
		t.Fatalf("幂等重跑后表集应与首次一致，期望 %v，实际 %v", migrationTables, got)
	}

	// 数据仍在：确认幂等不是靠「删表重建」实现的。
	store2, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsn})
	if err != nil {
		t.Fatalf("建复查连接失败：%v", err)
	}
	defer store2.Close()
	if _, err := store2.ByStudentNo(ctx, studentNo); err != nil {
		t.Fatalf("幂等重跑后用户应仍存在：%v", err)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
