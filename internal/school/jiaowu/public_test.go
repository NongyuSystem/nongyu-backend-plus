package jiaowu

// 公共服务（取数账号池 + 全局缓存）的测试。
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
)

// fakeFetchAccountPool 是内存池，行为与本域 store 的内存实现一致。
type fakeFetchAccountPool struct {
	accounts []FetchAccount
	failPick bool
	marked   []int64
}

func (p *fakeFetchAccountPool) Pick(context.Context) (FetchAccount, error) {
	if p.failPick || len(p.accounts) == 0 {
		return FetchAccount{}, ErrFetchAccountUnavailable
	}
	return p.accounts[0], nil
}
func (p *fakeFetchAccountPool) MarkFailure(_ context.Context, id int64) error {
	p.marked = append(p.marked, id)
	return nil
}

type fakeFetchST struct {
	st  string
	err error
	got string
}

func (f *fakeFetchST) IssueSTWithCredential(_ context.Context, cookieHeader string) (string, error) {
	f.got = cookieHeader
	if f.err != nil {
		return "", f.err
	}
	if f.st == "" {
		return "ST-PUBLIC-FAKE", nil
	}
	return f.st, nil
}

// scriptedPublicDoer 让 CAS 请求直接 200，目标页返回预设内容。
type scriptedPublicDoer struct {
	mu        sync.Mutex
	body      string
	requests  []*http.Request
	casStatus int
}

func (d *scriptedPublicDoer) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests = append(d.requests, req)
	status := http.StatusOK
	h := http.Header{}
	if strings.Contains(req.URL.Path, "caslogin.asp") {
		if d.casStatus != 0 {
			status = d.casStatus
		}
		return &http.Response{StatusCode: status, Header: h, Body: ioNopCloserString(""), Request: req}, nil
	}
	return &http.Response{StatusCode: status, Header: h, Body: ioNopCloserString(d.body), Request: req}, nil
}

// newPublicTestServer 构造带取数账号池的测试服务。
func newPublicTestServer(t *testing.T, withPool bool) (*gin.Engine, *Service, *fakeFetchAccountPool, *scriptedPublicDoer) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	doer := &scriptedPublicDoer{body: "<html>公共页</html>"}
	svc := NewWithAuth(doer, platform.NewMockTicketStore(),
		platform.NewMockCipher("test-key"), platform.NewRealClock(), nil)
	svc.BindCache(newMemCache(func() time.Time { return time.Now() }))
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	svc.BindSchemaValidator(v)
	var pool *fakeFetchAccountPool
	if withPool {
		pool = &fakeFetchAccountPool{accounts: []FetchAccount{{
			ID: 7, Label: "取数号A", TGT: "TGT-PUB",
			CookieHeader: "go-admin-tgt=TGT-PUB; laravel_session=x",
			VPNTicket:    "pubvpnticket", Enabled: true,
		}}}
		svc.BindFetchAccountPool(pool)
		svc.BindFetchAccountSTIssuer(&fakeFetchST{})
	}
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(school.CtxUserID, "user-1")
		c.Next()
	})
	RegisterCache(engine, svc)
	return engine, svc, pool, doer
}

func doPublicMiss(t *testing.T, engine *gin.Engine, dt string) map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/school/jiaowu/cache/"+dt, nil)
	r.Header.Set(HeaderCampusNetwork, "vpn")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, r)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

// TestPublicCacheMissUsesFetchAccount 公共类用取数账号池的票，不用学生的。
func TestPublicCacheMissUsesFetchAccount(t *testing.T) {
	engine, _, _, _ := newPublicTestServer(t, true)
	body := doPublicMiss(t, engine, "notice")
	if body["code"] != "cache_miss" {
		t.Fatalf("应仍是 cache_miss，实际 %v", body["code"])
	}
	cookies, _ := body["cookies"].([]any)
	if len(cookies) != 1 {
		t.Fatalf("应下发池账号的网关票，实际 %v", body["cookies"])
	}
	c, _ := cookies[0].(map[string]any)
	if c["name"] != gatewayTicketCookieName || c["value"] != "pubvpnticket" {
		t.Fatalf("cookie 应为池账号的票，实际 %v", c)
	}
	if body["session"] != "ok" {
		t.Fatalf("session 应为 ok，实际 %v", body["session"])
	}
}

// TestPublicCacheMissWithoutPool 没配池时明确报 fetch_account_unavailable，
// 绝不回退到学生自己的凭据。
func TestPublicCacheMissWithoutPool(t *testing.T) {
	engine, _, _, doer := newPublicTestServer(t, false)
	body := doPublicMiss(t, engine, "notice")
	if body["code"] != "fetch_account_unavailable" {
		t.Fatalf("应回 fetch_account_unavailable，实际 %v", body["code"])
	}
	if len(doer.requests) != 0 {
		t.Fatalf("没池时不应出站，实际 %d 次", len(doer.requests))
	}
}

// TestPublicNeedsInputSkipsPool 缺入参的查询类只给 needs/basePath，不碰池。
func TestPublicNeedsInputSkipsPool(t *testing.T) {
	engine, _, _, doer := newPublicTestServer(t, true)
	body := doPublicMiss(t, engine, "classroomInfo")
	if body["code"] != "cache_miss" {
		t.Fatalf("缺入参公共类应仍是 cache_miss，实际 %v", body["code"])
	}
	needs, _ := body["needs"].(string)
	if !strings.Contains(needs, "教室名") {
		t.Fatalf("应给 needs，实际 %q", needs)
	}
	if len(doer.requests) != 0 {
		t.Fatalf("缺入参不应出站取数，实际 %d 次", len(doer.requests))
	}
}

// TestPublicCacheKeyIsGlobal 公共类缓存键全局共享，私有类按用户隔离。
func TestPublicCacheKeyIsGlobal(t *testing.T) {
	if got := cacheDataKey("user-1", DataTypeNotice); got != "public:notice" {
		t.Fatalf("公共类键应为 public:notice，实际 %q", got)
	}
	if got := cacheDataKey("user-1", DataTypeCourse); got != "user-1:course" {
		t.Fatalf("私有类键应带 userID，实际 %q", got)
	}
	if cacheDataKey("user-1", DataTypeNotice) != cacheDataKey("user-2", DataTypeNotice) {
		t.Fatal("不同用户应命中同一份公共缓存")
	}
	if cacheDataKey("user-1", DataTypeCourse) == cacheDataKey("user-2", DataTypeCourse) {
		t.Fatal("不同用户的私有缓存必须隔离")
	}
}

// TestPublicCacheInvalidateAdminOnly 普通失效端点拒绝公共类。
func TestPublicCacheInvalidateAdminOnly(t *testing.T) {
	engine, _, _, _ := newPublicTestServer(t, true)
	r := httptest.NewRequest(http.MethodDelete, "/school/jiaowu/cache/notice", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("公共类普通失效应 403，实际 %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "public_cache_admin_only") {
		t.Fatalf("应回 public_cache_admin_only，实际 %s", w.Body.String())
	}
}

// TestIsPublicDataTypeClassification 分界线锁死，避免误把私有数据全局共享。
func TestIsPublicDataTypeClassification(t *testing.T) {
	publics := []DataType{DataTypeNotice, DataTypeTeachingNotice, DataTypeCompetition,
		DataTypeKaike, DataTypeClassroomInfo, DataTypeTeacherInfo,
		DataTypeTeacherCourse, DataTypeClassroomCourse}
	privates := []DataType{DataTypeCourse, DataTypeScore, DataTypeExam,
		DataTypeProgress, DataTypeRank, DataTypePlan, DataTypePersonal}
	for _, dt := range publics {
		if !IsPublicDataType(dt) {
			t.Fatalf("%s 应被判为公共类", dt)
		}
	}
	for _, dt := range privates {
		if IsPublicDataType(dt) {
			t.Fatalf("%s 按学生不同，不能被判为公共类（会串数据）", dt)
		}
	}
}
