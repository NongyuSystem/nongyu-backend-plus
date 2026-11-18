package jiaowu_test

// 本包（package jiaowu_test）跨测试文件共用的测试替身与 HTTP 层脚手架。
//
// keyHexPrefix / enforcedPolicy / newEngine / doGet 被 handler_test.go、fetch_test.go、
// 白名单（app_wiring_test.go）等多处共用，按「被 ≥2 个测试文件段使用就抽出来」的
// 约定集中放在这里，避免跟着某一个原文件走后其它用例引用不到（undefined）。

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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
