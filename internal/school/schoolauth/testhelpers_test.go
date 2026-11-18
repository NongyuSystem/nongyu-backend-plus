package schoolauth_test

// 跨文件共享的测试辅助。
//
// 为什么单独放一份：这些 helper 被 ≥2 个测试文件引用，若留在任何一个业务测试
// 文件里，其他文件就会反向依赖它，拆分后就无法独立存在。因此按「被两个以上
// 文件用到」的判据统一抽到这里：
//
//   - mustHex / mustB64 / b64Of / ioNopCloser：ywtb 解密测试与教务解析测试都用
//   - cred：适配器测试与凭据安全测试都用（本文件内暂未被引用，
//     是 acaemic/adapter 与凭据相关用例共用的构造器）
//   - newMemTGTStore / newTestManager：tgt 相关测试与 tgt_shape_test.go 都用

import (
	"encoding/base64"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/schoolauth"
)

// mustHex 解码十六进制字符串，失败即终止用例。
func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex 解码失败 %q：%v", s, err)
	}
	return b
}

// mustB64 解码标准 base64 字符串，失败即终止用例。
func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64 解码失败：%v", err)
	}
	return b
}

// b64Of 把字节编码成标准 base64 字符串。
func b64Of(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// nopCloser 把 io.Reader 包装成 io.ReadCloser（测试用响应体）。
type nopCloser struct{ io.Reader }

func (nopCloser) Close() error { return nil }

// ioNopCloser 把字符串包成 io.ReadCloser。
func ioNopCloser(s string) io.ReadCloser { return nopCloser{strings.NewReader(s)} }

// cred 用 "名字=值" 形式的参数构造结构化的 Credential。
func cred(cookies ...string) schoolauth.Credential {
	c := schoolauth.Credential{}
	for _, kv := range cookies {
		parts := strings.SplitN(kv, "=", 2)
		c.Cookies = append(c.Cookies, schoolauth.SessionCookie{Name: parts[0], Value: parts[1]})
	}
	return c
}

// newMemTGTStore 构造一个空的内存 TicketStore。
func newMemTGTStore() *memTGTStore {
	return &memTGTStore{data: map[string]struct {
		sealed []byte
		exp    time.Time
	}{}}
}

// newTestManager 用内存 store 与固定时钟构造一个 TGTManager。
// 注意：memTGTStore 类型本身定义在 tgt_test.go，同包内可见。
func newTestManager(t *testing.T, store *memTGTStore, clock platform.Clock) *schoolauth.TGTManager {
	t.Helper()
	m, err := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:  store,
		Cipher: platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source: mustJiaowuSource(t),
		Clock:  clock,
	})
	if err != nil {
		t.Fatalf("NewTGTManager: %v", err)
	}
	return m
}
