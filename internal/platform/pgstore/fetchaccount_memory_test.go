package pgstore

import (
	"context"
	"testing"

	"nongyu-backend-plus/internal/school/jiaowu"
)

func TestFetchAccountMemoryRepo_PickRotates(t *testing.T) {
	r := NewFetchAccountMemoryRepo()
	ctx := context.Background()
	if _, err := r.Pick(ctx); err != jiaowu.ErrFetchAccountUnavailable {
		t.Fatalf("空池应回 ErrFetchAccountUnavailable，实际 %v", err)
	}
	id1, _ := r.Create(ctx, jiaowu.FetchAccount{Label: "A", TGT: "T:A", VPNTicket: "vpnaaaa1111", Enabled: true})
	id2, _ := r.Create(ctx, jiaowu.FetchAccount{Label: "B", TGT: "T:B", VPNTicket: "vpnbbbb2222", Enabled: true})
	a, err := r.Pick(ctx)
	if err != nil || a.ID != id1 {
		t.Fatalf("第一次应取 A，实际 %v err=%v", a.ID, err)
	}
	b, err := r.Pick(ctx)
	if err != nil || b.ID != id2 {
		t.Fatalf("第二次应取 B，实际 %v err=%v", b.ID, err)
	}
	c, err := r.Pick(ctx)
	if err != nil || c.ID != id1 {
		t.Fatalf("第三次应回到 A，实际 %v", c.ID)
	}
}

func TestFetchAccountMemoryRepo_ListNeverReturnsCredentials(t *testing.T) {
	r := NewFetchAccountMemoryRepo()
	ctx := context.Background()
	_, _ = r.Create(ctx, jiaowu.FetchAccount{
		Label: "A", TGT: "SECRET-TGT", CookieHeader: "go-admin-tgt=SECRET-TGT",
		VPNTicket: "SECRET-VPN", Enabled: true,
	})
	rows, err := r.List(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("列表失败 %v len=%d", err, len(rows))
	}
	if rows[0].TGT != "" || rows[0].CookieHeader != "" || rows[0].VPNTicket != "" {
		t.Fatalf("列表不得回显凭据：%+v", rows[0])
	}
}

func TestFetchAccountMemoryRepo_MarkFailureDisables(t *testing.T) {
	r := NewFetchAccountMemoryRepo()
	ctx := context.Background()
	id, _ := r.Create(ctx, jiaowu.FetchAccount{Label: "A", TGT: "T:A", VPNTicket: "vpnaaaa1111", Enabled: true})
	for i := 0; i < 5; i++ {
		_ = r.MarkFailure(ctx, id)
	}
	if _, err := r.Pick(ctx); err != jiaowu.ErrFetchAccountUnavailable {
		t.Fatalf("连续失败后应被停用，实际 %v", err)
	}
}

func TestFetchAccountMemoryRepo_SetEnabledAndDelete(t *testing.T) {
	r := NewFetchAccountMemoryRepo()
	ctx := context.Background()
	id, _ := r.Create(ctx, jiaowu.FetchAccount{Label: "A", TGT: "T:A", VPNTicket: "vpnaaaa1111", Enabled: true})
	_ = r.SetEnabled(ctx, id, false)
	if _, err := r.Pick(ctx); err != jiaowu.ErrFetchAccountUnavailable {
		t.Fatalf("停用后不应被选中，实际 %v", err)
	}
	_ = r.SetEnabled(ctx, id, true)
	if _, err := r.Pick(ctx); err != nil {
		t.Fatalf("重新启用后应可取，实际 %v", err)
	}
	_ = r.Delete(ctx, id)
	if _, err := r.Pick(ctx); err != jiaowu.ErrFetchAccountUnavailable {
		t.Fatalf("删除后不应被选中，实际 %v", err)
	}
}
