package schoolauth_test

// #20 M5：tgt 续期必须带整套同域 cookie。
// 实测：单枚 go-admin-tgt 换票返回 code=500 未登录。
// seam = TGTManager.Save / Load / EnsureFresh 公共接口。

import (
	"context"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/schoolauth"
)

// 保管对象必须同时带 tgt 与整套 cookie，且解密后逐字还原。
func TestTGTManager_SaveLoadKeepsFullCookieHeader(t *testing.T) {
	store := newMemTGTStore()
	m, _ := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:  store,
		Cipher: platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source: mustJiaowuSource(t),
		Clock:  platform.NewMockClock(time.Now()),
	})
	ctx := context.Background()
	bundle := schoolauth.TGTBundle{
		TGT:          "201690:TGT:abc",
		CookieHeader: "go-admin-tgt=201690:TGT:abc; laravel_session=LS; XSRF-TOKEN=XS",
	}
	if err := m.Save(ctx, "u1", bundle); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _, err := m.Load(ctx, "u1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.TGT != bundle.TGT || got.CookieHeader != bundle.CookieHeader {
		t.Fatalf("保管内容不符：%+v", got)
	}
}

// 只存 tgt、不带配套 cookie 的记录，续期必须给出可识别的原因（别静默当成功）。
func TestTGTManager_RefreshWithoutCookieHeaderFailsClearly(t *testing.T) {
	store := newMemTGTStore()
	// 让本地记录「临期」，强制走续期分支
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	clock := platform.NewMockClock(now)
	m, _ := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:         store,
		Cipher:        platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source:        mustJiaowuSource(t),
		Clock:         clock,
		TTL:           24 * time.Hour,
		RefreshBefore: 25 * time.Hour, // 强制走续期分支
	})
	ctx := context.Background()
	// 只给 tgt，cookieHeader 为空
	if err := m.Save(ctx, "u1", schoolauth.TGTBundle{TGT: "201690:TGT:only"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, err := m.EnsureFresh(ctx, "u1", schoolauth.Credential{})
	if err == nil {
		t.Fatal("缺配套 cookie 的续期应失败并给出可识别原因")
	}
}
