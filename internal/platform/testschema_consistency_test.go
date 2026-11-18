package platform_test

// issue #23：三份 DDL 的一致性守护。
//
// 背景：仓库里有三份「建表形状」——
//   ① migrations/0001_audit_log.sql（权威，运维手工执行）
//   ② migrations/0002_identity.sql（权威，被 //go:embed 嵌入二进制）
//   ③ internal/platform/testschema_ddl.go 的 identityDDLStatements()（测试专用手写）
//
// ①② 是同一份权威源，本票已用 embed 让程序实际执行它们。③ 是测试隔离机制
// （每个包独立 schema）用的手写副本，票要求「不要改测试隔离机制」，故本测试
// **不改**它，而是把它产生的 schema 与权威迁移产生的 schema 做结构比对：
//   - 表集合、列集合、列类型、可空性、默认值、主键/唯一/检查/外键约束必须一致；
//   - 二级索引（CREATE INDEX）刻意排除：③ 一个都没建，而 ①② 建了 6 个，
//     这是**已知且被接受**的差异——索引只影响查询性能，不影响仓储行为正确性，
//     测试隔离 schema 不需要它们。报告里会明确写出这个差异。
//
// 这样将来有人改了迁移的列/约束却忘了同步手写副本（或反之），测试立刻变红，
// 不会让漂移悄悄扩大。

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/platform"
)

// parseAndStripSearchPath 去掉 DSN 里的 search_path，得到「原始库级 DSN」。
// TestMain 会往 DATABASE_URL 里塞本包隔离 schema，这里需要不带它的根 DSN
// 才能另建两个比对用的 schema。
func parseAndStripSearchPath(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("解析 DSN 失败：%w", err)
	}
	q := u.Query()
	q.Del("search_path")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// seqDefaultRe 匹配 nextval('<schema>.<seq>'::regclass) 里的 schema 前缀。
var seqDefaultRe = regexp.MustCompile(`'[^']*\.([a-zA-Z0-9_]+_seq)'::regclass`)

func normalizeDefault(def string) string {
	return seqDefaultRe.ReplaceAllString(def, `'$1'::regclass`)
}

// schemaQualifierRe 匹配 FK 定义里 REFERENCES <schema>.<table> 的 schema 前缀。
var schemaQualifierRe = regexp.MustCompile(`REFERENCES [a-zA-Z0-9_]+\.`)

func normalizeConstraint(def string) string {
	return schemaQualifierRe.ReplaceAllString(def, "REFERENCES ")
}

// schemaColumns 读一个 schema 里全部「表.列」的形状签名。
func schemaColumns(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT table_name, column_name, data_type, is_nullable,
		       COALESCE(column_default, ''), COALESCE(character_maximum_length::text, '')
		  FROM information_schema.columns
		 WHERE table_schema = $1
		 ORDER BY table_name, ordinal_position`, schema)
	if err != nil {
		t.Fatalf("查 %s 的列失败：%v", schema, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tbl, col, typ, nullable, def, maxlen string
		if err := rows.Scan(&tbl, &col, &typ, &nullable, &def, &maxlen); err != nil {
			t.Fatalf("扫列失败：%v", err)
		}
		// bigserial 的默认值是 nextval('<schema>.<seq>'::regclass)，里面带 schema 名，
		// 两个比对 schema 必然不同。把「任意 schema 前缀」归一掉，只留序列本身，
		// 否则会把「同名序列」误报成列定义不一致。
		def = normalizeDefault(def)
		out = append(out, strings.Join([]string{tbl, col, typ, nullable, def, maxlen}, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历列失败：%v", err)
	}
	return out
}

// schemaConstraints 读主键/唯一/检查/外键约束（不含索引），按名字排序。
func schemaConstraints(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT c.relname, con.conname, con.contype, pg_get_constraintdef(con.oid)
		  FROM pg_constraint con
		  JOIN pg_class c ON c.oid = con.conrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = $1 AND con.contype IN ('p','u','c','f')
		 ORDER BY c.relname, con.conname`, schema)
	if err != nil {
		t.Fatalf("查 %s 的约束失败：%v", schema, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var rel, name, typ, def string
		if err := rows.Scan(&rel, &name, &typ, &def); err != nil {
			t.Fatalf("扫约束失败：%v", err)
		}
		out = append(out, fmt.Sprintf("%s|%s|%s|%s", rel, name, typ, normalizeConstraint(def)))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历约束失败：%v", err)
	}
	sort.Strings(out)
	return out
}

// TestTestSchemaDDL_MatchesMigrationsStructurally 断言手写测试 DDL 与权威迁移
// 在「表 + 列形状 + 约束」上一致（索引差异见文件头说明，刻意排除）。
func TestTestSchemaDDL_MatchesMigrationsStructurally(t *testing.T) {
	base := os.Getenv("DATABASE_URL")
	if base == "" {
		t.Skip("未设置 DATABASE_URL，跳过 DDL 一致性集成测试")
	}
	// 本包 TestMain 已把 DATABASE_URL 改写为带 search_path 的隔离 DSN，
	// 这里需要**不带 search_path 的原始 DSN** 才能另建 schema。
	u, err := parseAndStripSearchPath(base)
	if err != nil {
		t.Fatalf("解析 DSN：%v", err)
	}
	root := u

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, root)
	if err != nil {
		t.Fatalf("建管理连接失败：%v", err)
	}
	defer pool.Close()

	const migSchema = "t_ddl_cmp_migrations"
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+migSchema+" CASCADE"); err != nil {
		t.Fatalf("清理 schema %s：%v", migSchema, err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+migSchema); err != nil {
		t.Fatalf("建 schema %s：%v", migSchema, err)
	}
	t.Cleanup(func() {
		c, cc := context.WithTimeout(context.Background(), 10*time.Second)
		defer cc()
		_, _ = pool.Exec(c, "DROP SCHEMA IF EXISTS "+migSchema+" CASCADE")
		_, _ = pool.Exec(c, "DROP SCHEMA IF EXISTS "+platform.TestSchemaName("nongyu-backend-plus/testpkg/ddlcompare")+" CASCADE")
	})

	sep := "?"
	if strings.Contains(root, "?") {
		sep = "&"
	}
	migDSN := root + sep + "search_path=" + migSchema
	if err := platform.Migrate(ctx, migDSN); err != nil {
		t.Fatalf("在比对 schema 上执行权威迁移失败：%v", err)
	}
	// 测试隔离机制：EnsureTestSchema 派生自己的 schema 名，这里用固定包名，
	// 但它会自己 CREATE SCHEMA；为可比，先删掉它即将用的那个名字再调。
	pkgPath := "nongyu-backend-plus/testpkg/ddlcompare"
	derived := platform.TestSchemaName(pkgPath)
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+derived+" CASCADE"); err != nil {
		t.Fatalf("清理派生 schema：%v", err)
	}
	testDSN, err := platform.EnsureTestSchema(ctx, root, pkgPath)
	if err != nil {
		t.Fatalf("EnsureTestSchema（手写测试 DDL）失败：%v", err)
	}
	if !strings.Contains(testDSN, derived) {
		t.Fatalf("EnsureTestSchema 应使用派生 schema %q，实际 DSN %q", derived, testDSN)
	}

	gotMigCols := schemaColumns(t, ctx, pool, migSchema)
	gotTestCols := schemaColumns(t, ctx, pool, derived)
	if strings.Join(gotMigCols, "\n") != strings.Join(gotTestCols, "\n") {
		t.Fatalf("手写测试 DDL 与权威迁移的【列形状】不一致：\n--- migrations ---\n%s\n--- testschema ---\n%s",
			strings.Join(gotMigCols, "\n"), strings.Join(gotTestCols, "\n"))
	}

	gotMigCons := schemaConstraints(t, ctx, pool, migSchema)
	gotTestCons := schemaConstraints(t, ctx, pool, derived)
	if strings.Join(gotMigCons, "\n") != strings.Join(gotTestCons, "\n") {
		t.Fatalf("手写测试 DDL 与权威迁移的【约束】不一致：\n--- migrations ---\n%s\n--- testschema ---\n%s",
			strings.Join(gotMigCons, "\n"), strings.Join(gotTestCons, "\n"))
	}

	// 显式钉住「已知差异」：权威迁移额外建 6 个二级索引（0001 三个 + 0002 三个），
	// 测试 DDL 一个都没建。只数「不属于任何约束」的索引，排除 PK/UNIQUE 自带索引，
	// 才是可读的 6 vs 0。索引只影响查询性能、不影响仓储行为正确性，测试隔离
	// schema 不需要它们，故为被接受的差异；数量变化只提示复核，不判失败。
	countSecondary := func(s string) int {
		var n int
		err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_indexes i
			 WHERE i.schemaname = $1
			   AND NOT EXISTS (
			     SELECT 1 FROM pg_constraint con
			      WHERE con.conname = i.indexname
			        AND con.connamespace = (SELECT oid FROM pg_namespace WHERE nspname = $1)
			   )`, s).Scan(&n)
		if err != nil {
			t.Fatalf("数 %s 的二级索引失败：%v", s, err)
		}
		return n
	}
	migIdx, testIdx := countSecondary(migSchema), countSecondary(derived)
	if migIdx != 6 || testIdx != 0 {
		t.Logf("提示：二级索引数量变化（migrations=%d, testschema=%d，基线 6/0），请复核报告里的「已知差异」结论", migIdx, testIdx)
	}
}
