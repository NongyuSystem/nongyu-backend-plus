package platform_test

import (
	"bytes"
	"testing"

	"nongyu-backend-plus/internal/platform"
)

// AEAD 语义测试：Authenticated Encryption 必须满足
//   - 同一明文两次封装产出不同密文（随机 nonce）
//   - 密文不含明文
//   - 错误密钥 / 篡改 / 截断一律拒绝
// 这些是「可上生产」相对骨架期 XOR 占位的关键增量。

func TestCipher_IsAuthenticatedEncryption(t *testing.T) {
	c := platform.NewMockCipher("production-key-from-config")
	plain := []byte("TICKET-SUPER-SECRET-VALUE")

	s1, err := c.Seal(plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	s2, err := c.Seal(plain)
	if err != nil {
		t.Fatalf("Seal #2: %v", err)
	}
	if bytes.Equal(s1, s2) {
		t.Fatal("AEAD 每次封装应使用随机 nonce，两次 Seal 不得相同（当前是确定性 XOR 占位）")
	}
	if bytes.Contains(s1, plain) {
		t.Fatalf("密文不得包含明文，实际 %q", s1)
	}
	got, err := c.Open(s1)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("往返失败：got=%q err=%v", got, err)
	}
	// 第二次密文也必须可解（nonce 随密文一起存放）。
	got2, err := c.Open(s2)
	if err != nil || !bytes.Equal(got2, plain) {
		t.Fatalf("第二次密文往返失败：got=%q err=%v", got2, err)
	}

	// 错误密钥
	other := platform.NewMockCipher("a-totally-different-key")
	if _, err := other.Open(s1); err == nil {
		t.Fatal("错误密钥 Open 必须失败")
	}
	// 篡改（含 nonce 区与密文区各一处）
	for _, idx := range []int{5, len(s1) - 1} {
		tampered := append([]byte(nil), s1...)
		tampered[idx] ^= 0x01
		if _, err := c.Open(tampered); err == nil {
			t.Fatalf("篡改第 %d 字节后 Open 必须失败", idx)
		}
	}
	// 截断
	if _, err := c.Open(s1[:len(s1)/2]); err == nil {
		t.Fatal("截断的密文必须失败")
	}
	// 空明文也能往返
	empty, err := c.Seal(nil)
	if err != nil {
		t.Fatalf("Seal(nil): %v", err)
	}
	back, err := c.Open(empty)
	if err != nil || len(back) != 0 {
		t.Fatalf("空明文往返失败：got=%q err=%v", back, err)
	}
}
