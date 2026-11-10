package schoolauth

// 本文件放「统一身份认证响应体解密」这一公共能力。
//
// 它与具体取数路径无关：换票（jiaowu）与其它任何走 /api/v1/* 的地方都共用同一套
// 信封格式与解密算法，所以单独成文件，不跟着某条路径的增删而移动。
//
// 实测协议（2026-11-09）：
//
//	响应：{"requestId":"<32位hex>","code":200,"msg":"...","data":"<加密 base64>"}
//	key  = MD5(requestId + ":sicau-cas") 的 digest 原始 16 字节（AES-128）
//	iv   = base64(data) 的前 16 字节
//	ct   = 其余；AES-CBC + 自定义尾部 padding（末字节 N 表示去掉 N 字节）

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"fmt"
)

// ywtbSalt 是解密 salt。来自实测：key = MD5(requestId + ":sicau-cas")。
const ywtbSalt = ":sicau-cas"

// ywtbEnvelope 是统一身份认证各接口共用的响应信封。
type ywtbEnvelope struct {
	RequestID string `json:"requestId"`
	Code      int    `json:"code"`
	Msg       string `json:"msg"`
	Data      string `json:"data"`
}

// DecryptYWTB 解密统一身份认证的加密响应体。
//
// 调用方负责先判断业务码（code != 200 时 data 通常为空，不要硬解）。
func DecryptYWTB(requestID, dataB64 string) ([]byte, error) {
	if requestID == "" {
		return nil, fmt.Errorf("schoolauth: 缺少 requestId，无法派生密钥")
	}
	raw, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return nil, fmt.Errorf("schoolauth: data 不是合法 base64：%w", err)
	}
	if len(raw) < aes.BlockSize*2 {
		return nil, fmt.Errorf("schoolauth: 密文过短（%d 字节）", len(raw))
	}

	sum := md5.Sum([]byte(requestID + ywtbSalt))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}

	iv := raw[:aes.BlockSize]
	ct := raw[aes.BlockSize:]
	if len(ct)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("schoolauth: 密文长度 %d 不是块大小倍数", len(ct))
	}

	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ct)
	return stripPKCS7(out)
}

// stripPKCS7 去掉 PKCS7 填充；填充非法时原样返回（让上层解析失败而不是静默截断）。
func stripPKCS7(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return b, nil
	}
	n := int(b[len(b)-1])
	if n == 0 || n > aes.BlockSize || n > len(b) {
		return b, fmt.Errorf("schoolauth: 填充长度非法（%d）", n)
	}
	for i := len(b) - n; i < len(b); i++ {
		if int(b[i]) != n {
			return b, fmt.Errorf("schoolauth: 填充字节不一致")
		}
	}
	return b[:len(b)-n], nil
}
