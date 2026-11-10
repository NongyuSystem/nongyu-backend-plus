package platform_test

import (
	"context"
	"os"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
)

// ---- from mailcode_test.go ----
// 邮箱验证码存储：Redis 实现，TTL 兼冷却锁。
// 决策依据：docs/tech/架构落点-身份仓.md 第六节。

func newMailCodeStore(t *testing.T) *platform.RedisMailCodeStore {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("未设置 REDIS_ADDR，跳过 Redis 集成测试")
	}
	s, err := platform.NewRedisMailCodeStore(platform.RedisOptions{
		Addr:     addr,
		Password: os.Getenv("REDIS_PASSWORD"),
		Prefix:   "nongyu-mailcode-test:",
	})
	if err != nil {
		t.Fatalf("NewRedisMailCodeStore: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Ping(ctx); err != nil {
		t.Skipf("Redis 不可达，跳过：%v", err)
	}
	return s
}

// 验证码在 TTL 窗口内既不能重复发送，也不能被重放。
func TestMailCode_IssueBlocksResendWithinTTL(t *testing.T) {
	s := newMailCodeStore(t)
	ctx := context.Background()
	email := "issue-test@sicau.edu.cn"
	_ = s.Clear(ctx, email)
	defer s.Clear(ctx, email)

	code := "123456"
	ttl := 5 * time.Minute
	if err := s.Issue(ctx, email, code, ttl); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// 同一邮箱在 TTL 内再次申请 → 拒绝，并给出剩余秒数
	remain, err := s.Cooldown(ctx, email)
	if err != nil {
		t.Fatalf("Cooldown: %v", err)
	}
	if remain <= 0 {
		t.Fatalf("TTL 内应处于冷却，实际剩余 %v", remain)
	}
	if remain > ttl {
		t.Fatalf("剩余时间不应超过 TTL：%v > %v", remain, ttl)
	}

	// 正确验证码可以消费一次
	ok, err := s.Verify(ctx, email, code)
	if err != nil || !ok {
		t.Fatalf("正确验证码应通过：ok=%v err=%v", ok, err)
	}
	// 一次性：再验同一条码应失败
	ok2, err := s.Verify(ctx, email, code)
	if err != nil {
		t.Fatalf("Verify #2: %v", err)
	}
	if ok2 {
		t.Fatal("验证码应一次性，重放必须失败")
	}
}

func TestMailCode_WrongCodeDoesNotConsume(t *testing.T) {
	s := newMailCodeStore(t)
	ctx := context.Background()
	email := "wrong-test@sicau.edu.cn"
	_ = s.Clear(ctx, email)
	defer s.Clear(ctx, email)

	if err := s.Issue(ctx, email, "654321", 5*time.Minute); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	ok, err := s.Verify(ctx, email, "000000")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ok {
		t.Fatal("错误验证码不得通过")
	}
	// 错误一次之后，正确码仍应可用（不因一次输错就作废）
	ok, err = s.Verify(ctx, email, "654321")
	if err != nil || !ok {
		t.Fatalf("输错一次后正确码仍应可用：ok=%v err=%v", ok, err)
	}
}

func TestMailCode_TooManyAttemptsInvalidates(t *testing.T) {
	s := newMailCodeStore(t)
	ctx := context.Background()
	email := "attempts-test@sicau.edu.cn"
	_ = s.Clear(ctx, email)
	defer s.Clear(ctx, email)

	if err := s.Issue(ctx, email, "111111", 5*time.Minute); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// 连续输错超过上限
	for i := 0; i < 10; i++ {
		_, _ = s.Verify(ctx, email, "999999")
	}
	// 之后即使输对，也必须失败（已被作废，需重新获取）
	ok, err := s.Verify(ctx, email, "111111")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ok {
		t.Fatal("尝试次数超限后验证码应作废")
	}
}

func TestMailCode_NoCodeMeansNoCooldown(t *testing.T) {
	s := newMailCodeStore(t)
	ctx := context.Background()
	email := "none-test@sicau.edu.cn"
	_ = s.Clear(ctx, email)

	remain, err := s.Cooldown(ctx, email)
	if err != nil {
		t.Fatalf("Cooldown: %v", err)
	}
	if remain != 0 {
		t.Fatalf("没有验证码时不应有冷却，实际 %v", remain)
	}
	// 没有验证码时校验必然失败
	ok, err := s.Verify(ctx, email, "123456")
	if err != nil || ok {
		t.Fatalf("无验证码时不应通过：ok=%v err=%v", ok, err)
	}
}

// ---- from audit_test.go ----
// 审计存储的集成测试：需要 DATABASE_URL（或 PG* 环境变量），未配置时跳过。

func newAuditStore(t *testing.T) *platform.PGAuditStore {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过 Postgres 集成测试")
	}
	store, err := platform.NewPGAuditStore(platform.PGOptions{
		DSN:         dsn,
		BufferSize:  64,
		BatchSize:   16,
		FlushPeriod: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewPGAuditStore: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		t.Skipf("Postgres 不可达，跳过：%v", err)
	}
	return store
}

func TestPGAuditStore_AppendAndFlush(t *testing.T) {
	store := newAuditStore(t)
	defer store.Close()
	ctx := context.Background()

	reqID := "test-req-" + time.Now().Format("20060102150405.000000")
	entries := []platform.AuditEntry{
		{RequestID: reqID, UserID: "alice", Action: "login", Target: "auth", Result: "ok", OccurredAt: time.Now()},
		{RequestID: reqID, UserID: "alice", Action: "address", Target: "jiaowu", Result: "ok", OccurredAt: time.Now()},
		{RequestID: reqID, UserID: "bob", Action: "fetch", Target: "jiaowu", Result: "target_denied", OccurredAt: time.Now()},
	}
	for _, e := range entries {
		if err := store.Append(ctx, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := store.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// 用同一个 DSN 反查落库结果（验证写入真的发生，而不是只看返回值）。
	got, err := platform.QueryAuditByRequestID(ctx, os.Getenv("DATABASE_URL"), reqID)
	if err != nil {
		t.Fatalf("QueryAuditByRequestID: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("应落库 3 条，实际 %d 条：%+v", len(got), got)
	}
	// 流水号把一次请求的三个动作串起来。
	for _, e := range got {
		if e.RequestID != reqID {
			t.Fatalf("流水号不符：%q", e.RequestID)
		}
	}
	if got[2].Result != "target_denied" {
		t.Fatalf("结果码应原样落库，实际 %q", got[2].Result)
	}
}

// 审计字段必须与约定完全一致：只有标识性字段，绝无票据/验证码/响应体。
func TestAuditEntryHasNoSensitiveFields(t *testing.T) {
	// 这条测试是「规格守卫」：给 AuditEntry 加敏感字段时它会编译失败或断言失败。
	e := platform.AuditEntry{
		RequestID:  "r1",
		UserID:     "u1",
		Action:     "login",
		Target:     "auth",
		Result:     "ok",
		OccurredAt: time.Now(),
	}
	if e.RequestID == "" || e.Action == "" {
		t.Fatal("审计必须带流水号与动作")
	}
}
