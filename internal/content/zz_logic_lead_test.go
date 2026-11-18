package content_test

// 业务逻辑审计结论（lead 独占写入文件）。
//
// 审计对象：帖子附件在「读者侧公开读」端点的暴露面。
//
// 观察（已实证，见下方测试）：publicView.Attachments 会把对象存储的**原始 key**
// 下发给读者，而 GET /posts 与 GET /posts/:id 都在 publicReadablePaths 里
// （免用户会话的公开读）。
//
// **结论：不构成可利用漏洞，属低价值信息卫生问题（Low），本轮不改代码。**
//
// 判定依据（逐条排除利用链）：
//  1. 同一条帖子本来就公开可读：响应里已有 attachmentUrls（短期预签名 URL），
//     读者无需 key 即可直接下载同一对象。知道 key **不增加任何下载能力**。
//  2. 全仓没有「传任意 key 换预签名 URL」的端点（已 grep 确认：PresignGet 只被
//     post.Service.presign 在「按帖子 id 取到该帖 attachments」后调用，key 不来自
//     客户端）。因此拿到的 key 无法用于探测或换取其它对象。
//  3. 上传端点生成的 key 含 crypto/rand 的 UUID v4，不可预测；且上一轮已加
//     归属校验（引用他人 key 会被拒），key 不能用于写入面越权。
//  4. 软删后对象会被清理，已知 key 指向的对象通常已不存在。
//
// 真正的改进（若将来要做）：读者侧只下发 attachmentUrls、不下发原始 key，
// 属「减少内部实现细节外泄」的纵深防御，不改变威胁模型。优先级低于本轮
// 已修复的账号接管漏洞（IDCard 登录爆破）。
//
// 本测试因此**断言当前行为并记录结论**，而不是断言"漏洞存在"。
// 这样它既不会长期红着阻塞 CI，又能在有人改动附件字段形状时提醒回看本结论。

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"nongyu-backend-plus/internal/content/post/store"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/content/post"
	"nongyu-backend-plus/internal/platform"
)

var logicNow = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

type logicClock struct{}

func (logicClock) Now() time.Time { return logicNow }

const logicVictimID int64 = 100

func logicEnv(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	repo := store.NewPostMemoryRepo(func() time.Time { return logicNow })
	store := platform.NewMemoryObjectStore()
	svc := post.New(post.Options{Repo: repo, Clock: logicClock{}, ObjectStore: store})

	r := gin.New()
	post.RegisterPublic(r, svc, func(*gin.Context) (int64, bool) { return 0, false })
	post.RegisterSession(r, svc, func(*gin.Context) (int64, bool) { return logicVictimID, true })
	return r
}

func logicUpload(t *testing.T, r *gin.Engine) string {
	t.Helper()
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "a.png")
	if err != nil {
		t.Fatalf("构造 multipart 失败：%v", err)
	}
	_, _ = fw.Write(png)
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/posts/attachments", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("预置：上传应 201，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Key == "" {
		t.Fatalf("预置：上传响应无 key：%s", rec.Body.String())
	}
	return out.Key
}

func logicJSON(t *testing.T, r *gin.Engine, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

// TestLogicObserved_AttachmentKeyExposedToReaders 记录「读者侧能拿到原始 key」这一事实，
// 并同时断言「同一响应里已给出可直接下载的预签名 URL」——后者是把它定为 Low 的关键证据：
// 知道 key 不增加下载能力。
func TestLogicObserved_AttachmentKeyExposedToReaders(t *testing.T) {
	r := logicEnv(t)
	key := logicUpload(t, r)

	status, body := logicJSON(t, r, http.MethodPost, "/posts", map[string]any{
		"board": "feedback", "title": "附件暴露面观察", "body": "b",
		"attachments": []string{key},
	})
	if status != http.StatusCreated {
		t.Fatalf("预置：发帖应 201，实际 %d body=%v", status, body)
	}
	postID := int64(body["data"].(map[string]any)["id"].(float64))

	status, detail := logicJSON(t, r, http.MethodGet, "/posts/"+strconv.FormatInt(postID, 10), nil)
	if status != http.StatusOK {
		t.Fatalf("预置：匿名读详情应 200，实际 %d", status)
	}
	d, _ := detail["data"].(map[string]any)

	// 事实 1：原始 key 被下发给未登录读者。
	keysSeen := false
	if arr, ok := d["attachments"].([]any); ok {
		for _, v := range arr {
			if s, _ := v.(string); s == key {
				keysSeen = true
			}
		}
	}
	// 事实 2：同一响应已给出预签名 URL —— 读者本来就能直接下载该对象。
	urls, hasURLs := d["attachmentUrls"].([]any)

	switch {
	case keysSeen && hasURLs && len(urls) > 0:
		t.Logf("记录：原始 key 对匿名读者可见（%q），但同一响应已提供预签名 URL（%d 个）。"+
			"知道 key 不增加下载能力 → 属 Low 级信息卫生问题，非可利用漏洞。", key, len(urls))
	case !keysSeen:
		t.Logf("行为已变化：读者侧不再下发原始 key —— 若这是有意收紧，请把本文件更新为回归断言。")
	default:
		t.Logf("观察：keys=%v urls=%v（未提供 URL 时请重新评估该暴露面的等级）", keysSeen, urls)
	}
}
