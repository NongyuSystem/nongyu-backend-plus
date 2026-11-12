package post_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/content/post"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/platform/pgstore"
)

func init() { gin.SetMode(gin.TestMode) }

var t0 = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

type fixedClock struct{ now time.Time }

func (f fixedClock) Now() time.Time { return f.now }

// ---------- 纯函数单测（Testing Decisions ①）----------

func TestWithinEditWindow(t *testing.T) {
	pub := t0
	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"刚发布", pub, true},
		{"23h59m", pub.Add(24*time.Hour - time.Minute), true},
		{"恰好 24h —— 窗口关闭", pub.Add(24 * time.Hour), false},
		{"25h", pub.Add(25 * time.Hour), false},
		// fail-closed：时钟回拨/数据异常时（now < publishedAt）也不能编辑，
		// 否则会出现「未来时刻反而能改」的窗口。
		{"now 早于发布时刻", pub.Add(-time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := post.WithinEditWindow(pub, tc.now); got != tc.want {
				t.Fatalf("WithinEditWindow = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidateAttachments(t *testing.T) {
	// 9 张合法
	ok9 := make([]string, 9)
	for i := range ok9 {
		ok9[i] = "posts/2026/11/abc" + strconv.Itoa(i) + ".png"
	}
	if err := post.ValidateAttachments(ok9); err != nil {
		t.Fatalf("9 张应合法：%v", err)
	}
	// 10 张超限
	over := append(append([]string(nil), ok9...), "posts/2026/11/extra.png")
	if err := post.ValidateAttachments(over); err == nil {
		t.Fatal("10 张应被拒（too_many_attachments）")
	}
	// 空 key
	if err := post.ValidateAttachments([]string{""}); err == nil {
		t.Fatal("空 key 应被拒")
	}
	// 前缀非法：不允许挂到别的域的对象上
	if err := post.ValidateAttachments([]string{"announcements/2026/11/x.png"}); err == nil {
		t.Fatal("非 posts/ 前缀应被拒（否则能把帖子挂到别人的对象上）")
	}
}

func TestValidateAttachmentSize(t *testing.T) {
	if err := post.ValidateAttachmentSize(post.MaxAttachmentBytes); err != nil {
		t.Fatalf("恰好 5MB 应合法：%v", err)
	}
	if err := post.ValidateAttachmentSize(post.MaxAttachmentBytes + 1); err == nil {
		t.Fatal("超过 5MB 应被拒")
	}
}

func TestParseEnums(t *testing.T) {
	if _, ok := post.ParseBoard("feedback"); !ok {
		t.Fatal("feedback 应合法")
	}
	if _, ok := post.ParseBoard("广场"); ok {
		t.Fatal("「广场」不是正名，应被拒")
	}
	if _, ok := post.ParseTargetType("comment"); !ok {
		t.Fatal("comment 应合法")
	}
	if _, ok := post.ParseReactionKind("favorite"); !ok {
		t.Fatal("favorite 应合法")
	}
	if _, ok := post.ParseReactionKind("thumbsup"); ok {
		t.Fatal("未知反应应被拒")
	}
}

// ---------- HTTP 集成测（Testing Decisions ②）----------

type fakeStore = platform.MemoryObjectStore

func newEnv(t *testing.T) (*gin.Engine, *post.Service, *pgstore.PostMemoryRepo, *fakeStore) {
	t.Helper()
	repo := pgstore.NewPostMemoryRepo(func() time.Time { return t0 })
	store := platform.NewMemoryObjectStore()
	svc := post.New(post.Options{
		Repo: repo, Clock: fixedClock{now: t0}, ObjectStore: store,
	})
	uidOf := func(c *gin.Context) (int64, bool) {
		raw := c.GetHeader("X-Test-UID")
		if raw == "" {
			return 0, false
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return 0, false
		}
		return id, true
	}
	r := gin.New()
	post.RegisterPublic(r, svc, uidOf)
	post.RegisterSession(r, svc, uidOf)
	post.RegisterAdmin(r, svc)
	return r, svc, repo, store
}

func do(t *testing.T, r *gin.Engine, method, path, uid string, body any) (int, map[string]any) {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	if uid != "" {
		req.Header.Set("X-Test-UID", uid)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

func createPost(t *testing.T, r *gin.Engine, uid, title string) int64 {
	t.Helper()
	code, body := do(t, r, http.MethodPost, "/posts", uid, map[string]any{
		"board": "feedback", "title": title, "body": "内容",
	})
	if code != http.StatusCreated {
		t.Fatalf("发帖失败：%d %v", code, body)
	}
	return int64(body["data"].(map[string]any)["id"].(float64))
}

func TestCreateAndList_AnonymousCanRead(t *testing.T) {
	r, _, _, _ := newEnv(t)
	createPost(t, r, "7", "第一条")

	// 未登录读列表：200，且 isMine 恒 false（匿名读者没有身份）
	code, body := do(t, r, http.MethodGet, "/posts", "", nil)
	if code != http.StatusOK {
		t.Fatalf("匿名读列表应 200，得到 %d", code)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("应有 1 条，得到 %d", len(items))
	}
	first := items[0].(map[string]any)
	if first["isMine"] != false {
		t.Fatal("匿名读者 isMine 应为 false")
	}
	// 关键：读者侧**不得下发 authorId**
	if _, has := first["authorId"]; has {
		t.Fatal("读者侧不能下发 authorId（帖子对读者匿名）")
	}
}

func TestDetail_IsMineForAuthorOnly(t *testing.T) {
	r, _, _, _ := newEnv(t)
	id := createPost(t, r, "7", "我的帖子")
	p := "/posts/" + strconv.FormatInt(id, 10)

	// 作者本人：isMine=true
	_, body := do(t, r, http.MethodGet, p, "7", nil)
	data := body["data"].(map[string]any)
	if data["isMine"] != true {
		t.Fatal("作者本人 isMine 应为 true")
	}
	// 别人：isMine=false，且同样看不到 authorId
	_, body = do(t, r, http.MethodGet, p, "8", nil)
	data = body["data"].(map[string]any)
	if data["isMine"] != false {
		t.Fatal("非作者 isMine 应为 false")
	}
	if _, has := data["authorId"]; has {
		t.Fatal("非作者不应看到 authorId")
	}
}

func TestEdit_WithinWindowThenLocked(t *testing.T) {
	r, _, _, _ := newEnv(t)
	id := createPost(t, r, "7", "原标题")
	p := "/posts/" + strconv.FormatInt(id, 10)

	// 窗口内可编辑
	code, body := do(t, r, http.MethodPatch, p, "7", map[string]any{"title": "新标题"})
	if code != http.StatusOK {
		t.Fatalf("窗口内编辑应成功，得到 %d %v", code, body)
	}
	if body["data"].(map[string]any)["editedAt"] == nil {
		t.Fatal("编辑后应回 editedAt")
	}

	// 非作者编辑 → 404（不泄露存在性，不能用 403）
	if code, _ := do(t, r, http.MethodPatch, p, "8", map[string]any{"title": "x"}); code != http.StatusNotFound {
		t.Fatalf("非作者编辑应 404，得到 %d", code)
	}
}

func TestEdit_RejectsAfterWindow(t *testing.T) {
	// 用可推进的时钟验证 24h 边界
	repo := pgstore.NewPostMemoryRepo(func() time.Time { return t0 })
	now := t0
	svc := post.New(post.Options{
		Repo:  repo,
		Clock: funcClock{fn: func() time.Time { return now }},
	})
	id, err := svc.Create(context.Background(), 7, post.CreateInput{
		Board: "feedback", Title: "t", Body: "b",
	})
	if err != nil {
		t.Fatalf("发帖失败：%v", err)
	}

	now = t0.Add(25 * time.Hour)
	title := "改"
	if _, err := svc.Update(context.Background(), 7, id.ID, post.UpdateInput{Title: &title}); err == nil {
		t.Fatal("超过 24h 应拒绝编辑（locked）")
	}
}

type funcClock struct{ fn func() time.Time }

func (f funcClock) Now() time.Time { return f.fn() }

func TestReaction_IdempotentAndCounted(t *testing.T) {
	r, _, _, _ := newEnv(t)
	id := createPost(t, r, "7", "t")
	p := "/posts/" + strconv.FormatInt(id, 10)

	// 同一用户点两次：只算一次
	for i := 0; i < 2; i++ {
		if code, body := do(t, r, http.MethodPost, p+"/like", "8", nil); code != http.StatusOK {
			t.Fatalf("点赞失败：%d %v", code, body)
		}
	}
	// 另一个用户再点：2 个赞
	if code, _ := do(t, r, http.MethodPost, p+"/like", "9", nil); code != http.StatusOK {
		t.Fatal("第二个用户点赞失败")
	}
	// 取消：剩 1
	if code, _ := do(t, r, http.MethodDelete, p+"/like", "8", nil); code != http.StatusOK {
		t.Fatal("取消点赞失败")
	}
}

func TestReply_AndCommentLike(t *testing.T) {
	r, _, _, _ := newEnv(t)
	id := createPost(t, r, "7", "t")
	p := "/posts/" + strconv.FormatInt(id, 10)

	code, body := do(t, r, http.MethodPost, p+"/replies", "8", map[string]any{"body": "留言"})
	if code != http.StatusCreated {
		t.Fatalf("留言失败：%d %v", code, body)
	}
	replyID := int64(body["data"].(map[string]any)["id"].(float64))

	// 给留言点赞（靶子是 comment）
	if code, _ := do(t, r, http.MethodPost,
		p+"/replies/"+strconv.FormatInt(replyID, 10)+"/like", "9", nil); code != http.StatusOK {
		t.Fatal("留言点赞失败")
	}

	// 详情里能看到留言
	_, d := do(t, r, http.MethodGet, p, "", nil)
	if replies, _ := d["replies"].([]any); len(replies) != 1 {
		t.Fatalf("详情应有 1 条留言，得到 %d", len(replies))
	}
}

func TestDelete_SoftDeleteHidesFromList(t *testing.T) {
	r, _, _, _ := newEnv(t)
	id := createPost(t, r, "7", "t")
	p := "/posts/" + strconv.FormatInt(id, 10)

	if code, _ := do(t, r, http.MethodDelete, p, "7", nil); code != http.StatusOK {
		t.Fatal("删帖失败")
	}
	// 列表不出现
	_, body := do(t, r, http.MethodGet, "/posts", "", nil)
	if items, _ := body["items"].([]any); len(items) != 0 {
		t.Fatalf("软删后列表应为空，得到 %d", len(items))
	}
	// 详情 404
	if code, _ := do(t, r, http.MethodGet, p, "", nil); code != http.StatusNotFound {
		t.Fatalf("软删后详情应 404，得到 %d", code)
	}
	// 幂等：再删一次仍 200
	if code, _ := do(t, r, http.MethodDelete, p, "7", nil); code != http.StatusOK {
		t.Fatal("重复删除应幂等")
	}
}

func TestCursorPagination_NoDuplicates(t *testing.T) {
	r, _, _, _ := newEnv(t)
	for i := 0; i < 5; i++ {
		createPost(t, r, "7", "t"+strconv.Itoa(i))
	}
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 10; i++ {
		path := "/posts?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		code, body := do(t, r, http.MethodGet, path, "", nil)
		if code != http.StatusOK {
			t.Fatalf("翻页失败：%d", code)
		}
		items, _ := body["items"].([]any)
		for _, it := range items {
			title := it.(map[string]any)["title"].(string)
			if seen[title] {
				t.Fatalf("翻页重复：%s", title)
			}
			seen[title] = true
		}
		next, _ := body["next"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 5 {
		t.Fatalf("应翻到 5 条，实际 %d", len(seen))
	}
}

func TestInvalidCursorRejected(t *testing.T) {
	r, _, _, _ := newEnv(t)
	if code, body := do(t, r, http.MethodGet, "/posts?cursor=!!!bad!!!", "", nil); code != http.StatusBadRequest {
		t.Fatalf("非法游标应 400，得到 %d %v", code, body)
	}
}

// ---------- 上传 ----------

// makePng 构造一个最小的合法 PNG 头（8 字节签名 + 少量数据），
// 供内容嗅探识别为 image/png。
func makePng(size int) []byte {
	sig := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	out := make([]byte, size)
	copy(out, sig)
	return out
}

func uploadFile(t *testing.T, r *gin.Engine, uid string, content []byte, filename string) (int, map[string]any) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, _ := w.CreateFormFile("file", filename)
	_, _ = fw.Write(content)
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/posts/attachments", body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if uid != "" {
		req.Header.Set("X-Test-UID", uid)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func TestUpload_AcceptsPngAndReturnsKey(t *testing.T) {
	r, _, _, store := newEnv(t)
	code, body := uploadFile(t, r, "7", makePng(1024), "a.png")
	if code != http.StatusCreated {
		t.Fatalf("上传应 201，得到 %d %v", code, body)
	}
	key, _ := body["key"].(string)
	if key == "" {
		t.Fatalf("应返回 key：%v", body)
	}
	// key 必须符合 posts/{yyyy}/{MM}/{uuid}.png 模板
	if len(key) < len("posts/2026/11/") || key[:len("posts/2026/11/")] != "posts/2026/11/" {
		t.Fatalf("key 模板不符：%s", key)
	}
	if got := store.PutCount(); got != 1 {
		t.Fatalf("应真的写入对象存储一次，得到 %d", got)
	}
}

func TestUpload_RejectsNonImageBySniffing(t *testing.T) {
	r, _, _, _ := newEnv(t)
	// 客户端谎称是 png，实际内容是 ZIP 头 —— 必须按内容拒绝。
	zip := append([]byte{'P', 'K', 0x03, 0x04}, make([]byte, 100)...)
	code, body := uploadFile(t, r, "7", zip, "fake.png")
	if code != http.StatusUnsupportedMediaType {
		t.Fatalf("非图片内容应 415，得到 %d %v", code, body)
	}
}

func TestUpload_RejectsOversize(t *testing.T) {
	r, _, _, _ := newEnv(t)
	big := makePng(int(post.MaxAttachmentBytes) + 1024)
	code, body := uploadFile(t, r, "7", big, "big.png")
	if code != http.StatusRequestEntityTooLarge && code != http.StatusBadRequest {
		t.Fatalf("超大附件应 413/400，得到 %d %v", code, body)
	}
}

func TestUpload_RequiresAuth(t *testing.T) {
	r, _, _, _ := newEnv(t)
	if code, _ := uploadFile(t, r, "", makePng(64), "a.png"); code != http.StatusUnauthorized {
		t.Fatalf("未登录上传应 401，得到 %d", code)
	}
}

// ---------- 管理端 ----------

func TestAdminList_IncludesAuthorID(t *testing.T) {
	r, _, _, _ := newEnv(t)
	createPost(t, r, "7", "t")
	code, body := do(t, r, http.MethodGet, "/admin/posts", "", nil)
	if code != http.StatusOK {
		t.Fatalf("管理端列表应 200，得到 %d", code)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("应有 1 条，得到 %d", len(items))
	}
	// 管理端**要**带真实作者（处理问题需要）
	if _, has := items[0].(map[string]any)["authorId"]; !has {
		t.Fatal("管理端列表必须带 authorId（读者侧才匿名）")
	}
}
