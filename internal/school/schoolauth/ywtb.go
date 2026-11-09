package schoolauth

// 本文件实现「ywtb 一网通办」的真实取数链路。
//
// 实测协议（2026-11-09，见知识库「川农 ywtb getUserInfo 真实取数」）：
//
//	POST https://ywtb.sicau.edu.cn/api/m/v1/user/getUserInfo
//	authorization: Bearer <用户ID>|<随机串>      ← 不是纯 cookie，XSRF-TOKEN 也不行
//	cookie: laravel_session=...
//	content-type: application/json
//	body: {}
//
//	响应：{"requestId":"...","code":200,"msg":"ok","data":"<加密 base64>"}
//
//	解密：key = MD5(requestId + ":sicau-cas") 的 **digest 原始 32 字节**（AES-256）
//	      iv  = base64(data) 的前 16 字节
//	      ct  = 其余；AES-256-CBC + PKCS7
//
//	明文（节选）：
//	  {"number":"202502204","name":"姚一飞","idcard":"650102200701142614",
//	   "unit":"信息工程学院","phone":"17799611327","wechat":"...",
//	   "birthday":"2007-01-14T08:00:00+08:00","groups":["bzks"],"status":1}
//
// 注意：专业/班级/年级**不在这里**，要走教务 bjiben.asp（另一路）。

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"nongyu-backend-plus/internal/platform"
)

// ywtbSalt 是解密 salt。来自实测：key = MD5(requestId + ":sicau-cas")。
const ywtbSalt = ":sicau-cas"

// maxYWTBBody 是响应体上限：正常只有几百字节，超限说明拿到的不是预期内容。
const maxYWTBBody = 1 << 20

// YWTBOptions 是 ywtb 取数源构造参数。
type YWTBOptions struct {
	// Doer 是出站闸口（白名单、超时、3xx 拦截都在它里面）。
	Doer platform.HTTPDoer
	// BaseURL 默认 https://ywtb.sicau.edu.cn；测试时可指向本地。
	BaseURL string
}

// YWTBSource 实现 AuthSource：用客户端交上来的 Bearer token 与 cookie 取身份。
type YWTBSource struct {
	doer    platform.HTTPDoer
	baseURL string
}

// NewYWTBSource 构造 ywtb 取数源。
func NewYWTBSource(o YWTBOptions) (*YWTBSource, error) {
	if o.Doer == nil {
		return nil, fmt.Errorf("schoolauth: YWTBSource 需要 HTTPDoer")
	}
	base := o.BaseURL
	if base == "" {
		base = "https://ywtb.sicau.edu.cn"
	}
	return &YWTBSource{doer: o.Doer, baseURL: strings.TrimRight(base, "/")}, nil
}

// ywtbUserInfo 是 getUserInfo 解密后的明文结构（只列用得到的字段）。
type ywtbUserInfo struct {
	Number   string   `json:"number"`   // 学号
	Name     string   `json:"name"`     // 姓名
	Gender   string   `json:"gender"`   // 性别
	IDCard   string   `json:"idcard"`   // 完整身份证号
	Unit     string   `json:"unit"`     // 所属单位（= 学院）
	Phone    string   `json:"phone"`    // 手机号
	Email    string   `json:"email"`    //
	QQ       string   `json:"qq"`       //
	Wechat   string   `json:"wechat"`   // 微信 openid
	Birthday string   `json:"birthday"` //
	Status   int      `json:"status"`   //
	Groups   []string `json:"groups"`   //
}

// ywtbEnvelope 是 getUserInfo 的响应信封。
type ywtbEnvelope struct {
	RequestID string `json:"requestId"`
	Code      int    `json:"code"`
	Msg       string `json:"msg"`
	Data      string `json:"data"`
}

// FetchIdentity 实现 AuthSource：取身份并解出明文。
//
// cred.BearerToken 必须是 `用户ID|随机串` 形式；cred.CookieHeader 需含 laravel_session。
func (s *YWTBSource) FetchIdentity(ctx context.Context, cred Credential) (RawIdentity, error) {
	if strings.TrimSpace(cred.BearerToken) == "" {
		return RawIdentity{}, fmt.Errorf("schoolauth: 缺少 Bearer token")
	}

	body, err := s.postUserInfo(ctx, cred)
	if err != nil {
		return RawIdentity{}, err
	}

	var env ywtbEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return RawIdentity{}, fmt.Errorf("schoolauth: 响应信封解析失败：%w", err)
	}
	// 会话失效时上游可能返回 200 + 非 200 的业务码，必须按业务码判失败。
	if env.Code != 200 {
		return RawIdentity{}, fmt.Errorf("schoolauth: 上游业务码 %d（%s）", env.Code, env.Msg)
	}
	if env.Data == "" {
		return RawIdentity{}, fmt.Errorf("schoolauth: 响应缺少 data")
	}

	plain, err := DecryptYWTB(env.RequestID, env.Data)
	if err != nil {
		return RawIdentity{}, err
	}

	var info ywtbUserInfo
	if err := json.Unmarshal(plain, &info); err != nil {
		return RawIdentity{}, fmt.Errorf("schoolauth: 明文解析失败：%w", err)
	}
	if info.Number == "" {
		return RawIdentity{}, fmt.Errorf("schoolauth: 明文缺少学号")
	}

	return RawIdentity{
		StudentNo:    info.Number,
		Name:         info.Name,
		College:      info.Unit,
		IDCardMasked: info.IDCard,
		// IDCardFull 是完整号；由上层立即加密，本层不留存。
		IDCardFull: info.IDCard,
	}, nil
}

// postUserInfo 发起 getUserInfo 请求并读回原始响应体。
func (s *YWTBSource) postUserInfo(ctx context.Context, cred Credential) ([]byte, error) {
	url := s.baseURL + "/api/m/v1/user/getUserInfo"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "*/*")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+strings.TrimSpace(cred.BearerToken))
	if c := cred.CookieHeader(); c != "" {
		req.Header.Set("Cookie", c)
	}

	resp, err := s.doer.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("schoolauth: ywtb 返回 %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxYWTBBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxYWTBBody {
		return nil, fmt.Errorf("schoolauth: 响应体超过上限")
	}
	return raw, nil
}

// DecryptYWTB 解密 ywtb 的 data 字段。
//
// key = MD5(requestId + ":sicau-cas") 的 digest 原始字节（32 字节 → AES-256）；
// base64 解码后前 16 字节是 IV，其余是密文；AES-256-CBC + PKCS7。
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
		return nil, fmt.Errorf("schoolauth: 明文为空")
	}
	n := int(b[len(b)-1])
	if n <= 0 || n > aes.BlockSize || n > len(b) {
		return nil, fmt.Errorf("schoolauth: 填充长度非法（%d）", n)
	}
	for i := len(b) - n; i < len(b); i++ {
		if int(b[i]) != n {
			return nil, fmt.Errorf("schoolauth: 填充字节不一致")
		}
	}
	return b[:len(b)-n], nil
}
