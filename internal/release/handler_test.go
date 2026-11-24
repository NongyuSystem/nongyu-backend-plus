package release_test

// 下载路径的端到端验收（Spec #80 / T3）。
//
// 走**真实 HTTP**（gin 路由 + handler），只断言外部可观察行为：
// 状态码、响应头、响应体字节。不深入 handler 内部。
// 先例：internal/skin/service_test.go 的 TestHandler_UploadCurrentDownloadActivate。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/release"
	releasestore "nongyu-backend-plus/internal/release/store"
)

// newReleaseRouter 构造一个只挂本域路由的 gin 引擎（不穿全局鉴权——
// 鉴权由 internal/app 的装配层用例覆盖，这里专测契约形状）。
func newReleaseRouter(t *testing.T) (*gin.Engine, *release.Service) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	svc := release.New(release.Options{
		Repo:        releasestore.NewReleaseMemoryRepo(nil),
		ObjectStore: platform.NewMemoryObjectStore(),
	})
	r := gin.New()
	release.RegisterSession(r, svc)
	release.RegisterAdmin(r, svc, func(*gin.Context) (string, bool) { return "admin-1", true })
	return r, svc
}

func doGet(r http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// 无任何版本时 /latest 返回 200 + hasRelease=false（不是 404）。
func TestHTTP_LatestHasReleaseFalseWhenEmpty(t *testing.T) {
	r, _ := newReleaseRouter(t)
	w := doGet(r, "/releases/android/latest?applicationId=com.nongyu.app")
	if w.Code != http.StatusOK {
		t.Fatalf("无版本时应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if body["hasRelease"] != false {
		t.Fatalf("无版本时 hasRelease 应为 false，得到 %v", body["hasRelease"])
	}
}

// 上传 → /latest 返回最大版本 → 下载到手字节一致 + sha256 头正确。
func TestHTTP_UploadLatestAndDownloadRoundTrip(t *testing.T) {
	r, svc := newReleaseRouter(t)
	payload := []byte("PK\x03\x04real-apk-payload-bytes")

	// 上传两版，乱序：102 先、103 后。
	for _, c := range []int64{102, 103} {
		if _, err := svc.Upload(context.Background(), release.UploadInput{
			PackBytes: payload, AppID: "com.nongyu.app",
			VersionCode: c, VersionName: "1.0." + itoaForTest(c-100),
		}); err != nil {
			t.Fatalf("上传 %d：%v", c, err)
		}
	}

	// 1) /latest 应指向 103，且 downloadUrl 只指向本服务。
	w := doGet(r, "/releases/android/latest?applicationId=com.nongyu.app")
	if w.Code != http.StatusOK {
		t.Fatalf("/latest 应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if got := int64(body["versionCode"].(float64)); got != 103 {
		t.Fatalf("最新应是 103，得到 %d", got)
	}
	if body["hasRelease"] != true {
		t.Fatalf("有版本时 hasRelease 应为 true，得到 %v", body["hasRelease"])
	}
	// 不得泄露对象存储地址/预签名 URL。
	raw := w.Body.String()
	if bytes.Contains([]byte(raw), []byte("memory://")) || bytes.Contains([]byte(raw), []byte("ttl=")) {
		t.Fatalf("响应泄露了对象存储直链：%s", raw)
	}
	if body["downloadUrl"] != "/releases/android/103/pack" {
		t.Fatalf("downloadUrl 应指向本服务下载端点，得到 %v", body["downloadUrl"])
	}
	shaWanted := body["sha256"].(string)

	// 2) 下载最新包：字节一致、头齐全。
	for _, path := range []string{
		"/releases/android/latest/pack?applicationId=com.nongyu.app",
		"/releases/android/103/pack?applicationId=com.nongyu.app",
	} {
		w := doGet(r, path)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s 应 200，实际 %d body=%s", path, w.Code, w.Body.String())
		}
		if !bytes.Equal(w.Body.Bytes(), payload) {
			t.Fatalf("GET %s 下载的字节与上传不一致", path)
		}
		if ct := w.Header().Get("Content-Type"); ct != release.ApkContentType {
			t.Fatalf("%s Content-Type 应为 %s，得到 %q", path, release.ApkContentType, ct)
		}
		if cd := w.Header().Get("Content-Disposition"); cd != "attachment" {
			t.Fatalf("%s Content-Disposition 应为 attachment，得到 %q", path, cd)
		}
		if sha := w.Header().Get(release.SHA256Header); sha != shaWanted {
			t.Fatalf("%s 的 %s 应为 %q，得到 %q", path, release.SHA256Header, shaWanted, sha)
		}
	}
}

// 不存在的版本码 → 404；非数字版本码 → 400（不是 500）。
func TestHTTP_PackErrors(t *testing.T) {
	r, svc := newReleaseRouter(t)
	if _, err := svc.Upload(context.Background(), release.UploadInput{
		PackBytes: []byte("x"), AppID: "com.nongyu.app",
		VersionCode: 100, VersionName: "1.0.0",
	}); err != nil {
		t.Fatalf("上传：%v", err)
	}

	w := doGet(r, "/releases/android/999/pack?applicationId=com.nongyu.app")
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在的版本应 404，实际 %d body=%s", w.Code, w.Body.String())
	}

	w = doGet(r, "/releases/android/not-a-number/pack?applicationId=com.nongyu.app")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非数字 versionCode 应 400，实际 %d body=%s", w.Code, w.Body.String())
	}
}

// itoaForTest 是测试里拼版本名用的小工具。
func itoaForTest(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
