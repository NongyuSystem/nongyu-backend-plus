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

// TestUpdate_WritesBackUpdatedAt 锁住「Update 之后入参的 UpdatedAt 是新的」。
//
// 内存与 PG 两个实现都曾只更新内部副本、不回写入参。服务层会把
// 传入的结构体直接当响应返回，于是客户端拿到旧 updatedAt——
// 用户看到的现象是「改了但时间没变」，像是没保存成功。
func TestUpdate_WritesBackUpdatedAt(t *testing.T) {
	t0 := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	later := t0.Add(time.Hour)

	// 用可推进时钟：先 t0 建，再 later 改，才能看出回写有没有生效。
	now := t0
	repo := pgstore.NewAnnouncementMemoryRepo(func() time.Time { return now })
	svc := announcement.New(announcement.Options{
		Repo:  repo,
		Clock: clockFunc(func() time.Time { return now }),
	})

	a, err := svc.Create(context.Background(), announcement.CreateInput{
		Title: "t", Body: "b",
		Audience: announcement.Audience{Scope: announcement.AudienceAll},
	})
	if err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	createdAt := a.UpdatedAt

	now = later
	title := "改过的标题"
	updated, err := svc.Update(context.Background(), a.ID, announcement.UpdateInput{Title: &title})
	if err != nil {
		t.Fatalf("更新失败：%v", err)
	}
	if !updated.UpdatedAt.After(createdAt) {
		t.Fatalf("UpdatedAt 应被回写为新时间：创建时 %v，更新后仍是 %v",
			createdAt, updated.UpdatedAt)
	}
}

// clockFunc 让普通函数满足 platform.Clock。
type clockFunc func() time.Time

func (f clockFunc) Now() time.Time { return f() }

// TestAdminGet_SeesDraftAndDeleted 锁住管理端单条详情能看草稿与已软删。
//
// 读者侧对这些一律不可见（404/不在列表），但管理端排查
// 「这条为什么没发出去 / 为什么被删」必须看得到。
func TestAdminGet_SeesDraftAndDeleted(t *testing.T) {
	t0 := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	svc := announcement.New(announcement.Options{
		Repo:  pgstore.NewAnnouncementMemoryRepo(func() time.Time { return t0 }),
		Clock: fixedClockAt{now: t0},
	})
	ctx := context.Background()

	// 草稿：读者侧看不到，管理端要能看到
	a, err := svc.Create(ctx, announcement.CreateInput{
		Title: "草稿", Body: "b",
		Audience: announcement.Audience{Scope: announcement.AudienceAll},
	})
	if err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	got, err := svc.AdminGet(ctx, a.ID)
	if err != nil {
		t.Fatalf("管理端应能读草稿：%v", err)
	}
	if got.Status != announcement.StatusDraft {
		t.Fatalf("状态应为草稿，得到 %v", got.Status)
	}

	// 软删后仍可读（排查「为什么被删」需要）
	if err := svc.Delete(ctx, a.ID); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	got, err = svc.AdminGet(ctx, a.ID)
	if err != nil {
		t.Fatalf("管理端应能读已软删的公告：%v", err)
	}
	if got.DeletedAt == nil {
		t.Fatal("已软删的公告应带 deletedAt")
	}

	// 非法 id 应被拒
	if _, err := svc.AdminGet(ctx, 0); err == nil {
		t.Fatal("id=0 应被拒")
	}
}

// TestSoftDeleted_CannotBeEditedOrRepublished 锁住「软删之后不可再动」。
//
// 背景：为了让管理端能查到已软删的公告，FindByID 改为返回含软删的行。
// 这带来一个必须补的洞——Update/Publish/Revoke 若不自己判 softDelete，
// 软删的公告就能被继续编辑甚至重新发布，软删形同虚设。
func TestSoftDeleted_CannotBeEditedOrRepublished(t *testing.T) {
	t0 := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	svc := announcement.New(announcement.Options{
		Repo:  pgstore.NewAnnouncementMemoryRepo(func() time.Time { return t0 }),
		Clock: fixedClockAt{now: t0},
	})
	ctx := context.Background()

	a, err := svc.Create(ctx, announcement.CreateInput{
		Title: "t", Body: "b",
		Audience: announcement.Audience{Scope: announcement.AudienceAll},
	})
	if err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	if err := svc.Delete(ctx, a.ID); err != nil {
		t.Fatalf("删除失败：%v", err)
	}

	// 编辑
	title := "改"
	if _, err := svc.Update(ctx, a.ID, announcement.UpdateInput{Title: &title}); err == nil {
		t.Fatal("已软删的公告不该能被编辑")
	}
	// 发布
	if _, err := svc.Publish(ctx, a.ID); err == nil {
		t.Fatal("已软删的公告不该能被发布")
	}
	// 撤回
	if _, err := svc.Revoke(ctx, a.ID); err == nil {
		t.Fatal("已软删的公告不该能被撤回")
	}
	// 删除本身是幂等的（重复删不报错）
	if err := svc.Delete(ctx, a.ID); err != nil {
		t.Fatalf("重复删除应幂等：%v", err)
	}
}
