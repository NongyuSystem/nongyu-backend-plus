package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/server"
)

// 与 nongyu-app-plus AppRequestSignerTest 共用的向量。改任何一边都要同时改另一边。
const (
	vectorKeyID = "dev"
	vectorKey   = "nongyu-dev-app-sign-key-v1-32b!!"
	vectorNonce = "n-test-nonce-0001"
	vectorTS    = int64(1760000000)
	vectorSig   = "tZEyLjSrwEfsM0907M4ZzF_WlN0DhvmFOfT4I0FdH0A"
	vectorBody  = `{"email":"a@b.c"}`
	vectorPath  = "/identity/login/admin"
)

func TestAppSignVectorMatchesClient(t *testing.T) {
	sig := server.SignAppRequest(vectorKeyID, []byte(vectorKey), vectorTS, vectorNonce, http.MethodPost, vectorPath, "", []byte(vectorBody))
	if sig != vectorSig {
		t.Fatalf("NY1 向量漂移，客户端会对不上：got %s", sig)
	}
}

func TestAppSignMiddlewareAcceptsVectorAndRestoresBody(t *testing.T) {
	r, clock := newAppSignEngine(t)
	clock.Set(time.Unix(vectorTS, 0))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, vectorPath, bytes.NewReader([]byte(vectorBody)))
	stamp(req, vectorKeyID, []byte(vectorKey), vectorTS, vectorNonce, "")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("合法签名应放行，实际 %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析回显失败：%v", err)
	}
	if got.Body != vectorBody {
		t.Fatalf("验签后请求体被改掉：%q", got.Body)
	}
}

func TestAppSignMiddlewareRejectsMissingTamperedReplayAndSkew(t *testing.T) {
	body := []byte(vectorBody)
	key := []byte(vectorKey)

	t.Run("missing", func(t *testing.T) {
		r, clock := newAppSignEngine(t)
		clock.Set(time.Unix(vectorTS, 0))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, vectorPath, bytes.NewReader(body)))
		assertAppSign(t, w, http.StatusUnauthorized, "app_sign_invalid")
	})

	t.Run("tampered body", func(t *testing.T) {
		r, clock := newAppSignEngine(t)
		clock.Set(time.Unix(vectorTS, 0))
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, vectorPath, bytes.NewReader([]byte(`{"email":"evil"}`)))
		// 签名覆盖的是原始 body，发出去的是被换掉的 body。
		stampOver(req, vectorKeyID, key, vectorTS, vectorNonce, "", body)
		r.ServeHTTP(w, req)
		assertAppSign(t, w, http.StatusUnauthorized, "app_sign_invalid")
	})

	t.Run("query covered", func(t *testing.T) {
		r, clock := newAppSignEngine(t)
		clock.Set(time.Unix(vectorTS, 0))
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, vectorPath+"?a=2", bytes.NewReader(body))
		stamp(req, vectorKeyID, key, vectorTS, "n-test-nonce-query1", "a=1")
		r.ServeHTTP(w, req)
		assertAppSign(t, w, http.StatusUnauthorized, "app_sign_invalid")
	})

	t.Run("replay", func(t *testing.T) {
		r, clock := newAppSignEngine(t)
		clock.Set(time.Unix(vectorTS, 0))
		nonce := "n-test-nonce-replay"
		first := httptest.NewRequest(http.MethodPost, vectorPath, bytes.NewReader(body))
		stamp(first, vectorKeyID, key, vectorTS, nonce, "")
		w1 := httptest.NewRecorder()
		r.ServeHTTP(w1, first)
		if w1.Code != http.StatusOK {
			t.Fatalf("首次应放行，实际 %d %s", w1.Code, w1.Body.String())
		}
		second := httptest.NewRequest(http.MethodPost, vectorPath, bytes.NewReader(body))
		stamp(second, vectorKeyID, key, vectorTS, nonce, "")
		w2 := httptest.NewRecorder()
		r.ServeHTTP(w2, second)
		assertAppSign(t, w2, http.StatusUnauthorized, "app_sign_invalid")
	})

	t.Run("skew", func(t *testing.T) {
		r, clock := newAppSignEngine(t)
		clock.Set(time.Unix(vectorTS, 0).Add(6 * time.Minute))
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, vectorPath, bytes.NewReader(body))
		stamp(req, vectorKeyID, key, vectorTS, "n-test-nonce-skew01", "")
		r.ServeHTTP(w, req)
		assertAppSign(t, w, http.StatusUnauthorized, "app_sign_invalid")
	})

	t.Run("unknown key", func(t *testing.T) {
		r, clock := newAppSignEngine(t)
		clock.Set(time.Unix(vectorTS, 0))
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, vectorPath, bytes.NewReader(body))
		stamp(req, "other", key, vectorTS, "n-test-nonce-other1", "")
		r.ServeHTTP(w, req)
		assertAppSign(t, w, http.StatusUnauthorized, "app_sign_invalid")
	})
}

func TestAppSignMiddlewareExemptsProbesOnly(t *testing.T) {
	r, _ := newAppSignEngine(t)
	for _, path := range []string{"/healthz", "/status"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s 应免签名，实际 %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/identity/login", nil))
	assertAppSign(t, w, http.StatusUnauthorized, "app_sign_invalid")
}

func TestAppSignMiddlewareFailClosedWhenNonceStoreDown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clock := platform.NewMockClock(time.Unix(vectorTS, 0))
	r := gin.New()
	r.Use(server.AppSignMiddleware(map[string][]byte{vectorKeyID: []byte(vectorKey)}, errNonceStore{}, clock, nil))
	r.POST(vectorPath, func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, vectorPath, bytes.NewReader([]byte(vectorBody)))
	stamp(req, vectorKeyID, []byte(vectorKey), vectorTS, vectorNonce, "")
	r.ServeHTTP(w, req)
	assertAppSign(t, w, http.StatusServiceUnavailable, "app_sign_unavailable")
}

type errNonceStore struct{}

func (errNonceStore) Claim(context.Context, string, time.Duration) (bool, error) {
	return false, io.ErrUnexpectedEOF
}

func newAppSignEngine(t *testing.T) (*gin.Engine, *platform.MockClock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	clock := platform.NewMockClock(time.Unix(vectorTS, 0))
	r := gin.New()
	r.Use(server.AppSignMiddleware(
		map[string][]byte{vectorKeyID: []byte(vectorKey)},
		server.NewMemoryNonceStore(clock),
		clock,
		nil,
	))
	echo := func(c *gin.Context) {
		raw, _ := io.ReadAll(c.Request.Body)
		c.JSON(http.StatusOK, gin.H{"body": string(raw)})
	}
	r.POST(vectorPath, echo)
	r.POST("/identity/login", echo)
	r.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/status", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r, clock
}

func stamp(req *http.Request, keyID string, key []byte, ts int64, nonce, rawQuery string) {
	body, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(body))
	stampOver(req, keyID, key, ts, nonce, rawQuery, body)
}

func stampOver(req *http.Request, keyID string, key []byte, ts int64, nonce, rawQuery string, signedBody []byte) {
	req.Header.Set(server.HeaderAppKeyID, keyID)
	req.Header.Set(server.HeaderAppTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(server.HeaderAppNonce, nonce)
	req.Header.Set(server.HeaderAppSignature, server.SignAppRequest(keyID, key, ts, nonce, req.Method, req.URL.EscapedPath(), rawQuery, signedBody))
}

func assertAppSign(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("状态码应为 %d，实际 %d body=%s", status, w.Code, w.Body.String())
	}
	var got struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析失败：%v body=%s", err, w.Body.String())
	}
	if got.Code != code {
		t.Fatalf("code 应为 %s，实际 %s", code, got.Code)
	}
}
