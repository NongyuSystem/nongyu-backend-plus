package app_test

// issue #23 的装配层验收测试。
//
// seam：公共装配入口 app.New(cfg, logger)。测试通过「给它一个空 schema 的
// DATABASE_URL，再观察库里是否多出迁移表」来验证启动时迁移；通过捕获 logger
// 输出来验证启动日志说真话。都不深入 app 内部函数。
//
// 期望值来自独立事实源：migrations/*.sql 里的表名字面量 + 「日志不得写死 mock」
// 这条票面要求，不是从装配代码里重算出来的。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
)

// migrationTables 是 migrations/*.sql 声明的全部表（独立事实源：SQL 字面量）。
var migrationTables = []string{
	"audit_log", "login_methods", "sessions", "user_profiles", "users",
}

// appTestDSN 返回一个指向**全新空 schema** 的 DSN。
// 每个用例用独立 schema，避免与 platform 包的隔离 schema 或真实 public 互相干扰。
func appTestDSN(t *testing.T, name string) (dsn, schema string) {
	t.Helper()
	base := os.Getenv("DATABASE_URL")
	if base == "" {
		t.Skip("未设置 DATABASE_URL，跳过启动迁移集成测试")
	}
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
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		p, err := pgxpool.New(c, base)
		if err != nil {
			return
		}
		defer p.Close()
		_, _ = p.Exec(c, "DROP SCHEMA IF EXISTS "+name+" CASCADE")
	})

	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	dsn = base + sep + "search_path=" + name
	return dsn, name
}

// tablesInSchema 走 information_schema 数表，是被测行为的可观察结果。
func tablesInSchema(t *testing.T, dsn, schema string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("建查询连接失败：%v", err)
	}
	defer pool.Close()
	rows, err := pool.Query(ctx,
		`SELECT table_name FROM information_schema.tables
		  WHERE table_schema = $1 AND table_type = 'BASE TABLE'`, schema)
	if err != nil {
		t.Fatalf("查 information_schema 失败：%v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("扫表名失败：%v", err)
		}
		got = append(got, n)
	}
	return got
}

// completionRecord 从 JSON 日志里找出「装配完成」那条记录。
func completionRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("日志行不是合法 JSON：%q（%v）", line, err)
		}
		if rec["msg"] == "装配完成" {
			return rec
		}
	}
	t.Fatalf("未捕获到「装配完成」启动日志，实际输出：\n%s", buf.String())
	return nil
}

// TestAppNew_RunsMigrationsOnStartup 验收「在 app.New 里：配了 DATABASE_URL 时
// 启动时执行迁移」：空 schema 上装配成功后，迁移表必须已经就位（业务仓储才能用）。
func TestAppNew_RunsMigrationsOnStartup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	dsn, schema := appTestDSN(t, "t_app_startup_migrate")
	if before := tablesInSchema(t, dsn, schema); len(before) != 0 {
		t.Fatalf("前置条件失败：空 schema 不应有表，实际 %v", before)
	}

	cfg := config.Config{
		Addr: ":0", AppEnv: "test", ShutdownTimeoutSec: 1,
		TicketKey: "test-ticket-key", HTTPTimeoutSec: 3,
		DatabaseURL: dsn,
	}
	a, err := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("app.New 应成功：%v", err)
	}
	defer func() { _ = a.Shutdown(time.Second) }()

	got := tablesInSchema(t, dsn, schema)
	for _, want := range migrationTables {
		found := false
		for _, g := range got {
			if g == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("启动迁移后应存在表 %q，实际表集 %v", want, got)
		}
	}
}

// TestAppNew_StartupLogTellsTruth 验收「启动日志不再写死 mock，要反映真实装配」。
// 用 DATABASE_URL + REDIS_ADDR 全配齐，检查日志逐项说真话。
func TestAppNew_StartupLogTellsTruth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	dsn, _ := appTestDSN(t, "t_app_startup_log")
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	cfg := config.Config{
		Addr: ":0", AppEnv: "test", ShutdownTimeoutSec: 1,
		TicketKey: "test-ticket-key", HTTPTimeoutSec: 3,
		DatabaseURL: dsn,
		RedisAddr:   os.Getenv("REDIS_ADDR"),
	}
	if cfg.RedisAddr == "" {
		t.Skip("未设置 REDIS_ADDR，跳过「全真实装配」日志测试")
	}
	cfg.RedisPassword = os.Getenv("REDIS_PASSWORD")

	a, err := app.New(cfg, logger)
	if err != nil {
		t.Fatalf("app.New 应成功：%v", err)
	}
	defer func() { _ = a.Shutdown(time.Second) }()

	rec := completionRecord(t, &buf)
	raw := buf.String()

	// 1) 不得再写死 mock 作为 store 的值——这是票里点名的误导。
	if v, ok := rec["store"]; ok && fmt.Sprint(v) == "mock" {
		t.Fatalf("启动日志 store 不得写死 mock，实际 %v\n%s", v, raw)
	}
	// 2) 必须说清票据用的是 Redis（真实），身份/审计用的是 Postgres（真实）。
	if fmt.Sprint(rec["tickets"]) != "redis" {
		t.Fatalf("配了 REDIS_ADDR，日志 tickets 应为 redis，实际 %v\n%s", rec["tickets"], raw)
	}
	if fmt.Sprint(rec["store"]) != "postgres" {
		t.Fatalf("配了 DATABASE_URL，日志 store 应为 postgres，实际 %v\n%s", rec["store"], raw)
	}
}
