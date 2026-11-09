package platform

// 集成测试的数据库隔离。
//
// 问题：多个测试包并行运行时共用同一个 Postgres，会互相看到/删除对方的数据，
// 表现为「单独跑通过、连跑失败」。实测踩过：identity 包与 flow 包用同一张 users 表。
//
// 方案：每个测试包用**独立 schema**。schema 名由包名派生（如 `t_identity`），
// 包启动时创建、跑完清理自己的行。这样：
//   - 并行安全：不同包写不同 schema；
//   - 无需多套数据库实例；
//   - 生产 DSN 不变，只在测试里改 search_path。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// TestSchemaName 由包名派生一个合法且可读的 schema 名。
//
// 例："nongyu-backend-plus/internal/identity" → "t_internal_identity_ab12cd"
// 后缀取哈希前 6 位，避免过长的包路径碰撞。
func TestSchemaName(pkgPath string) string {
	clean := strings.TrimPrefix(pkgPath, "nongyu-backend-plus/")
	clean = strings.TrimPrefix(clean, "internal/")
	replacer := strings.NewReplacer("/", "_", "-", "_", ".", "_")
	clean = replacer.Replace(clean)

	sum := sha256.Sum256([]byte(pkgPath))
	suffix := hex.EncodeToString(sum[:])[:6]

	name := "t_" + clean + "_" + suffix
	// Postgres 标识符上限 63；超出则截断保留后缀。
	if len(name) > 63 {
		keep := 63 - len(suffix) - 2
		if keep < 1 {
			keep = 1
		}
		name = name[:keep] + "_" + suffix
	}
	return strings.ToLower(name)
}

// WithSearchPath 往 DSN 里加上 search_path，把连接限定到指定 schema。
//
// 支持 `postgres://...` 形式的 URL DSN。
func WithSearchPath(dsn, schema string) (string, error) {
	if schema == "" {
		return dsn, nil
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("platform: DSN 解析失败：%w", err)
	}
	q := u.Query()
	// libpq 的 search_path 选项；pgx 会透传。
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// EnsureTestSchema 创建 schema 并返回一个指向它的新 DSN。
//
// 调用方应在测试包初始化时执行一次，用完不必删除（下次运行复用，
// 但每个包的测试自己负责清掉自己的行）。
func EnsureTestSchema(ctx context.Context, dsn, pkgPath string) (string, error) {
	admin, err := NewPGIdentityStore(PGOptions{DSN: dsn})
	if err != nil {
		return "", err
	}
	defer admin.Close()

	schema := TestSchemaName(pkgPath)
	// 建 schema 与建表都在这个 schema 下：用 SET search_path 的方式逐条执行。
	if err := admin.ExecDDL(ctx, "CREATE SCHEMA IF NOT EXISTS "+quoteIdent(schema)); err != nil {
		return "", err
	}
	// 首次使用时把四张表也建到这个 schema 里（幂等）。
	if err := admin.ExecDDLInSchema(ctx, schema, identityDDLStatements()); err != nil {
		return "", err
	}
	return WithSearchPath(dsn, schema)
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
