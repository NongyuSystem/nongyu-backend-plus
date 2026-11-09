package flow_test

// #21 M11：退全部设备。
// 事实前提：session.Issue 是单设备语义（签发前先 RevokeByUserScope），
// 因此「登录两台设备」在同一 scope 下本来只会留一条。这里改用 store 直接
// 造同一用户同 scope 的两条会话，验证 LogoutAll 能一次全部吊销。

import (
	"context"
	"testing"
	"time"

	"nongyu-backend-plus/internal/identity/flow"
	"nongyu-backend-plus/internal/identity/login"
	"nongyu-backend-plus/internal/platform"
)

const noLogoutAll = "888100007"

func TestFlow_LogoutAllRevokesEverySessionOfUser(t *testing.T) {
	f := setupFlow(t, fullIdent(noLogoutAll))
	ctx := context.Background()

	// 先通过登录建号，拿到 userID
	signed, err := f.flow.Login(ctx, flow.Params{
		Method:   "school",
		DeviceID: "dev-A",
		SchoolCred: login.SchoolCredential{
			Method: "password", StudentNo: noLogoutAll, Password: "school-pw",
		},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// 再直接插入第二条同 scope 会话（绕过单设备签发限制，模拟多设备残留）
	now := time.Now()
	s2 := platform.Session{
		ID: "logout-all-session-2", UserID: signed.User.ID,
		Scope: platform.ScopeApp, DeviceID: "dev-B",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := f.store.CreateSession(ctx, s2); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// 两条都还在
	for _, id := range []string{signed.SessionID, s2.ID} {
		got, err := f.store.SessionByID(ctx, id)
		if err != nil {
			t.Fatalf("会话 %s 应存在：%v", id, err)
		}
		if got.RevokedAt != nil {
			t.Fatalf("会话 %s 不应已吊销", id)
		}
	}

	// 退全部设备
	n, err := f.flow.LogoutAll(ctx, signed.User.ID, platform.ScopeApp)
	if err != nil {
		t.Fatalf("LogoutAll: %v", err)
	}
	if n < 2 {
		t.Fatalf("应至少吊销 2 条会话，实际 %d", n)
	}

	// 两条都已失效
	for _, id := range []string{signed.SessionID, s2.ID} {
		got, err := f.store.SessionByID(ctx, id)
		if err != nil {
			t.Fatalf("查会话 %s：%v", id, err)
		}
		if got.RevokedAt == nil {
			t.Fatalf("会话 %s 应已被吊销", id)
		}
	}
}
