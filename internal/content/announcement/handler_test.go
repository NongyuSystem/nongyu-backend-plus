package announcement

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// 这一层是 HTTP 集成测：httptest 打真实路由，仓储用内存实现。
// 依据 docs/specs/spec-六类常见功能.md 的 Testing Decisions ②。
//
// 刻意**不**在这里挂 server.AdminOnly：内容域若为了测试去 import server，
// 就违反了「业务域不依赖框架适配层」的边界（README 架构约束第 1、2 条）。
// 管理端角色门槛的 fail-closed 行为在 internal/server 的中间件测试里覆盖。

func init() {
	gin.SetMode(gin.TestMode)
}

// testViewer 从请求头读读者画像，让表驱动的 HTTP 用例不必构造会话。
func testViewer(c *gin.Context) Viewer {
	v := Viewer{}
	if raw := c.GetHeader("X-Test-Role"); raw != "" {
		if r, err := strconv.Atoi(raw); err == nil {
			v.Authenticated = true
			v.Role = r
		}
	}
	v.College = c.GetHeader("X-Test-College")
	v.Grade = c.GetHeader("X-Test-Grade")
	return v
}

// newTestServer 构造「读者侧 + 管理端（无中间件）」的测试路由。
func newTestServer(t *testing.T) (*gin.Engine, *Service) {
	t.Helper()
	repo := NewMemoryRepo(func() time.Time { return *ts("2026-11-12T00:00:00Z") })
	svc := New(Options{Repo: repo, Clock: fixedClock{now: *ts("2026-11-12T00:00:00Z")}})

	r := gin.New()
	RegisterPublic(r, svc, testViewer)
	RegisterAdmin(r, svc)
	return r, svc
}

type fixedClock struct{ now time.Time }

func (f fixedClock) Now() time.Time { return f.now }

// doJSON 发一个请求并返回状态码与已解析的响应体。
func doJSON(t *testing.T, r *gin.Engine, method, path string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败：%v", err)
		}
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	out := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

// createAnnouncement 走真实 HTTP 创建一条公告，返回 id。
func createAnnouncement(t *testing.T, r *gin.Engine, in map[string]any) int64 {
	t.Helper()
	code, body := doJSON(t, r, http.MethodPost, "/admin/announcements", in, nil)
	if code != http.StatusCreated {
		t.Fatalf("创建公告失败：status=%d body=%v", code, body)
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 data：%v", body)
	}
	idF, ok := data["id"].(float64)
	if !ok {
		t.Fatalf("响应缺少 id：%v", data)
	}
	return int64(idF)
}

func TestPublicList_AnonymousSeesOnlyAllScope(t *testing.T) {
	r, _ := newTestServer(t)

	// 全员公告（直接发布）。
	createAnnouncement(t, r, map[string]any{
		"title": "全校停课", "body": "明天停课",
		"audience": map[string]any{"scope": "all"}, "publish": true,
	})
	// 按角色定向的公告（直接发布）。
	createAnnouncement(t, r, map[string]any{
		"title": "仅管理员", "body": "内部",
		"audience": map[string]any{"scope": "role", "roles": []int{1, 2}}, "publish": true,
	})

	code, body := doJSON(t, r, http.MethodGet, "/announcements", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("匿名读列表应 200，得到 %d", code)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("匿名读者应只看到 1 条全员公告，实际 %d 条：%v", len(items), body)
	}
	first := items[0].(map[string]any)
	if first["title"] != "全校停课" {
		t.Fatalf("看到的是意料之外的公告：%v", first)
	}
	// 读者侧不该泄露内部字段。
	if _, has := first["status"]; has {
		t.Fatal("读者侧响应不应包含 status")
	}
	if _, has := first["audience"]; has {
		t.Fatal("读者侧响应不应包含 audience")
	}
}

func TestPublicList_RoleScopedVisibleToMatchingRole(t *testing.T) {
	r, _ := newTestServer(t)
	createAnnouncement(t, r, map[string]any{
		"title": "仅管理员", "body": "内部",
		"audience": map[string]any{"scope": "role", "roles": []int{1, 2}}, "publish": true,
	})

	code, body := doJSON(t, r, http.MethodGet, "/announcements", nil, map[string]string{"X-Test-Role": "1"})
	if code != http.StatusOK {
		t.Fatalf("应 200，得到 %d", code)
	}
	if items, _ := body["items"].([]any); len(items) != 1 {
		t.Fatalf("role=1 应看到定向公告，实际 %d 条", len(items))
	}
}

func TestPublicList_DraftAndRevokedHidden(t *testing.T) {
	r, _ := newTestServer(t)

	// 草稿：不发布。
	createAnnouncement(t, r, map[string]any{
		"title": "草稿", "body": "未定稿",
		"audience": map[string]any{"scope": "all"}, "publish": false,
	})
	// 已发布后撤回。
	id := createAnnouncement(t, r, map[string]any{
		"title": "要撤回", "body": "x",
		"audience": map[string]any{"scope": "all"}, "publish": true,
	})
	if code, body := doJSON(t, r, http.MethodPatch, "/admin/announcements/"+strconv.FormatInt(id, 10),
		map[string]any{"action": "revoke"}, nil); code != http.StatusOK {
		t.Fatalf("撤回失败：%d %v", code, body)
	}

	_, body := doJSON(t, r, http.MethodGet, "/announcements", nil, nil)
	if items, _ := body["items"].([]any); len(items) != 0 {
		t.Fatalf("草稿与已撤回都不应出现在读者列表，实际 %d 条：%v", len(items), items)
	}

	// 撤回后详情也不可见。
	if code, _ := doJSON(t, r, http.MethodGet, "/announcements/"+strconv.FormatInt(id, 10), nil, nil); code != http.StatusNotFound {
		t.Fatalf("已撤回公告详情应 404，得到 %d", code)
	}
}

func TestPublicDetail_ExpiredStillReadable(t *testing.T) {
	r, _ := newTestServer(t)
	id := createAnnouncement(t, r, map[string]any{
		"title": "已过期", "body": "x",
		"audience": map[string]any{"scope": "all"}, "publish": true,
		// 固定时钟是 2026-11-12，这里让它在昨天就过期。
		"expiresAt": "2026-11-11T00:00:00Z",
	})

	// 过期：不在列表。
	_, listBody := doJSON(t, r, http.MethodGet, "/announcements", nil, nil)
	if items, _ := listBody["items"].([]any); len(items) != 0 {
		t.Fatalf("过期公告不应出现在列表，实际 %d 条", len(items))
	}
	// 但详情仍可查（Spec 验收 4.1.3）。
	code, body := doJSON(t, r, http.MethodGet, "/announcements/"+strconv.FormatInt(id, 10), nil, nil)
	if code != http.StatusOK {
		t.Fatalf("过期公告详情应 200，得到 %d：%v", code, body)
	}
}

func TestPublicLatest_EmptyIsNullNotError(t *testing.T) {
	r, _ := newTestServer(t)
	code, body := doJSON(t, r, http.MethodGet, "/announcements/latest", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("没有公告时应 200，得到 %d", code)
	}
	if v, ok := body["data"]; !ok || v != nil {
		t.Fatalf("没有公告时 data 应为 null，得到 %v", body)
	}
}

func TestPublicLatest_PinnedWins(t *testing.T) {
	r, _ := newTestServer(t)
	createAnnouncement(t, r, map[string]any{
		"title": "普通", "body": "x",
		"audience": map[string]any{"scope": "all"}, "publish": true,
	})
	createAnnouncement(t, r, map[string]any{
		"title": "置顶", "body": "x", "pinned": true,
		"audience": map[string]any{"scope": "all"}, "publish": true,
	})

	_, body := doJSON(t, r, http.MethodGet, "/announcements/latest", nil, nil)
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("latest 应有 data：%v", body)
	}
	if data["title"] != "置顶" {
		t.Fatalf("置顶公告应优先，实际拿到 %v", data["title"])
	}
}

func TestAdminCreate_InvalidAudienceRejected(t *testing.T) {
	r, _ := newTestServer(t)
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"空标题", map[string]any{"title": "", "body": "b",
			"audience": map[string]any{"scope": "all"}}, http.StatusBadRequest},
		{"role 缺角色", map[string]any{"title": "t", "body": "b",
			"audience": map[string]any{"scope": "role"}}, http.StatusBadRequest},
		{"未知范围", map[string]any{"title": "t", "body": "b",
			"audience": map[string]any{"scope": "nope"}}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := doJSON(t, r, http.MethodPost, "/admin/announcements", tc.body, nil)
			if code != tc.want {
				t.Fatalf("status = %d, want %d, body=%v", code, tc.want, body)
			}
			if body["code"] == nil {
				t.Fatalf("错误响应应带 code：%v", body)
			}
		})
	}
}

func TestAdminPublish_RevisionAdvances(t *testing.T) {
	r, _ := newTestServer(t)
	id := createAnnouncement(t, r, map[string]any{
		"title": "t", "body": "b",
		"audience": map[string]any{"scope": "all"}, "publish": false,
	})
	path := "/admin/announcements/" + strconv.FormatInt(id, 10)

	// 草稿 → 发布：revision = 1。
	code, body := doJSON(t, r, http.MethodPatch, path, map[string]any{"action": "publish"}, nil)
	if code != http.StatusOK {
		t.Fatalf("发布失败：%d %v", code, body)
	}
	data := body["data"].(map[string]any)
	if got := data["publishRevision"].(float64); got != 1 {
		t.Fatalf("首次发布 revision 应为 1，得到 %v", got)
	}

	// 已发布 → 发布：Spec 的状态机没有这条转移，必须拒绝。
	// 若允许并自增，管理端误点两次就会让学生收到两条同样的通知。
	code, body = doJSON(t, r, http.MethodPatch, path, map[string]any{"action": "publish"}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("已发布再发布应 400（状态机不允许），得到 %d：%v", code, body)
	}

	// 撤回不改变 revision。
	_, body = doJSON(t, r, http.MethodPatch, path, map[string]any{"action": "revoke"}, nil)
	data = body["data"].(map[string]any)
	if data["status"] != "revoked" {
		t.Fatalf("撤回后状态应为 revoked，得到 %v", data["status"])
	}
	if got := data["publishRevision"].(float64); got != 1 {
		t.Fatalf("撤回不应改变 revision，得到 %v", got)
	}

	// 撤回 → 重发：Spec 允许，revision 递增，通知才会再发一次。
	_, body = doJSON(t, r, http.MethodPatch, path, map[string]any{"action": "publish"}, nil)
	data = body["data"].(map[string]any)
	if got := data["publishRevision"].(float64); got != 2 {
		t.Fatalf("撤回后重发 revision 应递增到 2，得到 %v", got)
	}
}

func TestAdminRevoke_DraftRejected(t *testing.T) {
	r, _ := newTestServer(t)
	id := createAnnouncement(t, r, map[string]any{
		"title": "t", "body": "b",
		"audience": map[string]any{"scope": "all"}, "publish": false,
	})
	code, body := doJSON(t, r, http.MethodPatch, "/admin/announcements/"+strconv.FormatInt(id, 10),
		map[string]any{"action": "revoke"}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("草稿不能撤回，应 400，得到 %d：%v", code, body)
	}
}

func TestAdminList_IncludesDrafts(t *testing.T) {
	r, _ := newTestServer(t)
	createAnnouncement(t, r, map[string]any{
		"title": "草稿", "body": "b",
		"audience": map[string]any{"scope": "all"}, "publish": false,
	})
	code, body := doJSON(t, r, http.MethodGet, "/admin/announcements", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("管理端列表应 200，得到 %d", code)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("管理端应看到草稿，实际 %d 条", len(items))
	}
	first := items[0].(map[string]any)
	if first["status"] != "draft" {
		t.Fatalf("管理端应看到 status，得到 %v", first)
	}
}

func TestAdminDelete_ThenGoneFromBothLists(t *testing.T) {
	r, _ := newTestServer(t)
	id := createAnnouncement(t, r, map[string]any{
		"title": "t", "body": "b",
		"audience": map[string]any{"scope": "all"}, "publish": true,
	})
	path := "/admin/announcements/" + strconv.FormatInt(id, 10)

	if code, body := doJSON(t, r, http.MethodDelete, path, nil, nil); code != http.StatusOK {
		t.Fatalf("删除失败：%d %v", code, body)
	}
	// 幂等：再删一次仍成功。
	if code, body := doJSON(t, r, http.MethodDelete, path, nil, nil); code != http.StatusOK {
		t.Fatalf("重复删除应幂等成功，得到 %d %v", code, body)
	}

	_, listBody := doJSON(t, r, http.MethodGet, "/announcements", nil, nil)
	if items, _ := listBody["items"].([]any); len(items) != 0 {
		t.Fatalf("已删公告不应在读者列表，实际 %d 条", len(items))
	}
	_, adminBody := doJSON(t, r, http.MethodGet, "/admin/announcements", nil, nil)
	if items, _ := adminBody["items"].([]any); len(items) != 0 {
		t.Fatalf("已删公告不应在管理端列表，实际 %d 条", len(items))
	}
}

func TestPublicLatest_FindsBeyondFirstWindow(t *testing.T) {
	r, _ := newTestServer(t)

	// 先造 60 条「按角色定向」的公告（匿名读者看不到），
	// 再发 1 条全员公告。全员公告排在最后（同刻发布时按 id 升序，
	// 它 id 最大 → 在倒序里最靠前的那批之外）。
	//
	// 这条用例锁住一个真实缺陷：若 Latest 只扫固定 50 条就返回，
	// 这条全员公告会被静默漏掉，首页显示「没有公告」。
	for i := 0; i < 60; i++ {
		createAnnouncement(t, r, map[string]any{
			"title": "定向" + strconv.Itoa(i), "body": "x",
			"audience": map[string]any{"scope": "role", "roles": []int{1, 2}}, "publish": true,
		})
	}
	createAnnouncement(t, r, map[string]any{
		"title": "全员可见", "body": "x",
		"audience": map[string]any{"scope": "all"}, "publish": true,
	})

	code, body := doJSON(t, r, http.MethodGet, "/announcements/latest", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("latest 应 200，得到 %d", code)
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("窗口外的全员公告不应被漏掉，实际 data=%v", body["data"])
	}
	if data["title"] != "全员可见" {
		t.Fatalf("应找到那唯一的全员公告，得到 %v", data["title"])
	}
}

func TestPagination_CursorWalksWithoutDuplicates(t *testing.T) {
	r, _ := newTestServer(t)
	// 造 5 条，发布时间由仓储的固定时钟给（同刻），id 兜底排序。
	for i := 0; i < 5; i++ {
		createAnnouncement(t, r, map[string]any{
			"title": "t" + strconv.Itoa(i), "body": "b",
			"audience": map[string]any{"scope": "all"}, "publish": true,
		})
	}

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		path := "/announcements?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		code, body := doJSON(t, r, http.MethodGet, path, nil, nil)
		if code != http.StatusOK {
			t.Fatalf("翻页失败：%d %v", code, body)
		}
		items, _ := body["items"].([]any)
		for _, it := range items {
			title := it.(map[string]any)["title"].(string)
			if seen[title] {
				t.Fatalf("翻页出现重复条目：%s", title)
			}
			seen[title] = true
		}
		next, _ := body["next"].(string)
		if next == "" {
			break
		}
		cursor = next
		pages++
		if pages > 10 {
			t.Fatal("翻页未收敛，可能游标没有推进")
		}
	}
	if len(seen) != 5 {
		t.Fatalf("应翻到 5 条，实际 %d 条", len(seen))
	}
}
