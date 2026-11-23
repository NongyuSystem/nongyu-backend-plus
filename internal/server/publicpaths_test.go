package server_test

import (
	"net/http"
	"testing"

	"nongyu-backend-plus/internal/server"
)

// 这一组用例锁住 2026-11-23 维护者决定后的认证边界：
// **基本所有业务端点都要认证**（publicReadablePaths 已清空为 []）。
// 唯一免认证的是 publicPaths 里的「取得身份类 / 探活」端点。
//
// 判定入口仍然是唯一的 AllowedWithoutAuth，两张清单不混用。

func TestIsPublicPath_IdentityPaths(t *testing.T) {
	// 既有「取得身份」类路径必须继续豁免，否则登录会变成鸡生蛋。
	// /status 不在此列：它返回内部依赖状态，必须要求有效会话。
	for _, p := range []string{
		"/healthz",
		"/identity/login",
		"/identity/login/admin",
		"/identity/mailcode",
		"/identity/register",
	} {
		if !server.IsPublicPath(p) {
			t.Fatalf("%s 应仍是取得身份类公开路径", p)
		}
	}
	// /status 不再免认证：唯一免认证探针只有 /healthz。
	if server.IsPublicPath("/status") {
		t.Fatal("/status 返回内部基础设施状态，不得再是免认证公开路径")
	}
}

// TestAllowedWithoutAuth_AllBusinessPathsRequireAuth 是本次改动的核心回归用例。
//
// 历史上 /announcements、/posts、/skins 在 publicReadablePaths 里，
// 未登录可以读。现在**全部业务数据一律要求会话**——包括公告、帖子、
// 皮肤目录与包体下载。
func TestAllowedWithoutAuth_AllBusinessPathsRequireAuth(t *testing.T) {
	business := []string{
		"/announcements",
		"/announcements/latest",
		"/announcements/42",
		"/posts",
		"/posts/42",
		"/posts/mine",
		"/skins/current",
		"/skins/default/1.0.0",
		"/skins/default/1.0.0/pack",
		"/classrooms",
		"/classrooms/lookup",
		"/classrooms/buildings",
		"/messages",
		"/track/events",
		"/admin/posts",
	}
	for _, p := range business {
		// GET 和 HEAD 都不能再免认证。
		if server.AllowedWithoutAuth(http.MethodGet, p) {
			t.Fatalf("GET %s 现在应要求会话（公开读清单已清空）", p)
		}
		if server.AllowedWithoutAuth(http.MethodHead, p) {
			t.Fatalf("HEAD %s 现在应要求会话（公开读清单已清空）", p)
		}
		// 写操作当然也要认证。
		if server.AllowedWithoutAuth(http.MethodPost, p) {
			t.Fatalf("POST %s 应要求会话", p)
		}
	}
}

// TestAllowedWithoutAuth_StillRequiresAuth 保留原有断言：消息、埋点、管理端
// 从不进公开清单。这里再加几个业务路径，防止将来有人误把它们加回去。
func TestAllowedWithoutAuth_StillRequiresAuth(t *testing.T) {
	for _, p := range []string{
		"/messages",
		"/messages/unread-count",
		"/track/events",
		"/track/events/summary",
		"/admin/announcements",
		"/admin/posts",
		"/classrooms/lookup",
		"/classrooms/buildings/1",
		"/post-reviews/teacher",
		"/post-reviews/teacher-course",
		"/keys/amap/acquire",
	} {
		if server.AllowedWithoutAuth(http.MethodGet, p) {
			t.Fatalf("%s 不应是公开路径（必须要求会话）", p)
		}
	}
}

// TestAllowedWithoutAuth_IdentityAndHealthStillPublic 确认「鸡生蛋」端点没被误伤。
//
// 把业务端点全部改为需认证时，最容易顺手把登录端点也锁上——
// 那样谁都登不进去。这条测试专门守住这几个例外。
func TestAllowedWithoutAuth_IdentityAndHealthStillPublic(t *testing.T) {
	for _, p := range []string{
		"/healthz",
		"/identity/login",
		"/identity/login/admin",
		"/identity/mailcode",
		"/identity/mailcode/verify",
		"/identity/register",
	} {
		if !server.AllowedWithoutAuth(http.MethodGet, p) {
			t.Fatalf("%s 必须保持公开（身份获取/探活）", p)
		}
	}
}

// TestAllowedWithoutAuth_PrefixDoesNotOverreach 前缀匹配不能把清单外的路径放过。
//
// 现在清单是空的，这条测试主要防止将来有人加回 "/posts" 时
// 用裸 HasPrefix 导致 "/postsfoo" 也免认证。
func TestAllowedWithoutAuth_PrefixDoesNotOverreach(t *testing.T) {
	for _, p := range []string{
		"/announcementsx",
		"/postsfoo",
		"/skinsXYZ",
		"/classroomsPrivate",
	} {
		if server.AllowedWithoutAuth(http.MethodGet, p) {
			t.Fatalf("前缀匹配不应命中清单外路径 %s", p)
		}
	}
}
