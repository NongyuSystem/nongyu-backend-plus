package jiaowu

// 取数账号管理端接口的测试（挂 adminGroup，这里直接测 handler）。
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type memAdminStore struct {
	rows   map[int64]FetchAccount
	nextID int64
}

func newMemAdminStore() *memAdminStore {
	return &memAdminStore{rows: map[int64]FetchAccount{}}
}
func (m *memAdminStore) List(context.Context) ([]FetchAccount, error) {
	out := []FetchAccount{}
	for _, a := range m.rows {
		out = append(out, a)
	}
	return out, nil
}
func (m *memAdminStore) Create(_ context.Context, a FetchAccount) (int64, error) {
	m.nextID++
	a.ID = m.nextID
	m.rows[a.ID] = a
	return a.ID, nil
}
func (m *memAdminStore) Update(_ context.Context, a FetchAccount) error {
	m.rows[a.ID] = a
	return nil
}
func (m *memAdminStore) Delete(_ context.Context, id int64) error {
	delete(m.rows, id)
	return nil
}
func (m *memAdminStore) SetEnabled(_ context.Context, id int64, enabled bool) error {
	a := m.rows[id]
	a.Enabled = enabled
	m.rows[id] = a
	return nil
}

func newAdminEngine(t *testing.T) (*gin.Engine, *memAdminStore, *Service) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := newMemAdminStore()
	svc := New(nil)
	engine := gin.New()
	RegisterFetchAccountAdmin(engine, store, svc)
	return engine, store, svc
}

func adminReq(t *testing.T, engine *gin.Engine, method, path, body string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, r)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// TestAdminCreateAndListNeverLeaksCredentials 新增后列表不回显任何凭据。
func TestAdminCreateAndListNeverLeaksCredentials(t *testing.T) {
	engine, _, _ := newAdminEngine(t)
	secret := "201690:TGT:super-secret-value"
	code, body := adminReq(t, engine, http.MethodPost, "/admin/jiaowu/fetch-accounts",
		`{"label":"取数号1","tgt":"`+secret+`","cookieHeader":"go-admin-tgt=`+secret+`; laravel_session=ls","vpnTicket":"abcd1234abcd1234"}`)
	if code != http.StatusOK {
		t.Fatalf("新增应成功，实际 %d %v", code, body)
	}
	raw := mustJSON(t, body)
	code, listed := adminReq(t, engine, http.MethodGet, "/admin/jiaowu/fetch-accounts", "")
	if code != http.StatusOK {
		t.Fatalf("列表应成功，实际 %d", code)
	}
	// 整个响应体不得含 tgt / cookieHeader / vpnTicket 的明文或字段名值
	blob := mustJSON(t, listed)
	for _, leak := range []string{secret, "laravel_session=ls", "abcd1234abcd1234", "cookieHeader", "vpnTicket"} {
		if strings.Contains(blob, leak) {
			t.Fatalf("列表响应泄漏了 %q：%s", leak, blob)
		}
	}
	_ = raw
}

// TestAdminRejectsBadCredentials 缺凭据或票形态非法时拒绝。
func TestAdminRejectsBadCredentials(t *testing.T) {
	engine, _, _ := newAdminEngine(t)
	bads := []string{
		`{"label":"x","tgt":"","vpnTicket":"abcd1234abcd1234"}`,
		`{"label":"x","tgt":"TGT:x","vpnTicket":""}`,
		`{"label":"x","tgt":"TGT:x","vpnTicket":"has space in it"}`,
		`{"label":"x","tgt":"TGT:x","vpnTicket":"<a>Found</a>"}`,
	}
	for _, b := range bads {
		code, _ := adminReq(t, engine, http.MethodPost, "/admin/jiaowu/fetch-accounts", b)
		if code == http.StatusOK {
			t.Fatalf("非法凭据应被拒，实际通过：%s", b)
		}
	}
}

// TestAdminUpdateEnableDelete 更新/启停/删除链路可用。
func TestAdminUpdateEnableDelete(t *testing.T) {
	engine, _, _ := newAdminEngine(t)
	code, body := adminReq(t, engine, http.MethodPost, "/admin/jiaowu/fetch-accounts",
		`{"label":"A","tgt":"TGT:A","vpnTicket":"abcd1234abcd1234"}`)
	if code != http.StatusOK {
		t.Fatalf("新增失败 %d %v", code, body)
	}
	id := int64(body["id"].(float64))
	code, _ = adminReq(t, engine, http.MethodPut, "/admin/jiaowu/fetch-accounts/1",
		`{"label":"B","tgt":"TGT:B","vpnTicket":"efef5678efef5678"}`)
	if code != http.StatusOK {
		t.Fatalf("更新失败 %d", code)
	}
	code, _ = adminReq(t, engine, http.MethodPatch, "/admin/jiaowu/fetch-accounts/1", `{"enabled":false}`)
	if code != http.StatusOK {
		t.Fatalf("启停失败 %d", code)
	}
	code, _ = adminReq(t, engine, http.MethodDelete, "/admin/jiaowu/fetch-accounts/1", "")
	if code != http.StatusOK {
		t.Fatalf("删除失败 %d", code)
	}
	_ = id
}

// TestAdminPublicCacheInvalidateRejectsPrivateType 管理端失效只认公共类。
func TestAdminPublicCacheInvalidateRejectsPrivateType(t *testing.T) {
	engine, _, _ := newAdminEngine(t)
	code, _ := adminReq(t, engine, http.MethodDelete, "/admin/jiaowu/public-cache/course", "")
	if code == http.StatusOK {
		t.Fatal("私有类不应允许走公共缓存失效端点")
	}
}

func mustJSON(t *testing.T, m map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(m)
	return string(b)
}
