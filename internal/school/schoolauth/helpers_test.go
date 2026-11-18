package schoolauth_test

// 测试辅助：教学大纲编码/解码的专属 helper。
//
// 注意：mustHex / mustB64 / b64Of / ioNopCloser 这几个被多个文件共用的转换 helper
// 已统一移到 testhelpers_test.go，本文件只保留教务解析专属的 GBK 编码器。
// nopCloser 类型也随 ioNopCloser 一并移走，同包内可见。

import (
	"golang.org/x/text/encoding/simplifiedchinese"
)

// encodeGBK 把字符串按 GBK 编码，用于构造真实的 GBK 页面样本。
func encodeGBK(s string) ([]byte, error) {
	enc := simplifiedchinese.GBK.NewEncoder()
	return enc.Bytes([]byte(s))
}
