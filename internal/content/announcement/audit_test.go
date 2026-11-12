package announcement_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"nongyu-backend-plus/internal/content/announcement"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/platform/pgstore"
)

// fakeAudit 是实现 platform.AuditStore 的测试替身，只记录 action。
type fakeAudit struct {
	mu      sync.Mutex
	actions []string
	targets []string
}

func (f *fakeAudit) Append(_ context.Context, e platform.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, e.Action)
	f.targets = append(f.targets, e.Target)
	return nil
}
func (f *fakeAudit) Flush(context.Context) error { return nil }
func (f *fakeAudit) Dropped() int64              { return 0 }
func (f *fakeAudit) Close() error                { return nil }

func (f *fakeAudit) has(action string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.actions {
		if a == action {
			return true
		}
	}
	return false
}

func (f *fakeAudit) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.actions)
}

// TestAdminActions_AreAudited 锁住 #46 Q3「管理动作一律留痕」这条。
//
// 双轴审查曾指出公告发布/撤回/软删「零留痕」，这里确保每个管理动作
// 都落一条 <域>.<动作> 审计，且 target 形如 announcement:<id>。
func TestAdminActions_AreAudited(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	audit := &fakeAudit{}
	svc := announcement.New(announcement.Options{
		Repo:  pgstore.NewAnnouncementMemoryRepo(func() time.Time { return now }),
		Clock: fixedClockAt{now: now},
		Audit: audit,
	})

	ctx := announcement.WithAuditContext(context.Background(), "req-1", "42")

	// 创建（草稿）
	a, err := svc.Create(ctx, announcement.CreateInput{
		Title: "t", Body: "b",
		Audience: announcement.Audience{Scope: announcement.AudienceAll},
	})
	if err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	if !audit.has("announcement.create") {
		t.Fatalf("创建未留痕：%v", audit.actions)
	}

	// 发布
	if _, err := svc.Publish(ctx, a.ID); err != nil {
		t.Fatalf("发布失败：%v", err)
	}
	if !audit.has("announcement.publish") {
		t.Fatalf("发布未留痕：%v", audit.actions)
	}

	// 编辑
	title := "t2"
	if _, err := svc.Update(ctx, a.ID, announcement.UpdateInput{Title: &title}); err != nil {
		t.Fatalf("编辑失败：%v", err)
	}
	if !audit.has("announcement.update") {
		t.Fatalf("编辑未留痕：%v", audit.actions)
	}

	// 撤回
	if _, err := svc.Revoke(ctx, a.ID); err != nil {
		t.Fatalf("撤回失败：%v", err)
	}
	if !audit.has("announcement.revoke") {
		t.Fatalf("撤回未留痕：%v", audit.actions)
	}

	// 软删
	if err := svc.Delete(ctx, a.ID); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if !audit.has("announcement.delete") {
		t.Fatalf("删除未留痕：%v", audit.actions)
	}

	// target 必须是标识性形式，不能塞正文。
	audit.mu.Lock()
	defer audit.mu.Unlock()
	for _, tg := range audit.targets {
		if tg == "" {
			t.Fatal("审计 target 不能为空")
		}
		if len(tg) > 64 {
			t.Fatalf("审计 target 过长，疑似塞入了正文：%q", tg)
		}
	}
}

// TestAudit_NilStoreDoesNotPanic 保证没有 AUDIT 时业务照常。
func TestAudit_NilStoreDoesNotPanic(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	svc := announcement.New(announcement.Options{
		Repo:  pgstore.NewAnnouncementMemoryRepo(func() time.Time { return now }),
		Clock: fixedClockAt{now: now},
		// Audit 留空
	})
	if _, err := svc.Create(context.Background(), announcement.CreateInput{
		Title: "t", Body: "b",
		Audience: announcement.Audience{Scope: announcement.AudienceAll},
	}); err != nil {
		t.Fatalf("无审计注入时创建应成功：%v", err)
	}
}

type fixedClockAt struct{ now time.Time }

func (f fixedClockAt) Now() time.Time { return f.now }
