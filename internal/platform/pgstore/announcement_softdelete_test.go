package pgstore_test

import (
	"context"
	"testing"
	"time"

	"nongyu-backend-plus/internal/content"
	"nongyu-backend-plus/internal/content/announcement"
)

// 回归守卫：软删后 FindByID 必须表现为「不存在」。
//
// 为什么单独一条：CreateFindUpdate 里也断言了这一点，但那条测试带 Update /
// Audience 往返 / 幂等重删等一大串前置步骤，一旦失败要先排除是别处污染。
// 这条只保留 Create → MarkDeleted → FindByID 三步，是能抓到该 bug 的最小闭环。
//
// 根因（2026-11-13）：AnnouncementPGRepo.FindByID 的 SQL 只按 id 查，漏了
// `AND deleted_at IS NULL`，而同文件的 list() / Update / MarkDeleted 都带了。
// 软删行仍在表里，于是 FindByID 返回实体而非 NotFound。
func TestAnnouncementPGRepo_SoftDeletedFindByIDReturnsNotFound(t *testing.T) {
	repo, _ := newAnnouncementRepo(t)
	if repo == nil {
		return
	}
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	pub := now.Add(-time.Hour)

	a := &announcement.Announcement{
		Title: "软删回归", Body: "正文",
		Status: announcement.StatusPublished, PublishedAt: &pub,
		PublishRevision: 1,
		Audience:        announcement.Audience{Scope: announcement.AudienceAll},
	}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.MarkDeleted(ctx, a.ID, now); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	_, err := repo.FindByID(ctx, a.ID)
	if err == nil {
		t.Fatal("软删后 FindByID 应返回 NotFound（实际 nil error）")
	}
	if code, ok := content.CodeOf(err); !ok || code != content.CodeNotFound {
		t.Fatalf("应返回 CodeNotFound，实际 code=%v ok=%v err=%v", code, ok, err)
	}
}
