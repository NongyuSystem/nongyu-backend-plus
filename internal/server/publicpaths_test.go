package server_test

import (
	"net/http"
	"testing"

	"nongyu-backend-plus/internal/server"
)

// 这一组用例锁住 docs/specs/spec-六类常见功能.md 的「不动清单」第 5 条：
// 公开读端点另立 publicReadablePaths，**不污染** publicPaths 的原语义，
// 且「是否需要认证」的判定仍然只有一处入口（AllowedWithoutAuth）。

func TestIsPublicPath_IdentityPaths(t *testing.T) {
	// 既有「取得身份」类路径必须继续豁免，否则登录会变成鸡生蛋。
	for _, p := range []string{
		"/healthz",
		"/status",
		"/identity/login",
		"/identity/login/admin",
		"/identity/mailcode",
		"/identity/register",
	} {
		if !server.IsPublicPath(p) {
			t.Fatalf("%s 应仍是取得身份类公开路径", p)
		}
	}
}

// TestAllowedWithoutAuth_PublicReadOnlyGet 是本次实现中抓到的漏洞的回归用例。
//
// publicReadablePaths 里有 "/posts"，而 "/posts/attachments"（上传）、
// "/posts/42/replies"（留言）、"/posts/42/like"（点赞）全都落在该前缀下。
// 若只按路径判定，这些**写操作会被误判为免认证**——即任何人无需登录即可
// 上传附件、发留言、点赞。因此公开可读必须叠加「仅 GET/HEAD」。
func TestAllowedWithoutAuth_PublicReadOnlyGet(t *testing.T) {
	readable := []string{
		"/announcements",
		"/announcements/latest",
		"/announcements/42",
		"/posts",
		"/posts/42",
	}
	for _, p := range readable {
		if !server.AllowedWithoutAuth(http.MethodGet, p) {
			t.Fatalf("GET %s 应免认证", p)
		}
		if !server.AllowedWithoutAuth(http.MethodHead, p) {
			t.Fatalf("HEAD %s 应免认证", p)
		}
	}

	// 同一批路径上的写操作**必须**要求认证。
	writes := []struct {
		method, path string
	}{
		{http.MethodPost, "/posts"},             // 发帖
		{http.MethodPost, "/posts/attachments"}, // 上传附件（曾误放行）
		{http.MethodPost, "/posts/42/replies"},  // 留言
		{http.MethodDelete, "/posts/42"},        // 删帖
		{http.MethodPatch, "/posts/42"},         // 编辑
		{http.MethodPost, "/announcements"},     // 公告写操作（前缀同源，防同类回归）
		{http.MethodDelete, "/announcements/42"},
	}
	for _, w := range writes {
		if server.AllowedWithoutAuth(w.method, w.path) {
			t.Fatalf("%s %s 必须要求已认证身份，不能被公开前缀顺带豁免", w.method, w.path)
		}
	}
}

func TestAllowedWithoutAuth_StillRequiresAuth(t *testing.T) {
	// 消息、埋点、管理端一律不进公开清单。
	for _, p := range []string{
		"/messages",
		"/messages/unread-count",
		"/track/events",
		"/track/events/summary",
		"/admin/announcements",
		"/admin/posts",
	} {
		if server.AllowedWithoutAuth(http.MethodGet, p) {
			t.Fatalf("%s 不应是公开路径（必须要求会话）", p)
		}
	}
}

func TestAllowedWithoutAuth_PrefixDoesNotOverreach(t *testing.T) {
	// 前缀匹配必须是 "p/" 形式，不能把 /announcementsx 也放过。
	if server.IsPublicReadable(http.MethodGet, "/announcementsx") {
		t.Fatal("前缀匹配不应命中 /announcementsx")
	}
	if server.IsPublicReadable(http.MethodGet, "/postsfoo") {
		t.Fatal("前缀匹配不应命中 /postsfoo")
	}
}
