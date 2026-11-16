package platform

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// zz_poc_data_test.go —— 安全审计回归守卫（独占文件，package platform 访问未导出符号）。
// 本文件原先是诊断阶段的两条 RED 能力反馈环（实证漏洞存在）；修复落盘后已翻成
// GREEN 回归断言：断言「修复后的安全行为」，漏洞一旦回归即变红。

// ---------------------------------------------------------------------------
// 探针（只打印、不判定）：记录 url.URL.Hostname() 对各类绕过形态的真实行为，
// 作为 hostInAllowlist 判定的事实底座。结论：userinfo@ / 尾点 / IDN 近似串 /
// IPv6 / IPv4 字面量都已被 Go 的 Hostname() + 精确白名单挡住；大小写、端口
// 被正常归一。真正的缺口不在这里，而在「解析后的 IP」（见下方 SSRF 回归）。
// ---------------------------------------------------------------------------

func TestZZ_Probe_HostnameParsingAndAllowlist(t *testing.T) {
	allow := []string{"jiaowu.sicau.edu.cn", "webvpn.sicau.edu.cn"}
	raws := []string{
		"https://jiaowu.sicau.edu.cn/x",
		"https://JIAOWU.SICAU.EDU.CN/x",
		"https://jiaowu.sicau.edu.cn./x",
		"https://jiaowu.sicau.edu.cn:8443/x",
		"https://webvpn.sicau.edu.cn@169.254.169.254/x",
		"https://evil.com@jiaowu.sicau.edu.cn/x",
		"https://jiaowu.sicau.edu.cn@evil.com/x",
		"https://[::1]/x",
		"https://127.0.0.1/x",
	}
	for _, raw := range raws {
		u, err := url.Parse(raw)
		if err != nil {
			t.Logf("PROBE raw=%-55q ParseErr=%v", raw, err)
			continue
		}
		hn := u.Hostname()
		t.Logf("PROBE raw=%-55q Host=%-40q Hostname=%-28q allow=%v",
			raw, u.Host, hn, hostInAllowlist(hn, allow))
	}
}

// ---------------------------------------------------------------------------
// 回归 #1（修复后 GREEN）：弱默认密钥不再可被攻击者解密 tgt / 身份证密文。
//
// 漏洞（已实证 RED）：NewMockCipher("") 曾回落到源码公开常量
// defaultCipherKey="dev-only-placeholder-key-change-me"，攻击者仅凭源码即可
// 构造同一 Cipher 解密 tgt / id_card_sealed 密文。
// 修复：空/占位弱密钥改成本进程随机临时密钥（caps_mock.go: NewMockCipher）。
// 断言：① 受害者空密钥 Cipher 自身仍能 Seal/Open 往返（不破坏既有契约）；
//       ② 攻击者用公开默认常量构造的 Cipher 必须解不开（err != nil）。
// ---------------------------------------------------------------------------

func TestZZ_GREEN_DefaultCipherKeyNoLongerDecryptsTGTAndIDCard(t *testing.T) {
	// 服务在 TICKET_KEY 未配置时走空密钥构造。
	victim := NewAESCipher("")
	plaintext := []byte("tgt=ST-1234567|idcard=510107199001011234")
	sealed, err := victim.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal 失败：%v", err)
	}
	// ① 空密钥仍可自身往返（骨架装配与既有单测依赖这一点）。
	got, err := victim.Open(sealed)
	if err != nil || string(got) != string(plaintext) {
		t.Fatalf("空密钥 Cipher 自身往返应仍可用：got=%q err=%v", got, err)
	}

	// ② 攻击者：拿源码里公开的 defaultCipherKey 常量构造 Cipher，必须解不开。
	attacker := NewAESCipher(defaultCipherKey)
	_, attackerErr := attacker.Open(sealed)
	if attackerErr == nil {
		t.Fatal("弱默认密钥回归：攻击者仍能用源码公开常量解密 tgt/身份证密文，说明空密钥又回落到了公开常量")
	}
	t.Logf("GREEN：公开默认常量已无法解密空密钥封装的密文（攻击者 Open err=%v）", attackerErr)
}

// ---------------------------------------------------------------------------
// 回归 #2（修复后 GREEN）：白名单域名被 DNS rebinding 解析到内网 IP 时零出站。
//
// 漏洞（已实证 RED）：RealHTTPDoer.Do 只按 req.URL.Hostname() 的字符串判白名单，
// 不校验解析结果。允许列表里的域名一旦被解析到 127.0.0.1 / 169.254.169.254，
// 请求会照常建连并打到内网服务 / 云元数据（SSRF）。
// 修复：outbound.go 的 Do 在白名单校验后、建连前调用 guardResolvedIPs，
// 解析 Hostname() 并对环回/私有/链路本地/未指定/组播 IP 零出站拒绝。
// 断言：Do 返回错误（且为 ErrUnsafeResolvedIP）、resp==nil、内网靶子零命中。
// ---------------------------------------------------------------------------

func TestZZ_GREEN_RealHTTPDoer_AllowlistedHostResolvingToPrivateIPIsDenied(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("X-Pwned-By", "ssrf")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("internal-metadata"))
	}))
	defer target.Close()

	_, port, _ := net.SplitHostPort(target.Listener.Addr().String())

	// 极简 DNS 响应器：把任意 A 查询回答成 127.0.0.1；AAAA 回答空 NOERROR。
	dnsPC, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起本地 DNS 响应器失败：%v", err)
	}
	defer dnsPC.Close()
	go serveFakeDNS(dnsPC)

	// 覆盖全局解析器：Go 纯解析器用这个 Dial 连到假 DNS，等价 DNS rebinding。
	orig := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 2 * time.Second}
			return d.DialContext(ctx, "udp", dnsPC.LocalAddr().String())
		},
	}
	defer func() { net.DefaultResolver = orig }()

	t.Setenv("HTTP_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	const allowHost = "jiaowu.sicau.edu.cn" // 明确在内建白名单内
	doer := NewRealHTTPDoer(RealHTTPDoerOptions{
		AllowedHosts: []string{allowHost},
		Timeout:      4 * time.Second,
	})

	reqURL := "http://" + allowHost + ":" + port + "/latest/meta-data/iam/security-credentials/"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, reqURL, nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}

	resp, err := doer.Do(context.Background(), req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	// GREEN 断言：① 必须拒绝（err 非空且为 ErrUnsafeResolvedIP）；② resp 为 nil；
	// ③ 内网靶子零命中（连 tcp 都没建）。
	if err == nil {
		t.Fatalf("SSRF 回归：白名单域名被解析到内网后仍成功出站（resp!=nil=%v），说明 guardResolvedIPs 失效", resp != nil)
	}
	if !errors.Is(err, ErrUnsafeResolvedIP) {
		t.Fatalf("应返回 ErrUnsafeResolvedIP，实际 %v", err)
	}
	if resp != nil {
		t.Fatalf("拒绝时 resp 应为 nil，实际 %+v", resp)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("应零出站，内网靶子被命中 %d 次", got)
	}
	t.Logf("GREEN：解析到内网 IP 已在建连前被零出站拒绝（hits=%d, err=%v）", hits.Load(), err)
}

// serveFakeDNS：在 UDP conn 上应答一个最小 DNS 报文。
func serveFakeDNS(pc net.PacketConn) {
	buf := make([]byte, 1500)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		q := append([]byte(nil), buf[:n]...)
		if len(q) < 12 {
			continue
		}
		qtype := uint16(0)
		i := 12
		for i < len(q) && q[i] != 0 {
			i += int(q[i]) + 1
		}
		i++
		if i+4 <= len(q) {
			qtype = uint16(q[i])<<8 | uint16(q[i+1])
		}
		question := append([]byte(nil), q[12:i+4]...)

		resp := make([]byte, 0, 64)
		resp = append(resp, q[0], q[1], 0x81, 0x80, 0x00, 0x01)
		an := 0
		if qtype == 1 { // A 记录才回答 127.0.0.1
			an = 1
		}
		resp = append(resp, byte(an>>8), byte(an), 0x00, 0x00, 0x00, 0x00)
		resp = append(resp, question...)
		if an == 1 {
			resp = append(resp,
				0xC0, 0x0C, 0x00, 0x01, 0x00, 0x01,
				0x00, 0x00, 0x00, 0x3C, 0x00, 0x04,
				0x7F, 0x00, 0x00, 0x01)
		}
		_, _ = pc.WriteTo(resp, addr)
	}
}
