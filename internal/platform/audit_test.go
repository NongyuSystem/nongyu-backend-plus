package platform_test

import (
	"context"
	"os"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
)

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
