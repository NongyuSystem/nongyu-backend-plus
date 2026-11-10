// Package cas 是川农统一身份认证（auth.sicau.edu.cn）的 CAS 协议实现。
//
// 职责：用 go-admin-tgt 换票、解密换票响应、取 ywtb 身份、以及登记各站点
// 在认证平台注册的 appid。
//
// 与 xsgz 包的分工：cas 只管「你是谁」（认证域），xsgz 只管「你的档案」
// （学工业务域）。两者互不 import，由 schoolauth 适配层串起来。
package cas

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"nongyu-backend-plus/internal/platform"
)

const (
	// goAdminTgtCookie 是统一身份认证的全局登录态 cookie 名。
	goAdminTgtCookie = "go-admin-tgt"
	// maxBody 是响应体上限：换票响应只有几百字节。
	maxBody = 1 << 20
	// ywtbHost 是一网通办站点。
	ywtbHost = "ywtb.sicau.edu.cn"
)

// SiteAppID 是已知站点在统一身份认证平台注册的 appid（UUID 形式）。
//
// 用名字去打 cas/login 只有 ywtb 会碰巧成功（它恰好被注册成那个名字），
// 其余必须用 UUID。新增站点时在此登记，未登记的站点请传 UUID。
var SiteAppID = map[string]string{
	"jiaowu": "b54a2f27-f7ba-4b91-bbf0-3b2b12244d14", // 教务
	"webvpn": "aea49a21-6b26-4e8f-bcfc-80e3de591c93", // 门户 / WebVPN
	"ywtb":   "ywtb",                                 // 特例：该平台允许用名字
}

// SiteKind 说明一个站点「能不能拿个人数据」，这决定了统一认证对它的价值。
//
// 2026-11-10 逐站实测结论（详见 docs/tech/实测-川农一次登录覆盖多站点的唯一解.md）：
// 用户给的 8 个站里，只有 3 个真能取个人数据；其余是资讯门户或独立账号体系。
// 「认证层全通」不等于「业务数据可取」——这个区分必须留在代码里，
// 否则后人会以为「8 个站都能抓」，实际抓不到。
type SiteKind string

const (
	// SitePersonal 有个人数据，且走统一认证：换票即可取数。
	SitePersonal SiteKind = "personal"
	// SitePortal 纯资讯门户：没有个人数据，认证与否都一样。
	SitePortal SiteKind = "portal"
	// SiteOwnAccount 有自己的账号体系（不认统一认证）：必须另行登录。
	SiteOwnAccount SiteKind = "own_account"
	// SiteGateway VPN 网关：它不提供数据，只用来访问内网站点。
	SiteGateway SiteKind = "gateway"
)

// SiteProfile 描述一个站点的性质与取数方式。
type SiteProfile struct {
	Kind SiteKind
	// Host 是站点主机名（便于日志与排查）。
	Host string
	// Note 说明「为什么是这个类别」，写给后来人看。
	Note string
}

// SiteProfiles 是逐站实测的站点目录。
//
// 用户原始清单的 8 个站全部收录；另加 opac（图书馆书目系统，因为它才是
// lib 站点里真正有个人数据的地方，且它不认统一认证）。
var SiteProfiles = map[string]SiteProfile{
	"jiaowu": {
		Kind: SitePersonal, Host: "jiaowu.sicau.edu.cn",
		Note: "教务：换票后三跳建会话可取 bjiben.asp（21 个学籍字段）",
	},
	"ywtb": {
		Kind: SitePersonal, Host: "ywtb.sicau.edu.cn",
		Note: "一网通办：需它自己的 Bearer，是身份证号的唯一来源",
	},
	"webvpn": {
		Kind: SiteGateway, Host: "webvpn.sicau.edu.cn",
		Note: "VPN 网关：用于访问内网站点，本身不提供个人数据",
	},
	"xsc": {
		Kind: SitePortal, Host: "xsc.sicau.edu.cn",
		Note: "党委学工部：纯资讯门户（部门概况/党建/学风/资助），无个人数据接口",
	},
	"zjc": {
		Kind: SitePortal, Host: "zjc.sicau.edu.cn",
		Note: "招生就业处：资讯门户；其下另有独立的招生/就业信息网",
	},
	"cwc": {
		Kind: SitePortal, Host: "cwc.sicau.edu.cn",
		Note: "财务处：首页提示「没有访问当前栏目的权限」，需额外授权，非统一认证范围",
	},
	"lib": {
		Kind: SitePortal, Host: "lib.sicau.edu.cn",
		Note: "图书馆门户：资讯为主；真正有个人数据的是其 opac 子系统",
	},
	"xsgz": {
		Kind: SitePersonal, Host: "xsgz.sicau.edu.cn",
		Note: "学工系统：SSO 后拿 main-app-access-token，GET /gateway/zovecenter-saip-xg-app-student/student/detail/<学号> 一次返回 13 组档案（含身份证/家庭/简历）",
	},
	"opac": {
		Kind: SiteOwnAccount, Host: "opac.sicau.edu.cn",
		Note: "图书馆书目系统：独立账号体系（username/password 表单），页内无 CAS/SSO 入口",
	},
	"ecology": {
		Kind: SitePortal, Host: "ecology.sicau.edu.cn",
		Note: "生态/学工一体化：可达（HTTPS 200），未发现统一认证取数入口",
	},
}

// User 是换票响应里携带的用户信息。
type User struct {
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

// Result 是一次换票的结果。
type Result struct {
	ST          string // 给目标站建会话用的 service ticket
	TGT         string // 响应里带回的新 tgt（可用于滚动续期）
	CallbackURL string // 目标站的回调地址（主机名即目标站）
	User        User   // 身份（换票顺带给出，不必再取一次）
}

// Exchange 用 go-admin-tgt 为指定 appid 换票。
//
// appid 可以是 SiteAppID 里的站点名，也可以是直接给出的 UUID。
// cookieHeader 为整套同域 cookie；只给 tgt 时退化为只带 tgt（实测可行）。
//
// decrypt 是换票明文的解密函数（由调用方注入 DecryptYWTB，避免 cas 包
// 依赖具体的加解密实现）。
func Exchange(ctx context.Context, doer platform.HTTPDoer, authBase, appid, tgt, cookieHeader string,
	decrypt func(requestID, data string) ([]byte, error)) (Result, error) {
	if doer == nil {
		return Result{}, fmt.Errorf("cas: Exchange 需要 HTTPDoer")
	}
	if strings.TrimSpace(tgt) == "" {
		return Result{}, fmt.Errorf("cas: 缺少 %s", goAdminTgtCookie)
	}
	if decrypt == nil {
		return Result{}, fmt.Errorf("cas: Exchange 需要解密函数")
	}
	// appid 可以在 SiteAppID 里查名字；查不到就当 UUID 直接用。
	if mapped, ok := SiteAppID[appid]; ok {
		appid = mapped
	}
	if authBase == "" {
		authBase = "https://auth.sicau.edu.cn"
	}
	cookie := strings.TrimSpace(cookieHeader)
	if cookie == "" {
		cookie = goAdminTgtCookie + "=" + tgt
	} else if !strings.Contains(cookie, goAdminTgtCookie+"=") {
		cookie += "; " + goAdminTgtCookie + "=" + tgt
	}

	u := strings.TrimRight(authBase, "/") + "/api/v1/cas/login?appid=" + appid
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120")

	resp, err := doer.Do(ctx, req)
	if err != nil {
		return Result{}, fmt.Errorf("cas: 换票请求失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Result{}, err
	}

	var env struct {
		Code      int    `json:"code"`
		Msg       string `json:"msg"`
		RequestID string `json:"requestId"`
		Data      string `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return Result{}, fmt.Errorf("cas: 换票响应解析失败：%w", err)
	}
	if env.Code != 200 || env.Data == "" {
		return Result{}, fmt.Errorf("cas: 换票失败（code=%d %s）", env.Code, env.Msg)
	}

	plain, err := decrypt(env.RequestID, env.Data)
	if err != nil {
		return Result{}, fmt.Errorf("cas: 换票明文解密失败：%w", err)
	}
	var r Result
	if err := json.Unmarshal(plain, &r); err != nil {
		return Result{}, fmt.Errorf("cas: 换票明文解析失败：%w", err)
	}
	return r, nil
}

// YWTBIdentity 是 ywtb 的完整身份（含身份证）。
type YWTBIdentity struct {
	Number    string   `json:"number"`
	Name      string   `json:"name"`
	Gender    string   `json:"gender"`
	IDCard    string   `json:"idcard"`
	Phone     string   `json:"phone"`
	Unit      string   `json:"unit"` // 学院
	Birthday  string   `json:"birthday"`
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
func FetchYWTBIdentity(ctx context.Context, doer platform.HTTPDoer, baseURL, bearer string,
	decrypt func(requestID, data string) ([]byte, error)) (YWTBIdentity, error) {
	if doer == nil {
		return YWTBIdentity{}, fmt.Errorf("cas: FetchYWTBIdentity 需要 HTTPDoer")
	}
	if strings.TrimSpace(bearer) == "" {
		return YWTBIdentity{}, fmt.Errorf("cas: 缺少 ywtb Bearer token（它不是 go-admin-tgt）")
	}
	if decrypt == nil {
		return YWTBIdentity{}, fmt.Errorf("cas: FetchYWTBIdentity 需要解密函数")
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
		return YWTBIdentity{}, fmt.Errorf("cas: ywtb 取身份请求失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return YWTBIdentity{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return YWTBIdentity{}, fmt.Errorf("cas: ywtb 返回 %d（Bearer 可能已失效）", resp.StatusCode)
	}

	var env struct {
		Code      int    `json:"code"`
		Msg       string `json:"msg"`
		RequestID string `json:"requestId"`
		Data      string `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return YWTBIdentity{}, fmt.Errorf("cas: ywtb 响应解析失败：%w", err)
	}
	if env.Code != 200 || env.Data == "" {
		return YWTBIdentity{}, fmt.Errorf("cas: ywtb 业务码 %d（%s）", env.Code, env.Msg)
	}
	plain, err := decrypt(env.RequestID, env.Data)
	if err != nil {
		return YWTBIdentity{}, fmt.Errorf("cas: ywtb 明文解密失败：%w", err)
	}
	var info YWTBIdentity
	if err := json.Unmarshal(plain, &info); err != nil {
		return YWTBIdentity{}, fmt.Errorf("cas: ywtb 明文解析失败：%w", err)
	}
	if strings.TrimSpace(info.Number) == "" {
		return YWTBIdentity{}, fmt.Errorf("cas: ywtb 明文缺少学号")
	}
	return info, nil
}
