package store_test

// PG 集成测试公共设施（原属 internal/platform/pgstore，按域拆分后各包自带一份）。
//
// 这几个 helper 原先在同一个 pgstore 包里跨文件共享；拆成多个独立 store/ 包后，
// 每个 store_test 包需要自己的一份。刻意不抽到公共包：抽了会让测试反向依赖
// 一个测试专用包，反而多一层不该有的耦合。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/platform/pg"
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
	isolated, err := pg.WithSearchPath(base, schema)
	if err != nil {
		admin.Close()
		t.Fatalf("拼隔离 DSN 失败：%v", err)
	}
	if err := pg.Migrate(ctx, isolated); err != nil {
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

// fixedNow 是被注入的「当前时刻」，固定值让断言不依赖真实时间。
func fixedNow() time.Time {
	return time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
}
