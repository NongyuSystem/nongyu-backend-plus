package schoolauth

// 本文件实现「一次登录覆盖多站点」的唯一解（2026-11-10 实测，见
// docs/tech/实测-川农一次登录覆盖多站点的唯一解.md）。
//
// 两张凭据，各管一半：
//
//	① go-admin-tgt —— 管所有「认证类站点」（教务 / WebVPN / 学生工作 / 财务 …）
//	   对任意**已注册 appid** 换票：GET auth/v1/cas/login?appid=<UUID>
//	   返回 code=200 + 加密 data，解出 {st, tgt, user, callbackUrl}。
//	   随机 UUID 会返回 code=500「未登录」——appid 必须是平台注册过的。
//
//	② ywtb Bearer —— 只管 ywtb 一个站（它完全不认 go-admin-tgt，会 401）
//	   形如 "405500|xxx"，请求头 Authorization: Bearer <token>。
//	   这是**身份证号的唯一来源**（教务 bjiben.asp 没有身份证）。
//
// 结论：不需要逐站登录。tgt 覆盖认证类站点，ywtb 额外需要一个 Bearer。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"nongyu-backend-plus/internal/platform"
)

// 已知站点在统一身份认证平台注册的 appid（UUID 形式）。
//
// 用名字去打 cas/login 只有 ywtb 会碰巧成功（它恰好被注册成那个名字），
// 其余必须用 UUID。新增站点时在此登记，未登记的站点请传 UUID。
var SiteAppID = map[string]string{
	"jiaowu": "b54a2f27-f7ba-4b91-bbf0-3b2b12244d14", // 教务
	"webvpn": "aea49a21-6b26-4e8f-bcfc-80e3de591c93", // 门户 / WebVPN
	"ywtb":   "ywtb",                                 // 特例：该平台允许用名字
}

const (
	// ywtbHost 是 ywtb 一网通办站点。
	ywtbHost = "ywtb.sicau.edu.cn"
	// maxYWTBBody 是 ywtb 响应体上限：正常只有几百字节。
	maxYWTBBody = 1 << 20
)

// CASUser 是换票响应里携带的用户信息。
type CASUser struct {
	ID     int64  `json:"id"`
	Number string `json:"number"` // 学号
	Name   string `json:"name"`   // 姓名
	Phone  string `json:"phone"`
	Group  []struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Alias string `json:"alias"` // 「本科生」等
	} `json:"group"`
}

// CASResult 是一次换票的结果。
type CASResult struct {
	ST          string  // 给目标站建会话用的 service ticket
	TGT         string  // 响应里带回的新 tgt（可用于滚动续期）
	CallbackURL string  // 目标站的回调地址（主机名即目标站）
	User        CASUser // 身份（换票顺带给出，不必再取一次）
}

// ExchangeCAS 用 go-admin-tgt 为指定 appid 换票。
//
// appid 可以是 SiteAppID 里的站点名，也可以是直接给出的 UUID。
// cookieHeader 为整套同域 cookie；只给 tgt 时退化为只带 tgt（实测可行）。
func ExchangeCAS(ctx context.Context, doer platform.HTTPDoer, authBase, appid, tgt, cookieHeader string) (CASResult, error) {
	if doer == nil {
		return CASResult{}, fmt.Errorf("schoolauth: ExchangeCAS 需要 HTTPDoer")
	}
	if strings.TrimSpace(tgt) == "" {
		return CASResult{}, fmt.Errorf("schoolauth: 缺少 %s", goAdminTgtCookie)
	}
	if authBase == "" {
		authBase = "https://" + authHost
	}
	// 站点名 → UUID（未登记时按原样当作 UUID）
	if id, ok := SiteAppID[appid]; ok {
		appid = id
	}

	u := strings.TrimRight(authBase, "/") + "/api/v1/cas/login?appid=" + appid
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return CASResult{}, err
	}
	req.Header.Set("accept", "*/*")
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120")
	if strings.TrimSpace(cookieHeader) != "" {
		req.Header.Set("Cookie", cookieHeader)
	} else {
		req.Header.Set("Cookie", goAdminTgtCookie+"="+tgt)
	}

	resp, err := doer.Do(ctx, req)
	if err != nil {
		return CASResult{}, fmt.Errorf("%w：换票请求失败：%v", ErrTicketInfra, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxJiaowuBody))
	if err != nil {
		return CASResult{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return CASResult{}, fmt.Errorf("%w：换票返回 %d", ErrTicketInfra, resp.StatusCode)
	}

	var env ywtbEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return CASResult{}, fmt.Errorf("%w：换票响应解析失败：%v", ErrTicketInfra, err)
	}
	if env.Code != 200 {
		// 这是「学校侧明确拒绝」——tgt 无效或该 appid 未注册。
		return CASResult{}, fmt.Errorf("%w：换票业务码 %d（%s）", ErrTicketRejected, env.Code, env.Msg)
	}
	plain, err := DecryptYWTB(env.RequestID, env.Data)
	if err != nil {
		return CASResult{}, fmt.Errorf("%w：换票响应解密失败：%v", ErrTicketInfra, err)
	}

	var t struct {
		ST          string  `json:"st"`
		TGT         string  `json:"tgt"`
		CallbackURL string  `json:"callbackUrl"`
		User        CASUser `json:"user"`
	}
	if err := json.Unmarshal(plain, &t); err != nil {
		return CASResult{}, fmt.Errorf("%w：票据解析失败：%v", ErrTicketInfra, err)
	}
	if t.ST == "" {
		return CASResult{}, fmt.Errorf("%w：换票未返回 service ticket", ErrTicketInfra)
	}
	return CASResult{ST: t.ST, TGT: t.TGT, CallbackURL: t.CallbackURL, User: t.User}, nil
}

// YWTBIdentity 是 ywtb getUserInfo 解密后的完整身份。
//
// 这是**身份证号的唯一来源**；教务档案页没有身份证。
type YWTBIdentity struct {
	StudentNo string   `json:"number"`
	Name      string   `json:"name"`
	Gender    string   `json:"gender"`
	IDCard    string   `json:"idcard"` // 完整号
	Phone     string   `json:"phone"`
	College   string   `json:"unit"` // 所属单位 = 学院
	Birthday  string   `json:"birthday"`
	Email     string   `json:"email"`
	QQ        string   `json:"qq"`
	Wechat    string   `json:"wechat"`
	Avatar    string   `json:"avatar"`
	Groups    []string `json:"groups"`
	PassLevel int      `json:"pass_level"`
	Status    int      `json:"status"`
	ExpireAt  string   `json:"expire_at"`
}

// ywtbGetUserInfoPath 是取完整身份的接口路径。
//
// 坑：ywtb 前端 axios 的 baseURL 是 "/api"，所以完整路径是 /api 加上代码里的
// /m/v1/user/getUserInfo。直接打 /api/m/user/getUserInfo 会 404。
const ywtbGetUserInfoPath = "/api/m/v1/user/getUserInfo"

// FetchYWTBIdentity 用 ywtb 自己的 Bearer token 取完整身份（含身份证）。
//
// 注意：**ywtb 不认 go-admin-tgt**，带 tgt 会 401。这里的 bearer 必须是
// ywtb 自己的 token（形如 "405500|xxx"），浏览器里存于 localStorage 的
// lifeData.data.vuex_token。
func FetchYWTBIdentity(ctx context.Context, doer platform.HTTPDoer, baseURL, bearer string) (YWTBIdentity, error) {
	if doer == nil {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: FetchYWTBIdentity 需要 HTTPDoer")
	}
	if strings.TrimSpace(bearer) == "" {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: 缺少 ywtb Bearer token（它不是 go-admin-tgt）")
	}
	if baseURL == "" {
		baseURL = "https://" + ywtbHost
	}

	u := strings.TrimRight(baseURL, "/") + ywtbGetUserInfoPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader("{}"))
	if err != nil {
		return YWTBIdentity{}, err
	}
	req.Header.Set("accept", "*/*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearer))
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120")

	resp, err := doer.Do(ctx, req)
	if err != nil {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: ywtb 取身份请求失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxYWTBBody))
	if err != nil {
		return YWTBIdentity{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: ywtb 返回 %d（Bearer 可能已失效）", resp.StatusCode)
	}

	var env ywtbEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: ywtb 响应解析失败：%w", err)
	}
	if env.Code != 200 {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: ywtb 业务码 %d（%s）", env.Code, env.Msg)
	}
	plain, err := DecryptYWTB(env.RequestID, env.Data)
	if err != nil {
		return YWTBIdentity{}, err
	}
	var info YWTBIdentity
	if err := json.Unmarshal(plain, &info); err != nil {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: ywtb 明文解析失败：%w", err)
	}
	if strings.TrimSpace(info.StudentNo) == "" {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: ywtb 明文缺少学号")
	}
	return info, nil
}
