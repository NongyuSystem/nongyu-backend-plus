package message_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/message"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/platform/pgstore"
)

// 这一层是 **HTTP 集成测**：httptest 打真实路由，仓储用内存实现，
// 依赖（时钟 / 收件人存在性 / 限频）用可计数 fake 注入。
// 依据 docs/specs/spec-六类常见功能.md 的 Testing Decisions ①②：
// 只断言外部可观察行为（状态码、响应体字段、fake 是否被调用），
// 不断言内部函数调用顺序或私有字段。
//
// 通知没有 HTTP 写入口（Spec：通知不接受外部直接建），因此通知一律通过
// svc.Emit 这个「供 app/notify 编排层调用」的写入口产生，再用 HTTP 读路径观察。
//
// 刻意**不**挂 server 的会话中间件：业务域若为了测试去 import server，
// 就违反了「业务域不依赖框架适配层」的边界。会话身份用注入的 userIDOf 从
// 测试请求头读取（与 announcement 的 testViewer 同一手法）。

func init() { gin.SetMode(gin.TestMode) }

var fixedNow = time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return fixedNow }

// stepClock 每次 Now() 前进一分钟，用于证明「upsert 合并不刷新 createdAt」。
type stepClock struct {
	mu   sync.Mutex
	cur  time.Time
	step time.Duration
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.cur
	c.cur = c.cur.Add(c.step)
	return t
}

func nowPtr() *time.Time { t := fixedNow; return &t }

// ---------- fake 依赖 ----------

type userSet struct {
	mu sync.Mutex
	m  map[int64]bool
}

func (u *userSet) allow(id int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.m[id] = true
}

func (u *userSet) Exists(_ context.Context, id int64) (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.m[id], nil
}

type fakeLimiter struct {
	mu      sync.Mutex
	allow   bool
	err     error
	calls   int
	lastKey string
}

func (l *fakeLimiter) Allow(_ context.Context, key string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	l.lastKey = key
	return l.allow, l.err
}

func (l *fakeLimiter) snapshot() (int, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls, l.lastKey
}

// ---------- 装配 ----------

type testEnv struct {
	r     *gin.Engine
	svc   *message.Service
	repo  *pgstore.MessageMemoryRepo
	users *userSet
	lim   *fakeLimiter
}

func testUserIDOf(c *gin.Context) (int64, bool) {
	raw := c.GetHeader("X-Test-User")
	if raw == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func newTestServer(t *testing.T, clk platform.Clock) *testEnv {
	t.Helper()
	if clk == nil {
		clk = fixedClock{}
	}
	repo := pgstore.NewMessageMemoryRepo(func() time.Time { return fixedNow })
	users := &userSet{m: map[int64]bool{}}
	lim := &fakeLimiter{allow: true}
	svc := message.New(message.Options{
		Repo:       repo,
		Clock:      clk,
		UserExists: users.Exists,
		Limiter:    lim,
	})
	r := gin.New()
	message.Register(r, svc, testUserIDOf)
	return &testEnv{r: r, svc: svc, repo: repo, users: users, lim: lim}
}

// ---------- HTTP helper ----------

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

func doAs(t *testing.T, r *gin.Engine, method, path string, body any, userID int64) (int, map[string]any) {
	t.Helper()
	return doJSON(t, r, method, path, body, map[string]string{"X-Test-User": strconv.FormatInt(userID, 10)})
}

func itemsOf(body map[string]any) []map[string]any {
	raw, _ := body["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func idOf(m map[string]any) int64 { return int64(m["id"].(float64)) }

func emit(t *testing.T, svc *message.Service, recipient, actor int64, domain, objID, title, body string) error {
	t.Helper()
	return svc.Emit(context.Background(), message.EmitInput{
		RecipientID: recipient, ActorID: actor,
		SourceDomain: domain, SourceObjectID: objID,
		Title: title, Body: body,
	})
}

func mustEmit(t *testing.T, svc *message.Service, recipient, actor int64, domain, objID, title, body string) {
	t.Helper()
	if err := emit(t, svc, recipient, actor, domain, objID, title, body); err != nil {
		t.Fatalf("Emit 不应返回错误（编排层按旁路处理）：%v", err)
	}
}

func assertCode(t *testing.T, body map[string]any, want apperr.Code) {
	t.Helper()
	if body["code"] != string(want) {
		t.Fatalf("错误码 = %v, want %q；body=%v", body["code"], want, body)
	}
}

// ---------- 会话 / 归属 ----------

func TestHTTP_RequiresSession(t *testing.T) {
	e := newTestServer(t, nil)
	// 没注入会话身份 → handler fail-closed 一律 401，绝不默认放行 0 号用户。
	for _, path := range []string{"/messages", "/messages/unread-count"} {
		code, body := doJSON(t, e.r, http.MethodGet, path, nil, nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("%s 无会话应 401，得到 %d %v", path, code, body)
		}
		assertCode(t, body, apperr.CodeUnauthorized)
	}
}

func TestHTTP_PerUserIsolation(t *testing.T) {
	e := newTestServer(t, nil)
	// 通知：分别投递给 1 与 2（同来源、不同收件人 → 两行）。
	mustEmit(t, e.svc, 1, 9, "post", "like:post:88", "有人赞了你的帖子", "快照A")
	mustEmit(t, e.svc, 2, 9, "post", "like:post:88", "有人赞了你的帖子", "快照A")
	// 内部信：1 → 2。
	e.users.allow(2)
	code, body := doAs(t, e.r, http.MethodPost, "/messages", map[string]any{
		"recipientId": 2, "title": "你好", "body": "借本书",
	}, 1)
	if code != http.StatusCreated {
		t.Fatalf("创建内部信失败：%d %v", code, body)
	}

	code, body = doAs(t, e.r, http.MethodGet, "/messages", nil, 1)
	if code != http.StatusOK {
		t.Fatalf("读列表失败：%d %v", code, body)
	}
	got1 := itemsOf(body)
	if len(got1) != 1 {
		t.Fatalf("用户1 只应看到自己的 1 条通知，实际 %d 条：%v", len(got1), body)
	}
	if got1[0]["recipientId"].(float64) != 1 || got1[0]["kind"] != "notification" {
		t.Fatalf("用户1 看到的行不对：%v", got1[0])
	}

	code, body = doAs(t, e.r, http.MethodGet, "/messages", nil, 2)
	if code != http.StatusOK {
		t.Fatalf("读列表失败：%d %v", code, body)
	}
	got2 := itemsOf(body)
	if len(got2) != 2 {
		t.Fatalf("用户2 应看到自己的通知 + 内部信共 2 条，实际 %d 条：%v", len(got2), body)
	}
	for _, m := range got2 {
		if m["recipientId"].(float64) != 2 {
			t.Fatalf("用户2 的列表混入了他人消息：%v", m)
		}
	}
	// 实名可见：内部信 senderId 是当前会话用户 1（不是客户端自报）。
	var mail map[string]any
	for _, m := range got2 {
		if m["kind"] == "internal_mail" {
			mail = m
		}
	}
	if mail == nil {
		t.Fatal("用户2 应能看到内部信")
	}
	if mail["senderId"].(float64) != 1 {
		t.Fatalf("内部信发送者应为 1，得到 %v", mail["senderId"])
	}
}

func TestHTTP_MarkReadNonOwnerIs404(t *testing.T) {
	e := newTestServer(t, nil)
	mustEmit(t, e.svc, 1, 9, "post", "reply:1234", "有人回复了你", "快照")

	_, list := doAs(t, e.r, http.MethodGet, "/messages", nil, 1)
	id := idOf(itemsOf(list)[0])

	// 非本人读 → 404，且不泄露存在性（错误码与「不存在」一致）。
	code, body := doAs(t, e.r, http.MethodPatch, "/messages/"+strconv.FormatInt(id, 10)+"/read", map[string]any{}, 2)
	if code != http.StatusNotFound {
		t.Fatalf("非本人置已读应 404，得到 %d %v", code, body)
	}
	assertCode(t, body, apperr.CodeNotFound)

	// 且不能改动别人的数据：用户1 仍未读。
	_, cnt := doAs(t, e.r, http.MethodGet, "/messages/unread-count", nil, 1)
	if cnt["count"].(float64) != 1 {
		t.Fatalf("非本人读不应改动未读数，得到 %v", cnt)
	}

	// 本人可读，readAt 变为非 null。
	code, body = doAs(t, e.r, http.MethodPatch, "/messages/"+strconv.FormatInt(id, 10)+"/read", map[string]any{}, 1)
	if code != http.StatusOK {
		t.Fatalf("本人置已读应 200，得到 %d %v", code, body)
	}
	data := body["data"].(map[string]any)
	if data["readAt"] == nil {
		t.Fatalf("已读后 readAt 应非 null：%v", data)
	}
}

func TestHTTP_NonOwnerDeleteIs404(t *testing.T) {
	e := newTestServer(t, nil)
	mustEmit(t, e.svc, 1, 9, "post", "like:post:1", "x", "x")
	_, list := doAs(t, e.r, http.MethodGet, "/messages", nil, 1)
	id := idOf(itemsOf(list)[0])

	code, body := doAs(t, e.r, http.MethodDelete, "/messages/"+strconv.FormatInt(id, 10), nil, 2)
	if code != http.StatusNotFound {
		t.Fatalf("非本人删除应 404，得到 %d %v", code, body)
	}
	assertCode(t, body, apperr.CodeNotFound)
}

// ---------- 未读数 / 已读 / 软删 ----------

func TestHTTP_UnreadCountAndMarkRead(t *testing.T) {
	e := newTestServer(t, nil)
	mustEmit(t, e.svc, 1, 9, "post", "like:post:1", "赞1", "x")
	mustEmit(t, e.svc, 1, 9, "post", "like:post:2", "赞2", "x")
	e.users.allow(1)
	// 内部信发给「另一个自己」不行，发给 3。
	e.users.allow(3)
	if code, body := doAs(t, e.r, http.MethodPost, "/messages", map[string]any{
		"recipientId": 3, "title": "t", "body": "b",
	}, 1); code != http.StatusCreated {
		t.Fatalf("创建失败：%d %v", code, body)
	}
	// 给 1 也发一封（由 3 发给 1）。
	if code, body := doAs(t, e.r, http.MethodPost, "/messages", map[string]any{
		"recipientId": 1, "title": "t", "body": "b",
	}, 3); code != http.StatusCreated {
		t.Fatalf("创建失败：%d %v", code, body)
	}

	_, cnt := doAs(t, e.r, http.MethodGet, "/messages/unread-count", nil, 1)
	if cnt["count"].(float64) != 3 {
		t.Fatalf("用户1 应有 3 条未读（2 通知 + 1 内部信），得到 %v", cnt)
	}

	_, list := doAs(t, e.r, http.MethodGet, "/messages", nil, 1)
	firstID := idOf(itemsOf(list)[0])
	doAs(t, e.r, http.MethodPatch, "/messages/"+strconv.FormatInt(firstID, 10)+"/read", map[string]any{}, 1)

	_, cnt = doAs(t, e.r, http.MethodGet, "/messages/unread-count", nil, 1)
	if cnt["count"].(float64) != 2 {
		t.Fatalf("单条已读后应剩 2 条未读，得到 %v", cnt)
	}

	// 重复置已读幂等成功。
	if code, body := doAs(t, e.r, http.MethodPatch, "/messages/"+strconv.FormatInt(firstID, 10)+"/read", map[string]any{}, 1); code != http.StatusOK {
		t.Fatalf("重复置已读应幂等 200，得到 %d %v", code, body)
	}
	_, cnt = doAs(t, e.r, http.MethodGet, "/messages/unread-count", nil, 1)
	if cnt["count"].(float64) != 2 {
		t.Fatalf("重复置已读不应重复抵扣，得到 %v", cnt)
	}
}

func TestHTTP_ReadAllByCurrentFilter(t *testing.T) {
	e := newTestServer(t, nil)
	// 2 条通知 + 1 条内部信，全未读（都发给用户 1）。
	mustEmit(t, e.svc, 1, 9, "post", "like:post:1", "赞1", "x")
	mustEmit(t, e.svc, 1, 9, "post", "like:post:2", "赞2", "x")
	e.users.allow(1)
	if code, body := doAs(t, e.r, http.MethodPost, "/messages", map[string]any{
		"recipientId": 1, "title": "t", "body": "b",
	}, 2); code != http.StatusCreated {
		t.Fatalf("创建失败：%d %v", code, body)
	}

	// 按当前筛选条件（kind=notification）批量置已读。
	code, body := doAs(t, e.r, http.MethodPost, "/messages/read-all?kind=notification", map[string]any{}, 1)
	if code != http.StatusOK {
		t.Fatalf("read-all 失败：%d %v", code, body)
	}
	if body["updated"].(float64) != 2 {
		t.Fatalf("应只更新 2 条通知，得到 %v", body)
	}

	_, cnt := doAs(t, e.r, http.MethodGet, "/messages/unread-count", nil, 1)
	if cnt["count"].(float64) != 1 {
		t.Fatalf("内部信应仍未读（未按筛选置已读），得到 %v", cnt)
	}

	// 不带 kind → 把剩下那条内部信也置已读。
	_, body = doAs(t, e.r, http.MethodPost, "/messages/read-all", map[string]any{}, 1)
	if body["updated"].(float64) != 1 {
		t.Fatalf("应再更新 1 条内部信，得到 %v", body)
	}
	_, cnt = doAs(t, e.r, http.MethodGet, "/messages/unread-count", nil, 1)
	if cnt["count"].(float64) != 0 {
		t.Fatalf("全部已读后未读数应为 0，得到 %v", cnt)
	}
}

func TestHTTP_SoftDeleteNotCounted(t *testing.T) {
	e := newTestServer(t, nil)
	mustEmit(t, e.svc, 1, 9, "post", "like:post:1", "赞1", "x")
	mustEmit(t, e.svc, 1, 9, "post", "like:post:2", "赞2", "x")

	_, list := doAs(t, e.r, http.MethodGet, "/messages", nil, 1)
	id := idOf(itemsOf(list)[0])

	code, body := doAs(t, e.r, http.MethodDelete, "/messages/"+strconv.FormatInt(id, 10), nil, 1)
	if code != http.StatusOK {
		t.Fatalf("软删应 200，得到 %d %v", code, body)
	}
	// 软删不计未读：2 条未读删掉 1 条 → 1。
	_, cnt := doAs(t, e.r, http.MethodGet, "/messages/unread-count", nil, 1)
	if cnt["count"].(float64) != 1 {
		t.Fatalf("软删的未读消息不应计入未读数，得到 %v", cnt)
	}
	// 列表不再含该行。
	_, list = doAs(t, e.r, http.MethodGet, "/messages", nil, 1)
	if len(itemsOf(list)) != 1 {
		t.Fatalf("软删后列表应只剩 1 条，实际 %d 条", len(itemsOf(list)))
	}
	// 幂等：再删一次仍成功。
	if code, _ = doAs(t, e.r, http.MethodDelete, "/messages/"+strconv.FormatInt(id, 10), nil, 1); code != http.StatusOK {
		t.Fatalf("重复软删应幂等 200，得到 %d", code)
	}
	// 已删的消息不能再置已读（视为不存在）。
	if code, body := doAs(t, e.r, http.MethodPatch, "/messages/"+strconv.FormatInt(id, 10)+"/read", map[string]any{}, 1); code != http.StatusNotFound {
		t.Fatalf("已删消息置已读应 404，得到 %d %v", code, body)
	}
}

func TestHTTP_UnreadOnlyAndKindFilter(t *testing.T) {
	e := newTestServer(t, nil)
	// fail-closed：内部信的收件人必须存在。用户1要能收到内部信就得先 allow(1)。
	e.users.allow(1)
	e.users.allow(2)
	mustEmit(t, e.svc, 1, 9, "post", "like:post:1", "赞1", "x")
	doAs(t, e.r, http.MethodPost, "/messages", map[string]any{"recipientId": 2, "title": "t", "body": "b"}, 1)

	// 把通知置已读。
	// 用户 2 只应收到「用户1 → 用户2」的那一封内部信，看不到用户 1 的通知。
	_, list := doAs(t, e.r, http.MethodGet, "/messages", nil, 2)
	if got := itemsOf(list); len(got) != 1 {
		t.Fatalf("用户2 应只收到 1 封内部信，实际 %d 条", len(got))
	} else if got[0]["kind"] != "internal_mail" {
		t.Fatalf("用户2 只能看到内部信，实际 kind=%v", got[0]["kind"])
	}
	// 重新给用户1造一封内部信（由 2 发给 1）。
	doAs(t, e.r, http.MethodPost, "/messages", map[string]any{"recipientId": 1, "title": "t", "body": "b"}, 2)

	_, list = doAs(t, e.r, http.MethodGet, "/messages?unread=true", nil, 1)
	if len(itemsOf(list)) != 2 {
		t.Fatalf("仅未读过滤应返回 2 条，实际 %d 条", len(itemsOf(list)))
	}
	_, list = doAs(t, e.r, http.MethodGet, "/messages?kind=notification", nil, 1)
	if len(itemsOf(list)) != 1 {
		t.Fatalf("kind 过滤应只返回 1 条通知，实际 %d 条", len(itemsOf(list)))
	}
}

// ---------- 内部信校验 / 限频 ----------

func TestHTTP_InternalMailValidation(t *testing.T) {
	t.Run("通知不接受外部直接建", func(t *testing.T) {
		e := newTestServer(t, nil)
		e.users.allow(2)
		code, body := doAs(t, e.r, http.MethodPost, "/messages", map[string]any{
			"kind": "notification", "recipientId": 2, "title": "t", "body": "b",
		}, 1)
		if code != http.StatusBadRequest {
			t.Fatalf("传 kind=notification 应 400，得到 %d %v", code, body)
		}
		assertCode(t, body, apperr.CodeValidationFailed)
		// 且确实没有产生任何消息。
		_, cnt := doAs(t, e.r, http.MethodGet, "/messages/unread-count", nil, 2)
		if cnt["count"].(float64) != 0 {
			t.Fatal("被拒的通知不应落库")
		}
	})

	t.Run("收件人不存在 fail-closed", func(t *testing.T) {
		e := newTestServer(t, nil)
		code, body := doAs(t, e.r, http.MethodPost, "/messages", map[string]any{
			"recipientId": 999, "title": "t", "body": "b",
		}, 1)
		if code != http.StatusNotFound {
			t.Fatalf("收件人不存在应 404，得到 %d %v", code, body)
		}
		assertCode(t, body, apperr.CodeRecipientNotFound)
	})

	t.Run("不能发给自己", func(t *testing.T) {
		e := newTestServer(t, nil)
		e.users.allow(1)
		code, body := doAs(t, e.r, http.MethodPost, "/messages", map[string]any{
			"recipientId": 1, "title": "t", "body": "b",
		}, 1)
		if code != http.StatusBadRequest {
			t.Fatalf("发给自己应 400，得到 %d %v", code, body)
		}
		assertCode(t, body, apperr.CodeSelfSendForbidden)
	})

	t.Run("body 长度上限", func(t *testing.T) {
		e := newTestServer(t, nil)
		e.users.allow(2)
		code, body := doAs(t, e.r, http.MethodPost, "/messages", map[string]any{
			"recipientId": 2, "title": "t", "body": strings.Repeat("x", message.MaxBodyLen+1),
		}, 1)
		if code != http.StatusBadRequest {
			t.Fatalf("正文超长应 400，得到 %d %v", code, body)
		}
		assertCode(t, body, apperr.CodeValidationFailed)
	})

	t.Run("按发送者限频", func(t *testing.T) {
		e := newTestServer(t, nil)
		e.users.allow(2)
		e.lim.allow = false
		code, body := doAs(t, e.r, http.MethodPost, "/messages", map[string]any{
			"recipientId": 2, "title": "t", "body": "b",
		}, 1)
		if code != http.StatusTooManyRequests {
			t.Fatalf("限频应 429，得到 %d %v", code, body)
		}
		assertCode(t, body, apperr.CodeRateLimited)
		calls, key := e.lim.snapshot()
		if calls != 1 || key != message.MailLimitKey(1) {
			t.Fatalf("限流键应按发送者隔离：calls=%d key=%q", calls, key)
		}
	})

	t.Run("限流后端故障不回显内部细节", func(t *testing.T) {
		e := newTestServer(t, nil)
		e.users.allow(2)
		e.lim.err = errors.New("redis 连接被拒 secret=xyz")
		code, body := doAs(t, e.r, http.MethodPost, "/messages", map[string]any{
			"recipientId": 2, "title": "t", "body": "b",
		}, 1)
		if code != http.StatusInternalServerError {
			t.Fatalf("限流后端故障应 500，得到 %d %v", code, body)
		}
		if body["code"] != "internal" {
			t.Fatalf("非领域错误应统一 internal，得到 %v", body)
		}
		if strings.Contains(body["message"].(string), "secret") || strings.Contains(body["message"].(string), "redis") {
			t.Fatalf("错误响应不应回显内部细节：%v", body)
		}
	})

	t.Run("合法创建返回实名发送者", func(t *testing.T) {
		e := newTestServer(t, nil)
		e.users.allow(2)
		code, body := doAs(t, e.r, http.MethodPost, "/messages", map[string]any{
			"recipientId": 2, "title": "你好", "body": "正文",
		}, 1)
		if code != http.StatusCreated {
			t.Fatalf("合法创建应 201，得到 %d %v", code, body)
		}
		data := body["data"].(map[string]any)
		if data["kind"] != "internal_mail" || data["senderId"].(float64) != 1 {
			t.Fatalf("内部信形态与实名发送者不对：%v", data)
		}
		if data["hits"].(float64) != 1 {
			t.Fatalf("内部信 hits 应为 1，得到 %v", data["hits"])
		}
	})
}

// ---------- 通知 upsert 合并 / 并发 / 自发自收 ----------

func TestEmit_NotificationUpsertMergesHitsAndResetsUnread(t *testing.T) {
	// 用递进时钟，证明「合并保持首次 createdAt，不把老通知顶到最前」。
	e := newTestServer(t, &stepClock{cur: fixedNow, step: time.Minute})

	mustEmit(t, e.svc, 1, 9, "post", "like:post:88", "有人赞了你的帖子", "快照-第1次")
	_, list := doAs(t, e.r, http.MethodGet, "/messages", nil, 1)
	rows := itemsOf(list)
	if len(rows) != 1 {
		t.Fatalf("首次 Emit 应只产生 1 行，实际 %d 行", len(rows))
	}
	first := rows[0]
	if first["hits"].(float64) != 1 || first["readAt"] != nil {
		t.Fatalf("首次通知应 hits=1、未读：%v", first)
	}
	firstCreatedAt := first["createdAt"]
	id := idOf(first)

	// 用户把这条读掉了。
	if code, body := doAs(t, e.r, http.MethodPatch, "/messages/"+strconv.FormatInt(id, 10)+"/read", map[string]any{}, 1); code != http.StatusOK {
		t.Fatalf("置已读失败：%d %v", code, body)
	}
	_, cnt := doAs(t, e.r, http.MethodGet, "/messages/unread-count", nil, 1)
	if cnt["count"].(float64) != 0 {
		t.Fatalf("已读后未读数应为 0，得到 %v", cnt)
	}

	// 同一幂等键再次触发（另一人点赞同一帖）→ 合并，不新增行。
	mustEmit(t, e.svc, 1, 10, "post", "like:post:88", "有人赞了你的帖子", "快照-第2次")
	_, list = doAs(t, e.r, http.MethodGet, "/messages", nil, 1)
	rows = itemsOf(list)
	if len(rows) != 1 {
		t.Fatalf("同键重复触发应合并成 1 行，实际 %d 行：%v", len(rows), rows)
	}
	got := rows[0]
	if got["hits"].(float64) != 2 {
		t.Fatalf("合并后 hits 应累加到 2，得到 %v", got["hits"])
	}
	if got["readAt"] != nil {
		t.Fatalf("合并应把 readAt 重置为 null（变回未读），得到 %v", got["readAt"])
	}
	if got["body"] != "快照-第2次" {
		t.Fatalf("合并应刷新触发方写入的快照，得到 %v", got["body"])
	}
	if got["createdAt"] != firstCreatedAt {
		t.Fatalf("合并应保持首次 createdAt，got=%v want=%v", got["createdAt"], firstCreatedAt)
	}
	_, cnt = doAs(t, e.r, http.MethodGet, "/messages/unread-count", nil, 1)
	if cnt["count"].(float64) != 1 {
		t.Fatalf("合并后应重新计入未读，得到 %v", cnt)
	}

	// 不同幂等键（另一动作靶子）→ 另起一行。
	mustEmit(t, e.svc, 1, 9, "post", "reply:1234", "有人回复了你", "快照B")
	_, list = doAs(t, e.r, http.MethodGet, "/messages", nil, 1)
	if len(itemsOf(list)) != 2 {
		t.Fatalf("不同幂等键应产生 2 行，实际 %d 行", len(itemsOf(list)))
	}
}

func TestEmit_ConcurrentDoesNotCreateDuplicateRows(t *testing.T) {
	e := newTestServer(t, nil)
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// actor 恒 ≠ recipient，避免被自发自收过滤；同键并发。
			errs <- emit(t, e.svc, 1, 9, "post", "like:post:88", "有人赞了你的帖子", "快照")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发 Emit 不应返回错误：%v", err)
		}
	}

	_, list := doAs(t, e.r, http.MethodGet, "/messages", nil, 1)
	rows := itemsOf(list)
	if len(rows) != 1 {
		t.Fatalf("并发同键 Emit 应合并成 1 行，实际 %d 行：%v", len(rows), rows)
	}
	if rows[0]["hits"].(float64) != n {
		t.Fatalf("并发 %d 次应累加 hits=%d，得到 %v", n, n, rows[0]["hits"])
	}
	_, cnt := doAs(t, e.r, http.MethodGet, "/messages/unread-count", nil, 1)
	if cnt["count"].(float64) != 1 {
		t.Fatalf("并发合并后未读数应为 1，得到 %v", cnt)
	}
}

func TestEmit_SelfSendFilteredAtModelLayer(t *testing.T) {
	e := newTestServer(t, nil)
	// 决策-消息域模型.md D2：模型层 fail-closed 拒自发自收，静默丢弃不算失败。
	if err := emit(t, e.svc, 1, 1, "post", "like:post:88", "x", "x"); err != nil {
		t.Fatalf("自发自收应静默丢弃而非报错：%v", err)
	}
	_, cnt := doAs(t, e.r, http.MethodGet, "/messages/unread-count", nil, 1)
	if cnt["count"].(float64) != 0 {
		t.Fatalf("自发自收不应产生消息，得到 %v", cnt)
	}
	_, list := doAs(t, e.r, http.MethodGet, "/messages", nil, 1)
	if len(itemsOf(list)) != 0 {
		t.Fatal("自发自收不应产生行")
	}
}

func TestEmit_SnapshotIsStoredNotRenderedOnRead(t *testing.T) {
	// D1：body 是触发方写入时落库的快照，读路径不按 source 渲染。
	// 写入不同的两段正文，读回来应是写入时的原文，而不是被重新拼装。
	e := newTestServer(t, nil)
	mustEmit(t, e.svc, 1, 9, "post", "like:post:77", "标题快照", "正文快照<原始>")
	_, list := doAs(t, e.r, http.MethodGet, "/messages", nil, 1)
	got := itemsOf(list)[0]
	if got["title"] != "标题快照" || got["body"] != "正文快照<原始>" {
		t.Fatalf("读到的应是触发方落库的快照原文：%v", got)
	}
}

// ---------- 游标分页 ----------

func TestHTTP_CursorPaginationNoDuplicates(t *testing.T) {
	e := newTestServer(t, nil)
	for i := 0; i < 5; i++ {
		mustEmit(t, e.svc, 1, 9, "post", "like:post:"+strconv.Itoa(i), "t", "b")
	}
	seen := map[int64]bool{}
	cursor := ""
	pages := 0
	for {
		path := "/messages?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		code, body := doAs(t, e.r, http.MethodGet, path, nil, 1)
		if code != http.StatusOK {
			t.Fatalf("翻页失败：%d %v", code, body)
		}
		for _, it := range itemsOf(body) {
			if seen[idOf(it)] {
				t.Fatalf("翻页出现重复条目 id=%d", idOf(it))
			}
			seen[idOf(it)] = true
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

func TestHTTP_InvalidCursorRejected(t *testing.T) {
	e := newTestServer(t, nil)
	code, body := doAs(t, e.r, http.MethodGet, "/messages?cursor=!!!not-base64!!!", nil, 1)
	if code != http.StatusBadRequest {
		t.Fatalf("非法游标应 400，得到 %d %v", code, body)
	}
	assertCode(t, body, apperr.CodeInvalidCursor)
}
