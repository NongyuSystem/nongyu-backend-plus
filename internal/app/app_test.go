package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/server"
)

// ---- from app_migrate_test.go ----
// issue #23 的装配层验收测试。
//
// seam：公共装配入口 app.New(cfg, logger)。测试通过「给它一个空 schema 的
// DATABASE_URL，再观察库里是否多出迁移表」来验证启动时迁移；通过捕获 logger
// 输出来验证启动日志说真话。都不深入 app 内部函数。
//
// 期望值来自独立事实源：migrations/*.sql 里的表名字面量 + 「日志不得写死 mock」
// 这条票面要求，不是从装配代码里重算出来的。

// migrationTables 是 migrations/*.sql 声明的全部表（独立事实源：SQL 字面量）。
var migrationTables = []string{
	"audit_log", "login_methods", "sessions",
	// 0004：学工系统（xsgz）完整档案四张表
	"student_family_members", "student_profile_groups",
	"student_profiles", "student_resumes",
	"user_profiles", "users",
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

// ---- from app_restart_test.go ----
// issue #23 / Spec 验收第 2 条「真实落库：启动服务、登录一次、重启进程，
// 用户/会话/票据仍存在」的实测确认。
//
// 口径（对 Lead 透明）：一次**真实 HTTP 登录**需要学校统一身份认证的凭据与
// 实时 ywtb 网络，离线环境无法复现（这不是本票要修的东西——票里已确认真实
// 数据路径接好，本票只做迁移 + 日志 + 实测确认）。因此这里验证的是「登录在
// 持久层留下的全部产物」能否跨进程重启存活：
//   - 用户 / 画像 / 登录方式 / 会话：经真实的 PGIdentityStore 写入 Postgres；
//   - 票据：经 app 自己装配的 Capabilities().Tickets（RedisTicketStore）写入 Redis。
//
// 「重启进程」用一次真实的装配生命周期模拟：app1.Shutdown() 关闭它持有的
// 全部连接池与后台 goroutine，再 app.New() 建一批**全新连接池**（并再次执行
// 启动迁移）。外部存储里的数据不受进程影响，而进程内存实现必然丢——这正是
// 验收判据要区分的两件事。app1 与 app2 之间不共享任何 Go 对象。
//
// 期望值来自独立事实源：写进去的学号/会话 ID/票据字节字面量，读回逐一比对。

// TestRealStorage_SurvivesProcessRestart 验证真实 Postgres + Redis 下，
// 登录产物（用户/画像/登录方式/会话/票据）跨一次进程重启仍然存在。
func TestRealStorage_SurvivesProcessRestart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	dsn, _ := appTestDSN(t, "t_app_restart")
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		t.Skip("未设置 REDIS_ADDR，跳过真实存储重启测试")
	}

	cfg := config.Config{
		Addr: ":0", AppEnv: "test", ShutdownTimeoutSec: 1,
		TicketKey: "test-ticket-key", HTTPTimeoutSec: 3,
		DatabaseURL:   dsn,
		RedisAddr:     redisAddr,
		RedisPassword: os.Getenv("REDIS_PASSWORD"),
		// 独立键前缀：与其它测试/真实数据隔离，收尾只删这一个键。
		RedisPrefix: "nongyu:t23restart:",
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// ── 进程 #1：启动（含启动迁移），模拟一次登录写入持久层 ──
	app1, err := app.New(cfg, logger)
	if err != nil {
		t.Fatalf("app1.New（首次启动/迁移）：%v", err)
	}

	const studentNo = "620231001"
	const sessionID = "sess-issue23-restart-0001"
	const ticketUserID = "ticket-user-issue23-restart"
	sealedTicket := []byte("sealed-tgt-bytes-for-issue23")
	expiresAt := time.Now().Add(9 * 24 * time.Hour).UTC().Truncate(time.Second)

	pg1, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsn})
	if err != nil {
		t.Fatalf("建 PG 连接（进程1）：%v", err)
	}
	u, err := pg1.Create(ctx, studentNo, platform.RoleUser)
	if err != nil {
		t.Fatalf("建用户：%v", err)
	}
	if err := pg1.Upsert(ctx, platform.Profile{
		UserID: u.ID, Name: "重启测试用户", College: "测试学院",
	}); err != nil {
		t.Fatalf("写画像：%v", err)
	}
	if _, err := pg1.Bind(ctx, platform.LoginMethod{
		UserID: u.ID, Kind: platform.LoginSchool, Identifier: studentNo,
	}); err != nil {
		t.Fatalf("绑登录方式：%v", err)
	}
	if err := pg1.CreateSession(ctx, platform.Session{
		ID: sessionID, UserID: u.ID, Scope: platform.ScopeApp,
		DeviceID: "device-issue23", ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatalf("建会话：%v", err)
	}
	_ = pg1.Close()

	// 票据走 app 自己装配的 Tickets（真实 Redis 实现）。
	if err := app1.Capabilities().Tickets.Put(ctx, ticketUserID, sealedTicket, expiresAt); err != nil {
		t.Fatalf("写票据（Redis）：%v", err)
	}

	// ── 重启：关闭进程 #1 的全部连接池/后台循环 ──
	if err := app1.Shutdown(2 * time.Second); err != nil {
		t.Fatalf("app1.Shutdown：%v", err)
	}

	// ── 进程 #2：全新装配（再次执行启动迁移，必须幂等不报错） ──
	app2, err := app.New(cfg, logger)
	if err != nil {
		t.Fatalf("app2.New（重启/二次迁移）应成功：%v", err)
	}
	defer func() { _ = app2.Shutdown(2 * time.Second) }()

	// 用**全新连接池**逐一读回，验证跨进程存活。
	pg2, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsn})
	if err != nil {
		t.Fatalf("建 PG 连接（进程2）：%v", err)
	}
	defer pg2.Close()

	gotUser, err := pg2.ByStudentNo(ctx, studentNo)
	if err != nil {
		t.Fatalf("重启后用户应仍存在：%v", err)
	}
	if gotUser.ID != u.ID || gotUser.StudentNo != studentNo {
		t.Fatalf("重启后用户数据不一致：%+v（原 ID=%d）", gotUser, u.ID)
	}
	gotProfile, err := pg2.ByUserID(ctx, u.ID)
	if err != nil {
		t.Fatalf("重启后画像应仍存在：%v", err)
	}
	if gotProfile.Name != "重启测试用户" {
		t.Fatalf("重启后画像姓名不符：%q", gotProfile.Name)
	}
	gotMethod, err := pg2.ByUserAndKind(ctx, u.ID, platform.LoginSchool)
	if err != nil {
		t.Fatalf("重启后登录方式应仍存在：%v", err)
	}
	if gotMethod.Identifier != studentNo {
		t.Fatalf("重启后登录方式标识不符：%q", gotMethod.Identifier)
	}
	gotSess, err := pg2.SessionByID(ctx, sessionID)
	if err != nil {
		t.Fatalf("重启后会话应仍存在：%v", err)
	}
	if gotSess.UserID != u.ID || gotSess.RevokedAt != nil {
		t.Fatalf("重启后会话不一致/被误吊销：%+v", gotSess)
	}

	// 票据：经进程 #2 全新装配的 Tickets 读回。
	gotSealed, gotExp, err := app2.Capabilities().Tickets.Get(ctx, ticketUserID)
	if err != nil {
		t.Fatalf("重启后票据应仍存在（Redis）：%v", err)
	}
	if !bytes.Equal(gotSealed, sealedTicket) {
		t.Fatalf("重启后票据字节不符：%x，期望 %x", gotSealed, sealedTicket)
	}
	if !gotExp.Equal(expiresAt) {
		t.Fatalf("重启后票据过期时刻不符：%v，期望 %v", gotExp, expiresAt)
	}

	// 收尾：删掉 Redis 票据键（schema 由 appTestDSN 的 Cleanup 删除）。
	_ = app2.Capabilities().Tickets.Delete(ctx, ticketUserID)
}

// TestAppNew_IdempotentAcrossRestart 补一条：启动迁移在「进程 #2 再跑一遍」
// 时不报错——它已由上面的重启用例隐式覆盖，这里显式断言日志与二次启动都干净，
// 让「部署不必手工敲 SQL」这条验收有独立可读的凭据。
func TestAppNew_IdempotentAcrossRestart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	dsn, schema := appTestDSN(t, "t_app_restart_idem")
	cfg := config.Config{
		Addr: ":0", AppEnv: "test", ShutdownTimeoutSec: 1,
		TicketKey: "test-ticket-key", HTTPTimeoutSec: 3,
		DatabaseURL: dsn,
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	for i := 1; i <= 2; i++ {
		a, err := app.New(cfg, logger)
		if err != nil {
			t.Fatalf("第 %d 次启动（迁移）应成功：%v", i, err)
		}
		_ = a.Shutdown(2 * time.Second)
	}
	// 两次启动后表集仍是迁移声明的全部表，没有重复/半成品。
	got := tablesInSchema(t, dsn, schema)
	if len(got) != len(migrationTables) {
		t.Fatalf("两次启动迁移后表数应为 %d，实际 %v", len(migrationTables), got)
	}
	for _, want := range migrationTables {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("两次启动迁移后缺表 %q，实际 %v", want, got)
		}
	}
}

// ---- from auth_401_200_test.go ----
// #25 任务 3 / #21 判据：受保护端点的 401/200 必须经**真实装配**验证。
//
// seam：app.New(cfg) 装配出的 http.Handler + HTTP 状态码。
// 期望值来自票正文：「无令牌访问受保护端点 → 401；有有效令牌 → 200」。
// 不是只测中间件函数本身——那样测不出装配顺序（Use 与路由注册的先后）。

// newAuthedApp 用独立 schema + 真实鉴权密钥装配一个 App，并返回同一个
// schema 下的身份存储（用于造会话），保证 App 与测试看到同一份数据。
func newAuthedApp(t *testing.T, schema string) (*app.App, *platform.PGIdentityStore) {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("未设置 DATABASE_URL，跳过装配层鉴权集成测试")
	}
	dsn, _ := appTestDSN(t, schema)
	cfg := config.Config{
		Addr: ":0", AppEnv: "test", ShutdownTimeoutSec: 1,
		TicketKey: "test-ticket-key-32-bytes-min!!", HTTPTimeoutSec: 3,
		AuthKey: testAuthKey, RateLimitPerMinute: 600,
		DatabaseURL: dsn,
		HTTPEnforce: false,
	}
	a, err := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(time.Second) })

	store, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsn})
	if err != nil {
		t.Fatalf("NewPGIdentityStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return a, store
}

const testAuthKey = "test-auth-key-for-401-200-check"

// 无令牌访问受保护端点 → 401。
func TestProtectedEndpoint_NoTokenIs401(t *testing.T) {
	a, _ := newAuthedApp(t, "app_auth_no_token")

	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/identity/me", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌访问 /identity/me 应 401，实际 %d body=%s", w.Code, w.Body.String())
	}
}

// 有效令牌访问受保护端点 → 200（不是 401，也不是 5xx）。
func TestProtectedEndpoint_ValidTokenIs200(t *testing.T) {
	a, store := newAuthedApp(t, "app_auth_valid_token")
	ctx := context.Background()

	// 建用户 + 画像 + 一条有效会话（全部用同一个 schema）
	u, err := store.Create(ctx, "888100031", platform.RoleUser)
	if err != nil {
		t.Fatalf("Create user: %v", err)
	}
	if err := store.Upsert(ctx, platform.Profile{
		UserID: u.ID, Name: "鉴权检查用户", College: "信息工程学院",
	}); err != nil {
		t.Fatalf("Upsert profile: %v", err)
	}
	now := time.Now()
	sess := platform.Session{
		ID: "app-auth-valid-token-session", UserID: u.ID,
		Scope: platform.ScopeApp, DeviceID: "dev-auth-check",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// 用装配层同一把密钥签发令牌
	verifier := server.NewTokenVerifier(testAuthKey, platform.NewRealClock())
	token := verifier.IssueSession(sess.ID, sess.ExpiresAt)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/identity/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	a.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("有效令牌访问 /identity/me 应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	// 体里应是该用户的画像（不能是空/别人的）
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应：%v", err)
	}
	if got, _ := body["name"].(string); got != "鉴权检查用户" {
		t.Fatalf("响应应为该用户画像，实际 %v", body)
	}
}

// ---- from auth_dev_test.go ----
// #21 M3：dev 下 AUTH_KEY 为空时，必须显式开关才允许「无认证」装配。
// seam 是公共入口 app.New；期望值来自票正文验收判据。
// 不触碰 config.Load()，因此不会打断「无环境变量调用 Load()」的既有测试。

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAppNew_ProdWithoutAuthKeyRequiresExplicitInsecureSwitch(t *testing.T) {
	base := config.Config{
		Addr: ":0", AppEnv: "prod", ShutdownTimeoutSec: 10,
		TicketKey: "test-key", HTTPTimeoutSec: 5,
		RateLimitPerMinute: 60,
		HTTPEnforce:        false, // 避免真实出站
	}

	// 未开开关 → 拒绝装配，且错误要说清原因
	_, err := app.New(base, testLogger())
	if err == nil {
		t.Fatal("prod 下 AUTH_KEY 为空且未开 ALLOW_INSECURE_DEV 时，app.New 必须拒绝")
	}
	if !contains(err.Error(), "AUTH_KEY") {
		t.Fatalf("错误信息应提到 AUTH_KEY，实际 %q", err.Error())
	}

	// 显式开开关 → 允许装配（并打 WARN，这里只验不报错）
	withSwitch := base
	withSwitch.AllowInsecureDev = true
	a, err := app.New(withSwitch, testLogger())
	if err != nil {
		t.Fatalf("显式开 ALLOW_INSECURE_DEV 后应允许装配，实际 %v", err)
	}
	_ = a
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ---- from auth_env_test.go ----
// code-review M1/M2：环境判定必须覆盖 staging，且默认无 .env 能起健康检查服务。
// seam = config.Load / app.New 公共入口。

func serveGet(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// M1：staging + AUTH_KEY 空且未开开关 → 必须拒绝（不能 fail-open）。
func TestAppNew_StagingWithoutAuthKeyIsRejected(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	t.Setenv("AUTH_KEY", "")
	t.Setenv("ALLOW_INSECURE_DEV", "false")
	t.Setenv("TICKET_KEY", "test-ticket-key-32-bytes-min!!")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	_, err = app.New(cfg, quietLogger())
	if err == nil {
		t.Fatal("staging 下 AUTH_KEY 为空且未开 ALLOW_INSECURE_DEV 时，app.New 必须拒绝（fail-closed）")
	}
}

// M2：默认无 .env（APP_ENV=dev、AUTH_KEY 空）时，app.New 必须能起健康检查服务，
// 与 config.go:19 / README 的契约一致。
func TestAppNew_DefaultDevCanStartHealthService(t *testing.T) {
	// 清掉可能污染的环境变量，模拟「无 .env 直接 go run」
	for _, k := range []string{"APP_ENV", "AUTH_KEY", "ALLOW_INSECURE_DEV", "DATABASE_URL", "REDIS_ADDR", "HTTP_ALLOWLIST"} {
		t.Setenv(k, "")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.AppEnv != "dev" {
		t.Fatalf("默认 AppEnv 应为 dev，实际 %q", cfg.AppEnv)
	}
	a, err := app.New(cfg, quietLogger())
	if err != nil {
		t.Fatalf("默认无 .env 应能起健康检查服务，实际 %v", err)
	}
	defer a.Shutdown(time.Second)

	// /healthz 可达
	w := serveGet(a.Handler(), "/healthz")
	if w.Code != 200 {
		t.Fatalf("/healthz 应 200，实际 %d", w.Code)
	}
}

// ---- from public_paths_test.go ----
// code-review M5/N6：公开路径必须是单一来源，三处中间件口径一致。
// 特别覆盖：装配后 /status 免认证可达、POST /identity/login 无尾斜杠不被 401 拦。

func TestPublicPaths_SingleSourceCoversStatusAndLogin(t *testing.T) {
	// 单一来源必须认得这几个路径
	for _, p := range []string{"/healthz", "/status", "/identity/login", "/identity/login/admin", "/identity/mailcode/send", "/identity/register"} {
		if !server.IsPublicPath(p) {
			t.Fatalf("公开路径 %q 应被 IsPublicPath 认得", p)
		}
	}
	if server.IsPublicPath("/identity/me") {
		t.Fatal("受保护端点 /identity/me 不得被认作公开路径")
	}
}

func TestAppNew_StatusReachableWithoutAuth(t *testing.T) {
	// 退化装配：AUTH_KEY 非空但无 DATABASE_URL → AuthMiddleware 路径，
	// 之前只放行 /healthz，/status 会被 401。
	cfg := config.Config{
		Addr: ":0", AppEnv: "test", ShutdownTimeoutSec: 1,
		TicketKey: "test-ticket-key-32-bytes-min!!", HTTPTimeoutSec: 3,
		AuthKey: "test-auth-key", RateLimitPerMinute: 60,
		HTTPEnforce: false,
	}
	a, err := app.New(cfg, quietLogger())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Shutdown(time.Second)

	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/status 应免认证可达（200），实际 %d", w.Code)
	}
}

func TestAppNew_LoginEndpointNotBlockedByAuth(t *testing.T) {
	cfg := config.Config{
		Addr: ":0", AppEnv: "test", ShutdownTimeoutSec: 1,
		TicketKey: "test-ticket-key-32-bytes-min!!", HTTPTimeoutSec: 3,
		AuthKey: "test-auth-key", RateLimitPerMinute: 60,
		HTTPEnforce: false,
	}
	a, err := app.New(cfg, quietLogger())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Shutdown(time.Second)

	// POST /identity/login 不带令牌：不应被鉴权拦成 401
	// （真正的校验在 handler 里，这里只看中间件是否误拦）
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/identity/login", nil)
	req.Header.Set("Content-Type", "application/json")
	a.Handler().ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("登录端点不得被全局鉴权拦成 401（公开路径漏判无尾斜杠）")
	}
}

// ---- from status_test.go ----
// #21 M6（Spec #18）：观测出口必须反映真实依赖状态，不是硬编码 ok。
// seam = HTTP 端点 /status；期望值来自真实 Ping 结果。

func TestStatusEndpoint_ReportsRealDependencyState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	// Postgres 探针故意失败、Redis 探针成功：端点必须如实反映，不能一律 ok。
	probe := app.StatusProbe{
		Postgres: func() bool { return false },
		Redis:    func() bool { return true },
		Breaker:  func() string { return "closed" },
		Storage:  func() string { return "postgres" },
	}
	r := gin.New()
	app.RegisterStatus(r, probe)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/status 应 200，实际 %d", w.Code)
	}
	var body app.StatusReport
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if body.Postgres != "down" {
		t.Fatalf("Postgres 不可达时应报 down，实际 %q", body.Postgres)
	}
	if body.Redis != "up" {
		t.Fatalf("Redis 可达时应报 up，实际 %q", body.Redis)
	}
	if body.Breaker != "closed" {
		t.Fatalf("熔断器状态应如实上报，实际 %q", body.Breaker)
	}
	if body.Process != "up" {
		t.Fatalf("进程存活应恒为 up，实际 %q", body.Process)
	}
}
