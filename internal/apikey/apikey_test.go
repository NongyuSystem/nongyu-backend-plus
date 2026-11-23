package apikey_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/apikey"
	"nongyu-backend-plus/internal/apikey/store"
	"nongyu-backend-plus/internal/platform"
)

func init() { gin.SetMode(gin.TestMode) }

type fixedClock struct{ now time.Time }

func (f fixedClock) Now() time.Time { return f.now }

func newEnv(t *testing.T) (*gin.Engine, *apikey.Service) {
	t.Helper()
	repo := store.NewKeyMemoryRepo()
	svc := apikey.New(apikey.Options{
		Repo:   repo,
		Cipher: platform.NewAESCipher("test-key-for-apikey-pool"),
		Clock:  fixedClock{now: time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)},
	})
	r := gin.New()
	apikey.RegisterPublic(r, svc)
	apikey.RegisterAdmin(r, svc)
	return r, svc
}

func do(t *testing.T, r *gin.Engine, method, path string, body any) (int, map[string]any) {
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
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

// TestAcquire_ReturnsKeyAndRotates 取用能拿到 key，且多把时轮换。
func TestAcquire_ReturnsKeyAndRotates(t *testing.T) {
	r, _ := newEnv(t)

	// 录两把 android key。
	for _, k := range []string{"amap-key-AAAA1111", "amap-key-BBBB2222"} {
		if code, body := do(t, r, http.MethodPost, "/admin/keys/amap", map[string]any{
			"platform": "android", "key": k,
		}); code != http.StatusCreated {
			t.Fatalf("录入失败：%d %v", code, body)
		}
	}

	// 连续取四次，应看到两把交替（轮询）。
	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		code, body := do(t, r, http.MethodPost, "/keys/amap/acquire", map[string]any{"platform": "android"})
		if code != http.StatusOK {
			t.Fatalf("取用失败：%d %v", code, body)
		}
		k, _ := body["key"].(string)
		seen[k]++
	}
	if len(seen) != 2 {
		t.Fatalf("轮询应覆盖两把 key，实际只用了 %d 把：%v", len(seen), seen)
	}
	for k, n := range seen {
		if n != 2 {
			t.Fatalf("轮询应均匀（各 2 次），key %q 用了 %d 次", k, n)
		}
	}
}

// TestAcquire_DoesNotLeakInternals 取用响应不含池的内部结构。
func TestAcquire_DoesNotLeakInternals(t *testing.T) {
	r, _ := newEnv(t)
	do(t, r, http.MethodPost, "/admin/keys/amap", map[string]any{
		"platform": "android", "key": "amap-key-SECRET9999",
	})
	_, body := do(t, r, http.MethodPost, "/keys/amap/acquire", map[string]any{"platform": "android"})

	for _, forbidden := range []string{"id", "usedCount", "cipher", "lastUsedAt"} {
		if _, has := body[forbidden]; has {
			t.Fatalf("取用响应不该含 %q：%v", forbidden, body)
		}
	}
	// hint 只能是后四位，不能是完整 key。
	hint, _ := body["hint"].(string)
	if hint != "****9999" {
		t.Fatalf("hint 应为 ****9999，得到 %q", hint)
	}
}

// TestAdmin_ListNeverReturnsPlainOrCipher 管理端列表不带明文与密文。
//
// 管理员要换 key 直接覆盖即可；能看完整 key 只会扩大泄露面。
func TestAdmin_ListNeverReturnsPlainOrCipher(t *testing.T) {
	r, _ := newEnv(t)
	do(t, r, http.MethodPost, "/admin/keys/amap", map[string]any{
		"platform": "android", "key": "amap-key-TOPSECRET12",
	})

	code, body := do(t, r, http.MethodGet, "/admin/keys/amap", nil)
	if code != http.StatusOK {
		t.Fatalf("列表失败：%d", code)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "TOPSECRET") {
		t.Fatalf("响应里出现了明文 key：%s", raw)
	}
	for _, forbidden := range []string{`"cipher"`, `"key"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("响应里出现了 %s：%s", forbidden, raw)
		}
	}
	// 后四位提示应在。
	if !strings.Contains(string(raw), "****T12") && !strings.Contains(string(raw), "****") {
		t.Fatalf("应带 hint：%s", raw)
	}
}

// TestAcquire_UnknownPlatformRejected 平台非法要 400。
func TestAcquire_UnknownPlatformRejected(t *testing.T) {
	r, _ := newEnv(t)
	if code, _ := do(t, r, http.MethodPost, "/keys/amap/acquire",
		map[string]any{"platform": "symbian"}); code != http.StatusBadRequest {
		t.Fatalf("非法平台应 400，得到 %d", code)
	}
}

// TestAcquire_NoKeyConfigured 没配 key 时明确报 not_found，不是 500。
func TestAcquire_NoKeyConfigured(t *testing.T) {
	r, _ := newEnv(t)
	code, body := do(t, r, http.MethodPost, "/keys/amap/acquire", map[string]any{"platform": "android"})
	if code != http.StatusNotFound {
		t.Fatalf("未配置应 404，得到 %d %v", code, body)
	}
}

// TestAcquire_DisabledKeySkipped 停用的 key 不参与分发。
func TestAcquire_DisabledKeySkipped(t *testing.T) {
	r, _ := newEnv(t)
	// 一把启用、一把停用。
	do(t, r, http.MethodPost, "/admin/keys/amap", map[string]any{
		"platform": "android", "key": "amap-key-ENABLED01",
	})
	_, body := do(t, r, http.MethodPost, "/admin/keys/amap", map[string]any{
		"platform": "android", "key": "amap-key-DISABLED2", "enabled": false,
	})
	disabledID := int64(body["data"].(map[string]any)["id"].(float64))

	// 连取 6 次，永远只能拿到启用的那把。
	for i := 0; i < 6; i++ {
		_, got := do(t, r, http.MethodPost, "/keys/amap/acquire", map[string]any{"platform": "android"})
		if k, _ := got["key"].(string); k != "amap-key-ENABLED01" {
			t.Fatalf("停用的 key 不该被分发，拿到 %q", k)
		}
	}
	_ = disabledID
}

// TestAcquire_PlatformIsIsolated 不同平台互不串用。
func TestAcquire_PlatformIsIsolated(t *testing.T) {
	r, _ := newEnv(t)
	do(t, r, http.MethodPost, "/admin/keys/amap", map[string]any{
		"platform": "android", "key": "amap-android-key1",
	})
	do(t, r, http.MethodPost, "/admin/keys/amap", map[string]any{
		"platform": "web", "key": "amap-web-key12345",
	})

	_, got := do(t, r, http.MethodPost, "/keys/amap/acquire", map[string]any{"platform": "web"})
	if k, _ := got["key"].(string); k != "amap-web-key12345" {
		t.Fatalf("web 应拿到 web 的 key，得到 %q", k)
	}
	_, got = do(t, r, http.MethodPost, "/keys/amap/acquire", map[string]any{"platform": "android"})
	if k, _ := got["key"].(string); k != "amap-android-key1" {
		t.Fatalf("android 应拿到 android 的 key，得到 %q", k)
	}
}

// TestUpdate_RotateKey 更新时能给已有记录换一把新 key。
func TestUpdate_RotateKey(t *testing.T) {
	r, _ := newEnv(t)
	_, body := do(t, r, http.MethodPost, "/admin/keys/amap", map[string]any{
		"platform": "android", "key": "amap-key-OLD11111",
	})
	id := int64(body["data"].(map[string]any)["id"].(float64))

	code, body := do(t, r, http.MethodPatch, "/admin/keys/amap/"+itoa(id), map[string]any{
		"key": "amap-key-NEW22222",
	})
	if code != http.StatusOK {
		t.Fatalf("更新失败：%d %v", code, body)
	}
	if hint, _ := body["data"].(map[string]any)["hint"].(string); hint != "****2222" {
		t.Fatalf("hint 应更新为 ****2222，得到 %q", hint)
	}

	// 取用拿到的是新 key。
	_, got := do(t, r, http.MethodPost, "/keys/amap/acquire", map[string]any{"platform": "android"})
	if k, _ := got["key"].(string); k != "amap-key-NEW22222" {
		t.Fatalf("应拿到新 key，得到 %q", k)
	}
}

// TestCreate_RequiresKey 录入时 key 必填。
func TestCreate_RequiresKey(t *testing.T) {
	r, _ := newEnv(t)
	if code, _ := do(t, r, http.MethodPost, "/admin/keys/amap",
		map[string]any{"platform": "android"}); code != http.StatusBadRequest {
		t.Fatalf("缺 key 应 400，得到 %d", code)
	}
}

// TestHintOf 后四位提示的边界。
func TestHintOf(t *testing.T) {
	// 通过 Service 间接验证：短 key 也应有 hint 且不泄露。
	r, _ := newEnv(t)
	_, body := do(t, r, http.MethodPost, "/admin/keys/amap", map[string]any{
		"platform": "android", "key": "abc",
	})
	hint, _ := body["data"].(map[string]any)["hint"].(string)
	if hint != "****" {
		t.Fatalf("过短的 key 的 hint 应为 ****，得到 %q", hint)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
