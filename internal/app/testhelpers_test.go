package app

// 本包（package app 内部测试）跨测试文件共用的测试替身与装配脚手架。
//
// fakeSchoolAuth / e2eDSN / newE2EApp 被 e2e_login_test.go 里三个端到端用例共用，
// 按「被 ≥2 个测试文件段使用就抽出来」的约定集中放在这里，
// 避免它们跟着某一个原文件走后其它用例引用不到（undefined）。

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/config"
	identitylogin "nongyu-backend-plus/internal/identity/login"
)

// fakeSchoolAuth 是假的学校认证源：按学号返回固定身份，模拟「客户端已完成学校登录」。
type fakeSchoolAuth struct {
	ident identitylogin.SchoolIdentity
	err   error
}

func (f fakeSchoolAuth) Authenticate(context.Context, identitylogin.SchoolCredential) (identitylogin.SchoolIdentity, error) {
	if f.err != nil {
		return identitylogin.SchoolIdentity{}, f.err
	}
	return f.ident, nil
}

// e2eDSN 建一个全新空 schema 并返回带 search_path 的 DSN。
// 本文件在 package app 内（需要访问 schoolForTest），因此不能复用 app_test 包的 appTestDSN。
func e2eDSN(t *testing.T, name string) string {
	t.Helper()
	base := os.Getenv("DATABASE_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatalf("建管理连接失败：%v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+name+" CASCADE"); err != nil {
		t.Fatalf("清理 schema 失败：%v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
		t.Fatalf("建 schema 失败：%v", err)
	}
	t.Cleanup(func() {
		c, cc := context.WithTimeout(context.Background(), 10*time.Second)
		defer cc()
		p, e := pgxpool.New(c, base)
		if e != nil {
			return
		}
		defer p.Close()
		_, _ = p.Exec(c, "DROP SCHEMA IF EXISTS "+name+" CASCADE")
	})
	sep := "?"
	if bytes.Contains([]byte(base), []byte("?")) {
		sep = "&"
	}
	return base + sep + "search_path=" + name
}

// newE2EApp 装配一个真实 App，并把学校认证源换成假实现。
func newE2EApp(t *testing.T, schema string, school identitylogin.SchoolAuthenticator) (*App, string) {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("未设置 DATABASE_URL，跳过端到端链路测试")
	}
	dsn := e2eDSN(t, schema)

	const authKey = "e2e-chain-auth-key-32-bytes-min!!"
	cfg := config.Config{
		Addr: ":0", AppEnv: "test", ShutdownTimeoutSec: 1,
		TicketKey: "e2e-ticket-key-32-bytes-min!!!!", HTTPTimeoutSec: 3,
		AuthKey: authKey, RateLimitPerMinute: 1000,
		DatabaseURL: dsn,
		HTTPEnforce: false,
	}

	old := schoolForTest
	schoolForTest = school
	t.Cleanup(func() { schoolForTest = old })

	a, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(time.Second) })
	return a, authKey
}
