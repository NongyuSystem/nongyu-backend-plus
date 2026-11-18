package secondclass

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform/mail"
	"nongyu-backend-plus/internal/school"
)

// memCache 是 platform.CacheStore 的进程内实现，语义与 platform.RedisCache 对齐：
// 键不存在返回 (nil, nil)，Set 覆盖写，TryAcquire 是「到期自动放开」的 SET NX。
// 形状照 jiaowu/cache_test.go 的脚手架（复制到本包同包测试）。
type memCache struct {
	mu      sync.Mutex
	data    map[string][]byte
	expires map[string]time.Time
	locks   map[string]time.Time
	now     func() time.Time
	failSet bool
}

func newMemCache(now func() time.Time) *memCache {
	return &memCache{
		data:    make(map[string][]byte),
		expires: make(map[string]time.Time),
		locks:   make(map[string]time.Time),
		now:     now,
	}
}

func (m *memCache) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if exp, ok := m.expires[key]; ok && m.now().After(exp) {
		delete(m.data, key)
		delete(m.expires, key)
		return nil, nil
	}
	v, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func (m *memCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failSet {
		return fmt.Errorf("模拟缓存写入故障")
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[key] = cp
	m.expires[key] = m.now().Add(ttl)
	return nil
}

func (m *memCache) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	delete(m.expires, key)
	return nil
}

func (m *memCache) TryAcquire(_ context.Context, key string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if exp, ok := m.locks[key]; ok && m.now().Before(exp) {
		return false, nil
	}
	m.locks[key] = m.now().Add(ttl)
	return true, nil
}

// forceAge 把某个键的 fetchedAt 往前拨，测新鲜度窗口与 7 天上限。
func (m *memCache) forceAge(t *testing.T, userID string, dt DataType, age time.Duration) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	key := cacheDataKey(userID, dt)
	raw, ok := m.data[key]
	if !ok {
		t.Fatalf("缓存里没有 %q，无法调整年龄", key)
	}
	var entry cachedEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("解缓存条目失败: %v", err)
	}
	entry.FetchedAt = entry.FetchedAt.Add(-age)
	next, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("重编缓存条目失败: %v", err)
	}
	m.data[key] = next
}

// fakeClock 让测试能直接推进时间（服务端用它给 fetchedAt 盖章）。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}
}
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// recordingAuditStore 实现 mail.AuditStore 四方法（审计是旁路，只记录、不影响业务）。
type recordingAuditStore struct {
	mu      sync.Mutex
	entries []mail.AuditEntry
}

func (a *recordingAuditStore) Append(_ context.Context, e mail.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return nil
}
func (a *recordingAuditStore) Flush(context.Context) error { return nil }
func (a *recordingAuditStore) Dropped() int64              { return 0 }
func (a *recordingAuditStore) Close() error                { return nil }
func (a *recordingAuditStore) results(action string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []string{}
	for _, e := range a.entries {
		if e.Action == action {
			out = append(out, e.Result)
		}
	}
	return out
}

// newCacheTestServer 装配一个只有缓存路由的 gin 引擎。
// userID 非空时用最薄中间件塞身份；传空串即模拟未认证（401）。
func newCacheTestServer(t *testing.T, userID string) (*gin.Engine, *Service, *memCache, *fakeClock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	clock := newFakeClock()
	cache := newMemCache(func() time.Time { return clock.Now() })
	validator, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	svc := New(Options{Cache: cache, Schema: validator, Clock: clock})
	engine := gin.New()
	if userID != "" {
		engine.Use(func(c *gin.Context) {
			c.Set(school.CtxUserID, userID)
			c.Next()
		})
	}
	RegisterCache(engine, svc)
	return engine, svc, cache, clock
}

// httpResult 是一次请求的全部可观察输出（外部行为，不含任何内部状态）。
type httpResult struct {
	status int
	body   map[string]any
	raw    []byte
	header http.Header
}

func serve(t *testing.T, engine *gin.Engine, r *http.Request) httpResult {
	t.Helper()
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, r)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return httpResult{status: w.Code, body: out, raw: w.Body.Bytes(), header: w.Header()}
}

// doJSON 发一个请求（body 为字符串）。
func doJSON(t *testing.T, engine *gin.Engine, method, path, body string) httpResult {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	return serve(t, engine, r)
}

// doRaw 发一个请求，body 用原始字节（测 >1MiB 超限用）。
func doRaw(t *testing.T, engine *gin.Engine, method, path string, body []byte) httpResult {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return serve(t, engine, r)
}

// payloadOf 从响应体里取出 payload 的原始 JSON（保留数组/对象形态）。
func payloadOf(t *testing.T, res httpResult) json.RawMessage {
	t.Helper()
	raw, ok := res.body["payload"]
	if !ok {
		t.Fatalf("响应体缺 payload: %s", res.raw)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("无法取出 payload: %v", err)
	}
	return b
}
