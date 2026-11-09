package schoolauth_test

// 教务取数测试的辅助：按 ywtb 的同一套算法加密信封内容，以及学籍页样本。

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"testing"
)

// academicPageHTML 是按实测表头构造的学籍页样本（UTF-8 版本）。
const academicPageHTML = `<html><head><meta charset="gb2312"></head><body>
<table class="tablebody">
<tr class="g_column">
<td>序</td><td>考生号</td><td>学号</td><td>姓名</td><td>性别</td>
<td>系别</td><td>专业</td><td>学制</td><td>培养层次</td><td>年级</td>
<td>班级</td><td>新专业</td><td>新班级</td><td>学籍状态</td><td>入学日期</td>
<td>民族</td><td>政治面貌</td><td>家庭通讯地址</td><td>火车站点</td>
<td>个人电话</td><td>家庭电话</td><td>特长</td><td>是否本人照片</td>
<td>锁定信息</td><td>核实和补充个人信息</td>
</tr>
<tr class="g_body_2">
<td>1</td><td>25650101151502</td><td>202502204</td><td>姚一飞</td><td>男</td>
<td>信息工程学院</td><td>数据科学与大数据技术（工学）</td><td>四年</td><td>本科</td><td>2025</td>
<td>大数据202503</td><td>数据科学与大数据技术</td><td>大数据202503</td><td>在读</td><td>2025-9-10</td>
<td>汉族</td><td>群众</td><td>新疆乌鲁木齐市</td><td>乌鲁木齐</td>
<td>17799611327</td><td>0991-1234567</td><td>无</td><td>否</td><td>无</td><td>无</td>
</tr>
</table>
</body></html>`

// encryptForTest 用与 ywtb 相同的算法加密信封内容，生成测试用的响应体。
//
// 参数 requestID 决定密钥；plaintext 是 data 字段解出来的明文。
func encryptForTest(t *testing.T, requestID, plaintext string) string {
	t.Helper()
	sum := md5.Sum([]byte(requestID + ":sicau-cas"))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	// 补齐到块大小并加 PKCS7 填充
	pad := aes.BlockSize - len(plaintext)%aes.BlockSize
	pt := make([]byte, len(plaintext)+pad)
	copy(pt, plaintext)
	for i := len(plaintext); i < len(pt); i++ {
		pt[i] = byte(pad)
	}
	// IV 固定为 16 个 0x11，便于测试确定性
	iv := make([]byte, aes.BlockSize)
	for i := range iv {
		iv[i] = 0x11
	}
	out := make([]byte, len(pt))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, pt)

	raw := append(append([]byte{}, iv...), out...)
	env := map[string]any{
		"requestId": requestID,
		"code":      200,
		"msg":       "ok",
		"data":      base64.StdEncoding.EncodeToString(raw),
	}
	b, _ := json.Marshal(env)
	return string(b)
}
