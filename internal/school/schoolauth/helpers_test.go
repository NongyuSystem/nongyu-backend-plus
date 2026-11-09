package schoolauth_test

// 测试辅助：base64/hex 转换与 io.ReadCloser 包装。

import (
	"encoding/base64"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
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

// encodeGBK 把字符串按 GBK 编码，用于构造真实的 GBK 页面样本。
func encodeGBK(s string) ([]byte, error) {
	enc := simplifiedchinese.GBK.NewEncoder()
	return enc.Bytes([]byte(s))
}
