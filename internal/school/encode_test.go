package school

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// 主机名密文的基准（不含 KEY_HEX 前缀），逐字来自
// docs/tech/旧农屿WebVPN可重写契约清单.md §2 第 91-92 行
// （源头：旧仓 core/webvpn/encode.selfcheck.ts:6-14）。
const (
	wantAuthCipher   = "f1e25594692361537f1dc7a99c406d36ef"
	wantJiaowuCipher = "fafe409330252643770b88b9d65027203418e0"
	keyHexPrefix     = "77726476706e69737468656265737421"
)

// hostCipherFrom 从 Encode 产出的网关地址里取出「主机名密文」这一段，
// 用来和 selfcheck 基准逐字比对。它只解析 Encode 的外部输出，不碰实现内部。
// 形态：/https/<KEY_HEX><host_cipher_hex>/<原 path>
func hostCipherFrom(encoded string) string {
	u, err := url.Parse(encoded)
	if err != nil {
		return ""
	}
	parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 3)
	if len(parts) < 2 || parts[0] != "https" {
		return ""
	}
	segment := parts[1]
	if !strings.HasPrefix(segment, keyHexPrefix) {
		return ""
	}
	return strings.TrimPrefix(segment, keyHexPrefix)
}

// TestEncodeKnownHostCiphertext 锁住两个已知 host 的密文与基准逐字一致，
// 同时锁定整体形态（网关 origin / scheme / KEY_HEX 前缀）与 path+query 明文。
func TestEncodeKnownHostCiphertext(t *testing.T) {
	cases := []struct {
		name       string
		rawURL     string
		wantCipher string
		wantSuffix string
	}{
		{"auth", "https://auth.sicau.edu.cn/api/v1/captcha?appid=x", wantAuthCipher, "/api/v1/captcha?appid=x"},
		{"jiaowu", "https://jiaowu.sicau.edu.cn/jiaoshi/aspsso/caslogin.asp?ticket=ST", wantJiaowuCipher, "/jiaoshi/aspsso/caslogin.asp?ticket=ST"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Encode(tc.rawURL)
			if err != nil {
				t.Fatalf("Encode(%q) 返回错误：%v", tc.rawURL, err)
			}
			prefix := "https://webvpn.sicau.edu.cn/https/" + keyHexPrefix + tc.wantCipher
			if !strings.HasPrefix(got, prefix) {
				t.Fatalf("编码地址前缀不符\n  got:  %s\n  want prefix: %s", got, prefix)
			}
			if !strings.HasSuffix(got, tc.wantSuffix) {
				t.Fatalf("path/query 未保持明文\n  got:  %s\n  want suffix: %s", got, tc.wantSuffix)
			}
			if cipher := hostCipherFrom(got); cipher != tc.wantCipher {
				t.Fatalf("主机名密文与基准不一致\n  got:  %s\n  want: %s", cipher, tc.wantCipher)
			}
			t.Logf("编码结果：%s", got)
			t.Logf("主机名密文（不含 KEY_HEX）：%s", tc.wantCipher)
		})
	}
}

// TestEncodeAlreadyWebVPNPassthrough 已是网关域的地址原样返回，不重复编码。
func TestEncodeAlreadyWebVPNPassthrough(t *testing.T) {
	in := "https://webvpn.sicau.edu.cn/https/77726476706e69737468656265737421deadbeef/some/path?x=1"
	got, err := Encode(in)
	if err != nil {
		t.Fatalf("Encode 返回错误：%v", err)
	}
	if got != in {
		t.Fatalf("已是网关域应原样返回\n  got:  %s\n  want: %s", got, in)
	}
}

// TestResolveUnknownTargetNameRejected 未知逻辑目标名被拒，且是带明确错误码的错误。
func TestResolveUnknownTargetNameRejected(t *testing.T) {
	_, err := Resolve(TargetName("unknown"))
	if err == nil {
		t.Fatal("未知逻辑目标名应被拒绝")
	}
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("错误类型应为 *Error，实际 %T: %v", err, err)
	}
	if se.Code != CodeTargetDenied {
		t.Fatalf("错误码应为 CodeTargetDenied，实际 %q", se.Code)
	}
}

// TestResolveRejectsURLLikeName 结构性保证：调用方只能传逻辑名，
// 把 URL 形态的字符串当目标名传入同样不在白名单、必须被拒。
func TestResolveRejectsURLLikeName(t *testing.T) {
	_, err := Resolve(TargetName("https://evil.example.com/x"))
	if err == nil {
		t.Fatal("把 URL 当目标名传入应被拒绝")
	}
}

// TestResolveKnownTargetsHaveBothShapes 每个内置目标都同时给出直连与经网关两种形态。
func TestResolveKnownTargetsHaveBothShapes(t *testing.T) {
	cases := []struct {
		name   TargetName
		host   string
		cipher string
	}{
		{TargetJiaowu, "jiaowu.sicau.edu.cn", wantJiaowuCipher},
		{TargetAuth, "auth.sicau.edu.cn", wantAuthCipher},
	}
	for _, tc := range cases {
		t.Run(string(tc.name), func(t *testing.T) {
			tgt, err := Resolve(tc.name)
			if err != nil {
				t.Fatalf("Resolve(%q) 返回错误：%v", tc.name, err)
			}
			if tgt.Name != tc.name {
				t.Fatalf("Name 不符：got %q want %q", tgt.Name, tc.name)
			}
			if tgt.Host != tc.host {
				t.Fatalf("Host 不符：got %q want %q", tgt.Host, tc.host)
			}
			if tgt.Direct == nil || tgt.Vpn == nil {
				t.Fatal("直连 / 经网关两种形态都不能为空")
			}
			if got := tgt.Direct.String(); got != "https://"+tc.host {
				t.Fatalf("直连形态不符：%s", got)
			}
			vpn := tgt.Vpn.String()
			if !strings.HasPrefix(vpn, "https://webvpn.sicau.edu.cn/https/"+keyHexPrefix+tc.cipher) {
				t.Fatalf("经网关形态不符：%s", vpn)
			}
		})
	}

	// webvpn 目标就是网关域自身，直连与经网关形态一致（不重复编码）。
	wv, err := Resolve(TargetWebvpn)
	if err != nil {
		t.Fatalf("Resolve(webvpn) 返回错误：%v", err)
	}
	if wv.Host != "webvpn.sicau.edu.cn" {
		t.Fatalf("webvpn 目标 Host 不符：%s", wv.Host)
	}
	if wv.Direct == nil || wv.Vpn == nil || wv.Direct.String() != wv.Vpn.String() {
		t.Fatalf("webvpn 目标不应被重复编码：direct=%v vpn=%v", wv.Direct, wv.Vpn)
	}
}

// TestEncodeNonHTTPPassthrough 空串、相对路径、非 http(s) 一律原样返回。
func TestEncodeNonHTTPPassthrough(t *testing.T) {
	for _, in := range []string{"", "/relative/path", "ftp://host/x", "mailto:a@b.c"} {
		got, err := Encode(in)
		if err != nil {
			t.Fatalf("Encode(%q) 返回错误：%v", in, err)
		}
		if got != in {
			t.Fatalf("非 http(s) 应原样返回\n  got:  %q\n  want: %q", got, in)
		}
	}
}

// TestEncodeBoundaryInputsDoNotPanic 边界 / 非法输入不得 panic。
func TestEncodeBoundaryInputsDoNotPanic(t *testing.T) {
	inputs := []string{
		"",
		"/relative/path",
		"not a url",
		"://bad",
		"http://%zz/x",
		"https://",
		"http://[::1",
		"HTTPS://AUTH.SICAU.EDU.CN/x",
	}
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Encode(%q) 不应 panic，实际：%v", in, r)
				}
			}()
			_, _ = Encode(in)
		}()
	}
}

// TestEncodePortSchemeSegment 覆盖 issue #12「本票可不实现端口，但要留结构」：
// 带显式端口时，scheme 段按探索笔记记录为 /https-443/ 形态，且端口从主机名中
// 剥离（不参与 host 加密）。来源见契约清单 §2:94 与「尚未查清」#1（该形态只在
// 探索笔记/规格，TS 源码 encodeWebVpnUrl 直接丢弃端口）——这里保留结构并锁定它。
func TestEncodePortSchemeSegment(t *testing.T) {
	got, err := Encode("https://auth.sicau.edu.cn:443/api/v1/captcha")
	if err != nil {
		t.Fatalf("Encode 返回错误：%v", err)
	}
	wantPrefix := "https://webvpn.sicau.edu.cn/https-443/" + keyHexPrefix + wantAuthCipher
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("带端口地址应落在 /https-443/ 段\n  got:  %s\n  want prefix: %s", got, wantPrefix)
	}
	if got != wantPrefix+"/api/v1/captcha" {
		t.Fatalf("带端口地址 path 应保持明文\n  got:  %s", got)
	}
}

// TestEncodeIsPureAndRepeatable Encode 是纯函数：只调用标准库纯函数、不读外部状态、
// 不发起网络请求。可观察证据是对同一输入重复调用结果逐字一致、无全局状态漂移。
func TestEncodeIsPureAndRepeatable(t *testing.T) {
	in := "https://auth.sicau.edu.cn/api/v1/captcha?appid=x"
	first, err := Encode(in)
	if err != nil {
		t.Fatalf("Encode 返回错误：%v", err)
	}
	for i := 0; i < 50; i++ {
		got, err := Encode(in)
		if err != nil {
			t.Fatalf("第 %d 次调用返回错误：%v", i, err)
		}
		if got != first {
			t.Fatalf("第 %d 次调用结果漂移\n  first: %s\n  got:   %s", i, first, got)
		}
	}
}
