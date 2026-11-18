package platform_test

// #21 M9：mock 与真实实现命名可区分。
// MockCipher 实际是真实 AES-256-GCM，名字误导；补 AESCipher 别名，
// 让装配代码可以写出「这是真实实现」的名字，同时不破坏既有调用方。

import (
	"bytes"
	"testing"

	"nongyu-backend-plus/internal/platform"
)

func TestAESCipher_IsRealAESNotAMock(t *testing.T) {
	c := platform.NewAESCipher("issue21-key")
	// 编译期：别名必须仍满足 Cipher 接口
	var _ platform.Cipher = c

	const plain = "农屿真实加密，不是占位桩"
	sealed, err := c.Seal([]byte(plain))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Equal(sealed, []byte(plain)) {
		t.Fatal("密文不得等于明文")
	}
	got, err := c.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(got) != plain {
		t.Fatalf("还原不符：%q", got)
	}
}
