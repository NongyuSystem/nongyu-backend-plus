// 临时工具：验证 ywtb getUserInfo 的 data 解密算法。
// 用法：go run ./tools/decrypt-probe -req <requestId> -ct <base64密文(不含IV)>
//
// 待验证的假设（来自既有知识库「川农 e 认证响应体解密」）：
//   key = MD5(requestId + ":sicau-cas") 的十六进制串按字节解释（32 字节 → 取前 16 作 AES-128 key）
//   IV  = 密文前 16 字节（本工具接收的已经是去掉 IV 的密文）
//   AES-128-CBC + NoPadding，明文末尾的 N 字节为填充长度，需裁掉
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"strings"
)

func main() {
	req := flag.String("req", "", "requestId")
	ctB64 := flag.String("ct", "", "base64 密文（不含 IV）")
	ivHex := flag.String("iv", "", "IV 的十六进制（可留空则尝试全零）")
	flag.Parse()

	rawCT, err := base64.StdEncoding.DecodeString(*ctB64)
	if err != nil {
		fmt.Println("密文 base64 解码失败：", err)
		return
	}

	sum := md5.Sum([]byte(*req + ":sicau-cas"))
	keyHex := hex.EncodeToString(sum[:])
	fmt.Println("key(md5 hex) =", keyHex, "len =", len(keyHex))

	// 候选 key：hex 串的前 16 字节，或 digest 本身
	candidates := map[string][]byte{
		"hex[:16]": []byte(keyHex[:16]),
		"hex[16:]": []byte(keyHex[16:]),
		"digest":   sum[:],
	}

	var iv []byte
	if *ivHex != "" {
		iv, _ = hex.DecodeString(*ivHex)
	} else {
		iv = make([]byte, 16)
	}
	if len(iv) != 16 {
		fmt.Println("IV 长度不是 16：", len(iv))
		return
	}

	for name, key := range candidates {
		for _, mod := range []string{"cbc"} {
			pt, err := decryptCBC(key, iv, rawCT)
			if err != nil {
				fmt.Printf("%-10s %-4s 失败：%v\n", name, mod, err)
				continue
			}
			// 尝试两种裁剪：标准 PKCS7，以及「末尾字节 N → 去掉 N 字节」
			std := stripPKCS7(pt)
			last := pt[len(pt)-1]
			custom := pt
			if int(last) > 0 && int(last) <= len(pt) {
				custom = pt[:len(pt)-int(last)]
			}
			fmt.Printf("%-10s %-4s key=%s\n", name, mod, hex.EncodeToString(key))
			fmt.Printf("   PKCS7 : %q\n", truncate(std, 400))
			fmt.Printf("   custom: %q\n", truncate(custom, 400))
			if looksJSON(std) || looksJSON(custom) {
				fmt.Println("   *** 命中 JSON ***")
			}
		}
	}
}

func decryptCBC(key, iv, ct []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(ct)%block.BlockSize() != 0 {
		return nil, fmt.Errorf("密文长度 %d 不是块大小的倍数", len(ct))
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ct)
	return out, nil
}

func stripPKCS7(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	n := int(b[len(b)-1])
	if n <= 0 || n > len(b) || n > 16 {
		return b
	}
	for i := len(b) - n; i < len(b); i++ {
		if int(b[i]) != n {
			return b
		}
	}
	return b[:len(b)-n]
}

func looksJSON(b []byte) bool {
	s := strings.TrimSpace(string(b))
	return strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
