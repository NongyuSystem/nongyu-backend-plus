package schoolauth_test

// 测试辅助：base64/hex 转换与 io.ReadCloser 包装。

import (
	"encoding/base64"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex 解码失败 %q：%v", s, err)
	}
	return b
}

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64 解码失败：%v", err)
	}
	return b
}

func b64Of(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

type nopCloser struct{ io.Reader }

func (nopCloser) Close() error { return nil }

func ioNopCloser(s string) io.ReadCloser { return nopCloser{strings.NewReader(s)} }
