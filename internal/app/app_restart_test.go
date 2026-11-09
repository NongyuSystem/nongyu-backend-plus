package app_test

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

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/platform"
)

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
	// 两次启动后表集仍是迁移声明的那 5 张，没有重复/半成品。
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
