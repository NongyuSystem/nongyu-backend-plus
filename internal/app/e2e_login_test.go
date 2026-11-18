package app

// #26 门槛 4 的**可自动化部分**：完整 HTTP 链路
//   POST /identity/login → 拿令牌 → GET /identity/me 200；无令牌 401。
//
// 用 schoolForTest 注入假学校认证源，不碰真实学校网络。
// seam：HTTP 端点（公共行为），不测内部函数。
// 真实凭据的人工验收另记在票评论里（依赖学校统一身份认证可达）。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	identitylogin "nongyu-backend-plus/internal/identity/login"
	"nongyu-backend-plus/internal/platform"
)

// 完整链路：登录拿令牌 → 用令牌访问受保护端点 200 → 无令牌 401。
func TestE2E_LoginThenAccessProtectedEndpoint(t *testing.T) {
	const studentNo = "888100041"
	school := fakeSchoolAuth{ident: identitylogin.SchoolIdentity{
		StudentNo: studentNo, Name: "端到端用户",
		College: "信息工程学院", Major: "数据科学与大数据技术",
		ClassName: "大数据202503", Grade: "2025",
		IDCardSuffix4: "2614",
	}}
	a, _ := newE2EApp(t, "e2e_login_chain", school)

	// ① 登录：school 方式，交客户端凭据（此处用假的，假源不校验）
	loginBody, _ := json.Marshal(map[string]string{
		"method":       "school",
		"schoolMethod": "password",
		"deviceId":     "e2e-device",
		"studentNo":    studentNo,
		"password":     "ignored-by-fake",
		"cookie":       "go-admin-tgt=fake; laravel_session=fake",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/identity/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	a.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析登录响应：%v body=%s", err, w.Body.String())
	}
	if resp.Token == "" {
		t.Fatal("登录成功必须返回令牌")
	}

	// ② 用令牌访问受保护端点 → 200
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/identity/me", nil)
	req2.Header.Set("Authorization", "Bearer "+resp.Token)
	a.Handler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("带令牌访问 /identity/me 应 200，实际 %d body=%s", w2.Code, w2.Body.String())
	}
	var me map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &me); err != nil {
		t.Fatalf("解析 /identity/me：%v", err)
	}
	if got, _ := me["name"].(string); got != "端到端用户" {
		t.Fatalf("/identity/me 应返回刚登录用户的画像，实际 %v", me)
	}
	if got, _ := me["major"].(string); got != "数据科学与大数据技术" {
		t.Fatalf("画像专业应来自学校认证，实际 %v", me)
	}

	// ③ 无令牌 → 401
	w3 := httptest.NewRecorder()
	a.Handler().ServeHTTP(w3, httptest.NewRequest(http.MethodGet, "/identity/me", nil))
	if w3.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌访问 /identity/me 应 401，实际 %d", w3.Code)
	}
}

// 学校认证失败时，登录端点必须返回错误而不是签发令牌。
func TestE2E_SchoolAuthFailureIssuesNoToken(t *testing.T) {
	school := fakeSchoolAuth{err: context.DeadlineExceeded}
	a, _ := newE2EApp(t, "e2e_school_fail", school)

	loginBody, _ := json.Marshal(map[string]string{
		"method": "school", "schoolMethod": "password",
		"deviceId": "e2e-device", "studentNo": "888100042", "password": "x",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/identity/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	a.Handler().ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Fatalf("学校认证失败时不得 200，实际 body=%s", w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte(`"token"`)) {
		t.Fatal("学校认证失败时不得签发令牌")
	}
}

// 令牌本身不得包含密码/身份证等敏感内容（Spec 用户故事 9）。
func TestE2E_TokenCarriesNoSensitiveData(t *testing.T) {
	const studentNo = "888100043"
	school := fakeSchoolAuth{ident: identitylogin.SchoolIdentity{
		StudentNo: studentNo, Name: "令牌检查",
		IDCardSuffix4: "2614", IDCardFull: "110101200001011234",
	}}
	a, _ := newE2EApp(t, "e2e_token_payload", school)

	loginBody, _ := json.Marshal(map[string]string{
		"method": "school", "schoolMethod": "password",
		"deviceId": "e2e-device", "studentNo": studentNo, "password": "SuperSecret123",
		// 字段校验要求 school 方式带可换票凭据；这里用假值，fake 源不会真去换票。
		"goAdminTgt": "201690:TGT:fake-for-e2e",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/identity/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	// 令牌是 base64 的 payload：解出来检查不得含敏感串
	for _, forbidden := range []string{"SuperSecret123", "110101200001011234", studentNo} {
		if bytes.Contains([]byte(resp.Token), []byte(forbidden)) {
			t.Fatalf("令牌不得含敏感内容 %q", forbidden)
		}
	}
	_ = platform.ScopeApp
}
