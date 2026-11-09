// 临时工具：解密统一身份认证平台的换票响应，取出 service ticket。
// 用法：go run ./tools/decrypt-probe -req <requestId> -iv <ivHex> -ct <base64密文>
//
// 也支持直接给完整 data（base64）：-data <base64>
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
	ivHex := flag.String("iv", "", "IV 的十六进制")
	full := flag.String("data", "", "完整 data（base64，含 IV）")
	flag.Parse()

	var raw []byte
	var err error
	switch {
	case *full != "":
		raw, err = base64.StdEncoding.DecodeString(*full)
		if err != nil {
			fmt.Println("data base64 解码失败：", err)
			return
		}
	case *ctB64 != "":
		ct, e := base64.StdEncoding.DecodeString(*ctB64)
		if e != nil {
			fmt.Println("密文 base64 解码失败：", e)
			return
		}
		iv, e := hex.DecodeString(*ivHex)
		if e != nil {
			fmt.Println("IV 十六进制解码失败：", e)
			return
		}
		raw = append(iv, ct...)
	default:
		fmt.Println("需要 -data 或 -ct")
		return
	}

	sum := md5.Sum([]byte(*req + ":sicau-cas"))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		fmt.Println("aes:", err)
		return
	}
	iv := raw[:aes.BlockSize]
	ct := raw[aes.BlockSize:]
	if len(ct)%aes.BlockSize != 0 {
		fmt.Printf("密文长度 %d 不是块大小倍数\n", len(ct))
		return
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ct)

	// 去 PKCS7
	n := int(out[len(out)-1])
	if n > 0 && n <= aes.BlockSize && n <= len(out) {
		ok := true
		for i := len(out) - n; i < len(out); i++ {
			if int(out[i]) != n {
				ok = false
				break
			}
		}
		if ok {
			out = out[:len(out)-n]
		}
	}
	s := string(out)
	if strings.HasPrefix(strings.TrimSpace(s), "{") {
		fmt.Println("=== 明文 JSON ===")
		fmt.Println(s)
		return
	}
	fmt.Printf("不是 JSON，前 200 字节：%q\n", s[:min(200, len(s))])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
