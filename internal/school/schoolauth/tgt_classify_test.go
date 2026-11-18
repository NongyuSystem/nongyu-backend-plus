package schoolauth_test

// #25 任务 4 / #20：续期失败必须区分两类，且各给可识别原因：
//   - 凭据真的失效（学校侧拒绝）→ 应提示「需客户端重交凭据」
//   - 缺少配套 cookie（代码/数据 bug）→ 应提示「缺 cookie」
// seam：TGTManager.EnsureFresh（公共接口），断言错误文本可识别。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/schoolauth"
)

// 学校侧拒绝换票（业务码非 200）→ 归为「凭据失效」，提示重交。
func TestTGTManager_RefreshCredentialRejectedIsClassified(t *testing.T) {
	// 换票接口返回业务码 500「未登录」：这正是学校侧拒绝的形状
	d := &scriptedDoer{responses: map[string]*scriptedResp{
		"/api/v1/cas/login": {body: `{"requestId":"r","code":500,"msg":"未登录","data":""}`},
	}}
	src, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: d})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}
	m := newManagerWithSource(t, src)

	got, err := m.EnsureFresh(context.Background(), "u1", schoolauth.Credential{})
	if err == nil {
		t.Fatal("学校侧拒绝换票时必须返回错误")
	}
	// 必须是「凭据失效」这一类，且明确要求重交
	if !errors.Is(err, schoolauth.ErrCredentialRejected) {
		t.Fatalf("应归类为 ErrCredentialRejected，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "重新交") && !strings.Contains(err.Error(), "重交") {
		t.Fatalf("凭据失效必须提示重交，实际 %q", err.Error())
	}
	_ = got
}

// 缺配套 cookie → 归为「数据/代码 bug」，与凭据失效可区分。
func TestTGTManager_RefreshMissingCookieIsClassifiedSeparately(t *testing.T) {
	m := newManagerWithSource(t, mustJiaowuSource(t))
	ctx := context.Background()

	// 只存 tgt、不带 cookie
	if err := m.Save(ctx, "u2", schoolauth.TGTBundle{TGT: "201690:TGT:ONLY"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 强制走续期分支：TTL 与 RefreshBefore 由 helper 设定
	_, err := m.EnsureFresh(ctx, "u2", schoolauth.Credential{})
	if err == nil {
		t.Fatal("缺 cookie 时续期必须失败")
	}
	if !errors.Is(err, schoolauth.ErrMissingCookie) {
		t.Fatalf("应归类为 ErrMissingCookie，实际 %v", err)
	}
	// 两类必须可区分
	if errors.Is(err, schoolauth.ErrCredentialRejected) {
		t.Fatal("缺 cookie 不应被归为凭据失效（那是代码/数据 bug，不是用户要重登）")
	}
}

// helper：构造一个「本地有完整记录、已临期」的 manager，便于强制走续期分支。
func newManagerWithSource(t *testing.T, src *schoolauth.JiaowuSource) *schoolauth.TGTManager {
	t.Helper()
	store := newMemTGTStore()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	m, err := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:         store,
		Cipher:        platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source:        src,
		Clock:         platform.NewMockClock(now),
		TTL:           24 * time.Hour,
		RefreshBefore: 25 * time.Hour, // 强制立即续期
	})
	if err != nil {
		t.Fatalf("NewTGTManager: %v", err)
	}
	// 预置一条完整记录（含整套 cookie），保证走到「换票」而非「缺 cookie」分支
	if err := m.Save(context.Background(), "u1", testBundle("201690:TGT:OLD")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return m
}
