package jiaowu_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/jiaowu"
)

// keyHexPrefix 是 WebVPN 编码地址的固定路径段前缀，逐字来自契约清单 §2。
// 白名单成功路径必须产出含该前缀的编码地址。
const keyHexPrefix = "77726476706e69737468656265737421"

// enforcedPolicy 是「出站闸口强制生效」的策略，用于 #14/#17 的闸口测试。
// 白名单覆盖三个真实目标 host；Enforce=true 使 platform 实现层的
// 白名单/超时/302/Set-Cookie 全部强制（ADR-0001 全系统唯一强制点）。
func enforcedPolicy() platform.DoerPolicy {
	return platform.DoerPolicy{
		Enforce:      true,
		AllowedHosts: []string{"jiaowu.sicau.edu.cn", "auth.sicau.edu.cn", "webvpn.sicau.edu.cn"},
	}
}

// newEngine 用「受控的 MockHTTPDoer」起一个只挂了教务子域路由的 gin 引擎。
// 这是 issue #13 要求的 HTTP 层唯一接缝：出站能力在这里被换成记录型假实现，
// 以便用 Requests() 断言「零出站」。
func newEngine(t *testing.T) (http.Handler, *platform.MockHTTPDoer) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	doer := platform.NewMockHTTPDoer(platform.DoerPolicy{})
	engine := gin.New()
	jiaowu.Register(engine, jiaowu.New(doer))
	return engine, doer
}

func doGet(t *testing.T, h http.Handler, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/school/address?"+values.Encode(), nil)
	h.ServeHTTP(w, req)
	return w
}

// TestAddressWhitelistReturnsEncodedAddressAndNoOutbound 锁住成功路径的三件事：
// 200、响应体含逻辑目标名与含 KEY_HEX 前缀的编码地址、以及零出站请求。
func TestAddressWhitelistReturnsEncodedAddressAndNoOutbound(t *testing.T) {
	h, doer := newEngine(t)
	w := doGet(t, h, url.Values{"target": {"jiaowu"}})

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，响应体：%s", w.Code, w.Body.String())
	}
	var body struct {
		Target  string `json:"target"`
		Address string `json:"address"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	if body.Target != "jiaowu" {
		t.Fatalf("响应体应含逻辑目标名，实际 target=%q", body.Target)
	}
	if !strings.Contains(body.Address, keyHexPrefix) {
		t.Fatalf("地址应含 WebVPN KEY_HEX 前缀，实际 address=%q", body.Address)
	}
	if !strings.HasPrefix(body.Address, "https://webvpn.sicau.edu.cn/https/"+keyHexPrefix) {
		t.Fatalf("编码地址形态不符，实际 address=%q", body.Address)
	}
	if got := len(doer.Requests()); got != 0 {
		t.Fatalf("Address 只返回地址、不得发起任何出站请求；成功路径也应零出站，实际记录到 %d 次：%#v", got, doer.Requests())
	}
}

// TestAddressAppendsOptionalPathInPlaintext 覆盖「可选查询条件」：path（含自带
// query 串）应以明文追加到编码地址上，且成功路径依旧零出站。
func TestAddressAppendsOptionalPathInPlaintext(t *testing.T) {
	h, doer := newEngine(t)
	w := doGet(t, h, url.Values{
		"target": {"jiaowu"},
		"path":   {"/jiaoshi/aspsso/caslogin.asp?ticket=ST"},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d，响应体：%s", w.Code, w.Body.String())
	}
	var body struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	if !strings.Contains(body.Address, "/jiaoshi/aspsso/caslogin.asp?ticket=ST") {
		t.Fatalf("path/query 应以明文保持，实际 address=%q", body.Address)
	}
	if got := len(doer.Requests()); got != 0 {
		t.Fatalf("成功路径应零出站，实际记录到 %d 次：%#v", got, doer.Requests())
	}
}

// TestAddressDeniesNonWhitelistTargetsWithZeroOutbound 是「拒绝发生在闸口而非事后」
// 的可观测证据：未知逻辑名、把 URL 当名字传、把 host 当名字传，都必须是 400 +
// target_denied，且假出站实现记录到零请求。
func TestAddressDeniesNonWhitelistTargetsWithZeroOutbound(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{"未知逻辑名", "unknown"},
		{"把 URL 当名字传", "https://evil.example.com/x"},
		{"把 host 当名字传", "jiaowu.sicau.edu.cn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, doer := newEngine(t)
			w := doGet(t, h, url.Values{"target": {tc.target}})

			if w.Code != http.StatusBadRequest {
				t.Fatalf("期望 400，实际 %d，响应体：%s", w.Code, w.Body.String())
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("响应体不是合法 JSON：%v，body=%s", err, w.Body.String())
			}
			if body.Code != "target_denied" {
				t.Fatalf("错误码应为 target_denied，实际 code=%q，body=%s", body.Code, w.Body.String())
			}
			if got := len(doer.Requests()); got != 0 {
				t.Fatalf("拒绝必须发生在闸口、不得有任何出站请求，实际记录到 %d 次：%#v", got, doer.Requests())
			}
		})
	}
}
