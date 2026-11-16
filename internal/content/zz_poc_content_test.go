// zz_poc_content_test.go —— 安全审计 PoC → 修复后的回归测试。
//
// 这两个 0-day 由本文件（原 RED 版本）实证，修复后翻成 GREEN 回归断言：
//   - 发现 1（P1）：引用他人对象 key + 删自己帖子清掉他人附件。
//     修复：上传登记 owner；Create/Update 校验 key 归属；cleanupAttachments 只删
//     属主匹配、查不到归属默认不删（fail-closed）。
//   - 发现 2（P2）：留言点赞接口忽略 URL :id，可越权对全站 replyId 点赞。
//     修复：reactCommentHandler 解析 :id，ReactOnComment → ensureTarget 校验
//     r.PostID == :id，不匹配返回 not_found(404)。
//
// 运行：go test ./internal/content/ -run 'TestRegression|TestPOC' -v -count=1
// （全量：go test ./internal/content/... -count=1）
package content_test

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

var pocNow = time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)

type pocClock struct{}

func (pocClock) Now() time.Time { return pocNow }

// captureStore 在真实内存存储之上记录每次 Delete 的 key，用来确定性地观察
// cleanupAttachments（异步 goroutine）到底删了哪些对象。
type captureStore struct {
	*platform.MemoryObjectStore
	deleted chan string
}

func (s *captureStore) Delete(ctx context.Context, key string) error {
	err := s.MemoryObjectStore.Delete(ctx, key)
	if err == nil {
		select {
		case s.deleted <- key:
		default:
		}
	}
	return err
}

func newPocEnv(t *testing.T) (*gin.Engine, *captureStore) {
	t.Helper()
	repo := pgstore.NewPostMemoryRepo(func() time.Time { return pocNow })
	store := &captureStore{
		MemoryObjectStore: platform.NewMemoryObjectStore(),
		deleted:           make(chan string, 16),
	}
	svc := post.New(post.Options{Repo: repo, Clock: pocClock{}, ObjectStore: store})
	// 模拟「鉴权中间件已把会话身份写进 context」：业务域只认这个注入函数。
	uidOf := func(c *gin.Context) (int64, bool) {
		id, err := strconv.ParseInt(c.GetHeader("X-POC-UID"), 10, 64)
		return id, err == nil && id > 0
	}
	r := gin.New()
	post.RegisterPublic(r, svc, uidOf)
	post.RegisterSession(r, svc, uidOf)
	return r, store
}

func pocDo(t *testing.T, r *gin.Engine, method, path, uid string, body any) (int, map[string]any) {
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
		req.Header.Set("X-POC-UID", uid)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

func pocUpload(t *testing.T, r *gin.Engine, uid string) string {
	t.Helper()
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0, 0, 0, 0, 0}
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, _ := w.CreateFormFile("file", "a.png")
	_, _ = fw.Write(png)
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/posts/attachments", body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-POC-UID", uid)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("上传应 201，得到 %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Key == "" {
		t.Fatalf("上传未返回 key：%s", rec.Body.String())
	}
	return out.Key
}

// TestRegression_CannotReferenceOthersAttachment
//
// 回归（修复后应为 GREEN）：
//  1. 受害者上传对象并正常发帖引用它；
//  2. 攻击者把自己的帖子 attachments 填成受害者的 key —— 归属校验应拒（403）；
//  3. 攻击者发一条空附件帖子再删掉它 —— 受害者对象必须仍然存在；
//     且 cleanupAttachments 不得删除受害者的 key（fail-closed）。
func TestRegression_CannotReferenceOthersAttachment(t *testing.T) {
	r, store := newPocEnv(t)

	// 受害者 100 上传并发布带图帖子（owner==作者，合法引用）。
	victimKey := pocUpload(t, r, "100")
	code, body := pocDo(t, r, http.MethodPost, "/posts", "100", map[string]any{
		"board": "feedback", "title": "受害者帖子", "body": "x",
		"attachments": []string{victimKey},
	})
	if code != http.StatusCreated {
		t.Fatalf("受害者本人引用自己的附件应 201，得到 %d %v", code, body)
	}
	if _, err := store.Stat(context.Background(), victimKey); err != nil {
		t.Fatalf("预置：受害者对象应存在：%v", err)
	}

	// 攻击者 200 引用受害者的 key：Create 的归属校验应拦下（403 forbidden）。
	code, body = pocDo(t, r, http.MethodPost, "/posts", "200", map[string]any{
		"board": "feedback", "title": "攻击者帖子", "body": "y",
		"attachments": []string{victimKey},
	})
	if code == http.StatusCreated {
		t.Fatalf("【回归失败】攻击者仍能引用他人对象 key 发帖（越权破坏未修复）：%v", body)
	}
	if code != http.StatusForbidden {
		t.Fatalf("引用他人附件应 forbidden(403)，得到 %d %v", code, body)
	}

	// 攻击者发一条不带附件的帖子再删除（模拟「删自己帖子」这一动作）。
	code, body = pocDo(t, r, http.MethodPost, "/posts", "200", map[string]any{
		"board": "feedback", "title": "攻击者空帖", "body": "z",
	})
	if code != http.StatusCreated {
		t.Fatalf("攻击者发空附件帖应 201，得到 %d %v", code, body)
	}
	attackPostID := int64(body["data"].(map[string]any)["id"].(float64))
	if code, _ = pocDo(t, r, http.MethodDelete,
		"/posts/"+strconv.FormatInt(attackPostID, 10), "200", nil); code != http.StatusOK {
		t.Fatalf("删自己帖子应 200，得到 %d", code)
	}

	// 等异步 cleanupAttachments goroutine 有机会跑完，再确认受害者对象仍在、且未被删。
	time.Sleep(120 * time.Millisecond)
	if _, err := store.Stat(context.Background(), victimKey); err != nil {
		t.Fatalf("【回归失败】删攻击者自己的帖子后，受害者对象 %q 竟然被清掉了：%v", victimKey, err)
	}
	select {
	case deletedKey := <-store.deleted:
		t.Fatalf("【回归失败】攻击者删自己帖子触发了对对象 %q 的清理（应无清理发生）", deletedKey)
	default:
		// 通道为空：没有任何删除 —— 符合预期。
	}
}

// TestRegression_OwnerDeleteStillCleansOwnAttachment
//
// 回归：修复不能「一刀切全不删」。属主本人删自己的帖子时，自己上传的对象仍应
// 被正常清理，否则修复会误伤既有功能 / 留下孤儿对象。
func TestRegression_OwnerDeleteStillCleansOwnAttachment(t *testing.T) {
	r, store := newPocEnv(t)

	key := pocUpload(t, r, "100")
	code, body := pocDo(t, r, http.MethodPost, "/posts", "100", map[string]any{
		"board": "feedback", "title": "带图帖", "body": "x",
		"attachments": []string{key},
	})
	if code != http.StatusCreated {
		t.Fatalf("发帖应 201，得到 %d %v", code, body)
	}
	postID := int64(body["data"].(map[string]any)["id"].(float64))

	if code, _ = pocDo(t, r, http.MethodDelete,
		"/posts/"+strconv.FormatInt(postID, 10), "100", nil); code != http.StatusOK {
		t.Fatalf("删帖应 200，得到 %d", code)
	}

	select {
	case deletedKey := <-store.deleted:
		if deletedKey != key {
			t.Fatalf("清理删了非预期 key %q，期望 %q", deletedKey, key)
		}
		// 属主本人删除，自己的对象被清理 —— 正常功能未被误伤。
	case <-time.After(2 * time.Second):
		t.Fatalf("属主删自己帖子后，自己的附件对象应被清理，但没观察到 Delete 调用")
	}
}

// TestRegression_CommentLikeChecksPostID
//
// 回归（修复后应为 GREEN）：
//   - 用与留言不匹配的 :id（甚至不存在的帖子）点赞 → 404 not_found；
//   - 用正确的 :id 点赞 → 200（正常功能不受影响）。
func TestRegression_CommentLikeChecksPostID(t *testing.T) {
	r, _ := newPocEnv(t)

	code, body := pocDo(t, r, http.MethodPost, "/posts", "100", map[string]any{
		"board": "feedback", "title": "t", "body": "b",
	})
	if code != http.StatusCreated {
		t.Fatalf("预置：发帖应 201，得到 %d %v", code, body)
	}
	postID := int64(body["data"].(map[string]any)["id"].(float64))

	code, rbody := pocDo(t, r, http.MethodPost,
		"/posts/"+strconv.FormatInt(postID, 10)+"/replies", "100",
		map[string]any{"body": "一条留言"})
	if code != http.StatusCreated {
		t.Fatalf("预置：留言应 201，得到 %d %v", code, rbody)
	}
	replyID := int64(rbody["data"].(map[string]any)["id"].(float64))

	// 攻击者 200 用一个与留言不存在的帖子 id 点赞 → 必须 404。
	bogus := "/posts/999999999/replies/" + strconv.FormatInt(replyID, 10) + "/like"
	code, lbody := pocDo(t, r, http.MethodPost, bogus, "200", nil)
	if code == http.StatusOK {
		t.Fatalf("【回归失败】POST %s 仍返回 200：父资源 :id 校验未生效，越权 IDOR 未修复", bogus)
	}
	if code != http.StatusNotFound {
		t.Fatalf("父资源不匹配应 404(not_found)，得到 %d %v", code, lbody)
	}

	// 正确的父资源 id → 点赞成功（正常功能未破坏）。
	good := "/posts/" + strconv.FormatInt(postID, 10) + "/replies/" + strconv.FormatInt(replyID, 10) + "/like"
	if code, lbody = pocDo(t, r, http.MethodPost, good, "200", nil); code != http.StatusOK {
		t.Fatalf("用正确 :id 给留言点赞应 200，得到 %d %v", code, lbody)
	}
}
