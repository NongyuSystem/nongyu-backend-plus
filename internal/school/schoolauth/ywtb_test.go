package schoolauth_test

// 用**真实捕获的一次响应**验证解密链路。
//
// 这组数据来自 2026-11-09 对 ywtb 的实际请求（见知识库「川农 ywtb getUserInfo 真实取数」）。
// 有了真实样本，解密算法一旦被改动、或假设错了 key 的取法，测试会立刻失败——
// 这是纯构造数据测不出来的。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"net/http"

	"nongyu-backend-plus/internal/school/schoolauth"
)

// 真实样本：requestId 与去掉 IV 前的完整 data。
// 注意：这是**已过期**的一次会话响应，不含有效凭据。
const (
	sampleRequestID = "d925d02ff19462ba0816dcc11221e679"
	sampleIVHex     = "e91f656a952ffd78311f63e154d0efc7"
	sampleCipherB64 = "TuRC4C2ae0gkk0FyHrMe0H0xnktLm3MEEb2PnKgnVaa1ZNsR4LCcH+uJwptynr0hSImp83kPkBz+66tFAH1IDIhHoL4zh5Yy7Tg8EnjJrkPKnq4AqaSml5CpVgd9DwMSTSisDyfp0AQWZPcuKY2MSoW/dhjN7eKDCF/ZOVV1fquwor8yoigpPKu+2okKdAtkyb1M2konMJdcLAyJegKLNeqAk2IRftKZlKkFhBOlYWsVk7PY46XPh305hNNlIJL1qxilRpVRu/nb7ZRH6MoE5KAnlrzNLThj2TiWlgj8V1vPkSDLIGnyzq22UxxS/0Eu+OlNLkeKejF4Tu1uuvG7bBT+SvWFBhTBDJ01BxMJhWfvPwz2yfbFA0Cpo5PfZ05rlmVWiSnCM6rBxIFu9/2FX218HB/Xs1AgD+gk7GeYg5n6TxHx2xtfKIxNZxFb0WaoELMSyU6/U1jm6ofrKHyg/bWW4TKsrOK+lXGaw4VBCRWbnCzMK8UgZbd9ApoAJj6E"
)

// TestDecryptYWTB_RealSample 用真实样本验证：能解出学号与完整身份证号。
func TestDecryptYWTB_RealSample(t *testing.T) {
	// 重新组装成完整 data（IV + 密文）再做一次 base64，模拟真实响应里的 data。
	full := sampleIVHex + sampleCipherB64
	_ = full

	// 直接调内部函数前，先把 IV 与密文拼回原始字节。
	ivBytes := mustHex(t, sampleIVHex)
	ct := mustB64(t, sampleCipherB64)
	dataB64 := b64Of(append(ivBytes, ct...))

	plain, err := schoolauth.DecryptYWTB(sampleRequestID, dataB64)
	if err != nil {
		t.Fatalf("DecryptYWTB: %v", err)
	}

	var got struct {
		Number string `json:"number"`
		Name   string `json:"name"`
		IDCard string `json:"idcard"`
		Unit   string `json:"unit"`
	}
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("明文不是合法 JSON：%v\n明文：%s", err, plain)
	}
	if got.Number != "202502204" {
		t.Fatalf("学号不符：%q", got.Number)
	}
	if got.Name != "姚一飞" {
		t.Fatalf("姓名不符：%q", got.Name)
	}
	// 关键断言：API 返回的是**完整身份证号**（页面上的 650****2614 只是展示掩码）
	if got.IDCard != "650102200701142614" {
		t.Fatalf("身份证号不符：%q", got.IDCard)
	}
	if got.Unit != "信息工程学院" {
		t.Fatalf("所属单位不符：%q", got.Unit)
	}
}

// 错误的 requestId 必须解密失败，而不是解出乱码当成功。
func TestDecryptYWTB_WrongRequestIDFails(t *testing.T) {
	ivBytes := mustHex(t, sampleIVHex)
	ct := mustB64(t, sampleCipherB64)
	dataB64 := b64Of(append(ivBytes, ct...))

	plain, err := schoolauth.DecryptYWTB("00000000000000000000000000000000", dataB64)
	if err == nil {
		// 若填充恰好合法（概率极低），解出来的也绝不可能是合法 JSON
		if json.Valid(plain) {
			t.Fatalf("错误 requestId 竟解出合法 JSON：%s", plain)
		}
		return
	}
	if !strings.Contains(err.Error(), "填充") {
		t.Logf("错误类型：%v（可接受）", err)
	}
}

func TestDecryptYWTB_RejectsBadInput(t *testing.T) {
	cases := []struct{ name, reqID, data string }{
		{"缺 requestId", "", b64Of(make([]byte, 32))},
		{"data 非 base64", "abc", "!!!not-base64!!!"},
		{"密文过短", "abc", b64Of([]byte{1, 2, 3})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := schoolauth.DecryptYWTB(c.reqID, c.data); err == nil {
				t.Fatal("应报错")
			}
		})
	}
}

// ── 端到端：假 Doer 返回真实信封 + 真实密文 ──────────────────────

type envelopeDoer struct {
	body   string
	status int
	// 记录收到的请求头，用于断言 Bearer 与 cookie 都传了。
	gotAuth   string
	gotCookie string
	gotPath   string
	gotMethod string
}

func (d *envelopeDoer) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	d.gotAuth = req.Header.Get("authorization")
	d.gotCookie = req.Header.Get("Cookie")
	d.gotPath = req.URL.Path
	d.gotMethod = req.Method
	st := d.status
	if st == 0 {
		st = 200
	}
	return &http.Response{StatusCode: st, Body: ioNopCloser(d.body)}, nil
}

func TestYWTBSource_FetchIdentityEndToEnd(t *testing.T) {
	ivBytes := mustHex(t, sampleIVHex)
	ct := mustB64(t, sampleCipherB64)
	dataB64 := b64Of(append(ivBytes, ct...))

	env, _ := json.Marshal(map[string]any{
		"requestId": sampleRequestID,
		"code":      200,
		"msg":       "ok",
		"data":      dataB64,
	})

	d := &envelopeDoer{body: string(env)}
	src, err := schoolauth.NewYWTBSource(schoolauth.YWTBOptions{
		Doer: d, BaseURL: "https://ywtb.example.invalid",
	})
	if err != nil {
		t.Fatalf("NewYWTBSource: %v", err)
	}

	raw, err := src.FetchIdentity(context.Background(), schoolauth.Credential{
		BearerToken: "405162|fake-token-for-test",
		RawCookie:   "laravel_session=fake-session",
	})
	if err != nil {
		t.Fatalf("FetchIdentity: %v", err)
	}

	// 字段映射
	if raw.StudentNo != "202502204" || raw.Name != "姚一飞" || raw.College != "信息工程学院" {
		t.Fatalf("字段映射不符：%+v", raw)
	}
	if raw.IDCardFull != "650102200701142614" {
		t.Fatalf("应带出完整身份证号，实际 %q", raw.IDCardFull)
	}

	// 请求形状：POST + 正确路径 + Bearer + cookie
	if d.gotMethod != http.MethodPost {
		t.Fatalf("应为 POST，实际 %s", d.gotMethod)
	}
	if d.gotPath != "/api/m/v1/user/getUserInfo" {
		t.Fatalf("路径不符：%s", d.gotPath)
	}
	if d.gotAuth != "Bearer 405162|fake-token-for-test" {
		t.Fatalf("Authorization 不符：%q", d.gotAuth)
	}
	if !strings.Contains(d.gotCookie, "laravel_session=") {
		t.Fatalf("应带 laravel_session，实际 %q", d.gotCookie)
	}
}

// 缺 Bearer token 时直接拒绝，不去发请求（纯 cookie 在真实环境是 401）。
func TestYWTBSource_RequiresBearerToken(t *testing.T) {
	d := &envelopeDoer{body: "{}"}
	src, _ := schoolauth.NewYWTBSource(schoolauth.YWTBOptions{Doer: d})
	_, err := src.FetchIdentity(context.Background(), schoolauth.Credential{
		RawCookie: "laravel_session=x",
	})
	if err == nil {
		t.Fatal("缺 Bearer token 应报错")
	}
	if d.gotAuth != "" && d.gotPath != "" {
		t.Fatal("缺 token 时不应发起请求")
	}
}

// 业务码非 200：必须判失败（会话失效时上游常返 200 + 非 200 业务码）。
func TestYWTBSource_NonOKBusinessCodeFails(t *testing.T) {
	env, _ := json.Marshal(map[string]any{
		"requestId": "x", "code": 401, "msg": "未登录", "data": "",
	})
	d := &envelopeDoer{body: string(env)}
	src, _ := schoolauth.NewYWTBSource(schoolauth.YWTBOptions{Doer: d})
	_, err := src.FetchIdentity(context.Background(), schoolauth.Credential{BearerToken: "1|t"})
	if err == nil {
		t.Fatal("业务码非 200 应报错")
	}
}

func TestYWTBSource_HTTPErrorFails(t *testing.T) {
	d := &envelopeDoer{body: "unauthorized", status: 401}
	src, _ := schoolauth.NewYWTBSource(schoolauth.YWTBOptions{Doer: d})
	_, err := src.FetchIdentity(context.Background(), schoolauth.Credential{BearerToken: "1|t"})
	if err == nil {
		t.Fatal("HTTP 401 应报错")
	}
}

// 编译期断言：YWTBSource 满足 AuthSource。
var _ schoolauth.AuthSource = (*schoolauth.YWTBSource)(nil)

var _ = time.Second
