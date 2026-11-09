package platform_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
)

func TestTestSchemaName_DeterministicAndValid(t *testing.T) {
	a := platform.TestSchemaName("nongyu-backend-plus/internal/identity")
	b := platform.TestSchemaName("nongyu-backend-plus/internal/identity")
	if a != b {
		t.Fatalf("同包名应得到同名 schema：%q vs %q", a, b)
	}
	if len(a) > 63 {
		t.Fatalf("schema 名不得超过 63：%q (%d)", a, len(a))
	}
	if strings.ContainsAny(a, "/-.") {
		t.Fatalf("schema 名不得含路径符号：%q", a)
	}
	if !strings.HasPrefix(a, "t_") {
		t.Fatalf("应以 t_ 开头：%q", a)
	}

	// 不同包必须得到不同 schema——这是隔离的前提。
	other := platform.TestSchemaName("nongyu-backend-plus/internal/identity/flow")
	if other == a {
		t.Fatalf("不同包应得到不同 schema：%q", a)
	}
}

func TestWithSearchPath(t *testing.T) {
	got, err := platform.WithSearchPath("postgres://u:p@h:5432/db?sslmode=disable", "t_x")
	if err != nil {
		t.Fatalf("WithSearchPath: %v", err)
	}
	if !strings.Contains(got, "search_path=t_x") && !strings.Contains(got, "search_path=t%5Fx") {
		t.Fatalf("应带上 search_path，实际 %q", got)
	}
	if !strings.Contains(got, "sslmode=disable") {
		t.Fatalf("原有参数不应丢失，实际 %q", got)
	}
}

// 真的建 schema 并验证隔离：同一条学号在两个 schema 里互不冲突。
func TestEnsureTestSchema_IsolatesData(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过 Postgres 集成测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dsnA, err := platform.EnsureTestSchema(ctx, dsn, "nongyu-backend-plus/testpkg/a")
	if err != nil {
		t.Fatalf("EnsureTestSchema(a): %v", err)
	}
	dsnB, err := platform.EnsureTestSchema(ctx, dsn, "nongyu-backend-plus/testpkg/b")
	if err != nil {
		t.Fatalf("EnsureTestSchema(b): %v", err)
	}
	if dsnA == dsnB {
		t.Fatal("两个包的 DSN 必须不同（隔离的前提）")
	}

	storeA, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsnA})
	if err != nil {
		t.Fatalf("storeA: %v", err)
	}
	defer storeA.Close()
	storeB, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsnB})
	if err != nil {
		t.Fatalf("storeB: %v", err)
	}
	defer storeB.Close()

	const no = "555000001"
	// 先在 A 里删掉可能残留
	if u, err := storeA.ByStudentNo(ctx, no); err == nil {
		_ = storeA.SetStatus(ctx, u.ID, platform.UserActive)
	}
	if u, err := storeB.ByStudentNo(ctx, no); err == nil {
		_ = storeB.SetStatus(ctx, u.ID, platform.UserActive)
	}

	// 两个 schema 各建一条同学号用户，不应冲突。
	if _, err := storeA.Create(ctx, no, platform.RoleUser); err != nil && !platform.IsAlreadyExists(err) {
		t.Fatalf("storeA.Create: %v", err)
	}
	if _, err := storeB.Create(ctx, no, platform.RoleUser); err != nil && !platform.IsAlreadyExists(err) {
		t.Fatalf("storeB.Create（同学号不应冲突）: %v", err)
	}

	// 在 A 里删除，不应影响 B。
	ua, err := storeA.ByStudentNo(ctx, no)
	if err != nil {
		t.Fatalf("storeA.ByStudentNo: %v", err)
	}
	if err := storeA.DeleteUser(ctx, ua.ID); err != nil {
		t.Fatalf("storeA.DeleteUser: %v", err)
	}
	if _, err := storeA.ByStudentNo(ctx, no); !platform.IsNotFound(err) {
		t.Fatalf("A 里应已删除，实际 %v", err)
	}
	if _, err := storeB.ByStudentNo(ctx, no); err != nil {
		t.Fatalf("B 里的数据不应被 A 的删除影响，实际 %v", err)
	}
}
