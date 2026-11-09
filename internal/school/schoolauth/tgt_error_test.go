package schoolauth_test

// code-review M3：本地有保管记录但换票失败时，错误必须保留真实原因，
// 不能被静默吞掉后误报「没有可用的 tgt」。

import (
	"context"
	"strings"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/schoolauth"
)

func TestTGTManager_RefreshFailureKeepsRealCause(t *testing.T) {
	// 用 failDoer 构造 JiaowuSource：换票请求必然失败
	src, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: failDoer{}})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}
	store := newMemTGTStore()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	m, err := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:         store,
		Cipher:        platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source:        src,
		Clock:         platform.NewMockClock(now),
		TTL:           24 * time.Hour,
		RefreshBefore: 25 * time.Hour, // 强制走续期分支
	})
	if err != nil {
		t.Fatalf("NewTGTManager: %v", err)
	}
	ctx := context.Background()
	// 本地有完整保管记录
	if err := m.Save(ctx, "u1", testBundle("201690:TGT:OLD")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err = m.EnsureFresh(ctx, "u1", schoolauth.Credential{})
	if err == nil {
		t.Fatal("换票失败时必须返回错误")
	}
	// 必须保留「续期失败」的真实原因，不能误归因成「没有可用的 tgt」
	if !strings.Contains(err.Error(), "续期失败") {
		t.Fatalf("错误应保留续期失败原因，实际 %q", err.Error())
	}
	if strings.Contains(err.Error(), "没有可用的 tgt") {
		t.Fatalf("本地明明有记录，不得误报没有可用的 tgt，实际 %q", err.Error())
	}
}
