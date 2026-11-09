package schoolauth

// 教务取数：换票 → 建教务会话 → 抓学籍页。
//
// 协议来自知识库《川农 e 认证「一次登录处处可达」靠 go-admin-tgt cookie 免密换票》：
//
//	1) 客户端交 go-admin-tgt（auth.sicau.edu.cn 域 cookie，10 天有效）
//	2) 后端 GET https://auth.sicau.edu.cn/api/v1/cas/login?appid=<教务appid>
//	   带 Cookie: go-admin-tgt=... → JSON 信封，解密得 st
//	3) 回投 https://jiaowu.sicau.edu.cn/jiaoshi/aspsso/caslogin.asp?appid=..&ticket=<st>
//	   跟随跳转建立教务会话（种 ASPSESSIONID*）
//	4) 带会话 GET https://jiaowu.sicau.edu.cn/xuesheng/dangan/banji/bjiben.asp
//
// 关于重定向与 SSRF：
//
//	闸口的默认规则是「不跟随指向真站的 3xx」，这是 SSRF 护栏。
//	但建教务会话**必须**跟随 caslogin.asp 的跳转——而该跳转发生在
//	**白名单内的教务域**（jiaowu.sicau.edu.cn），且 Location 由上游决定、
//	仍受白名单校验。这不构成「任意 URL 代理」，符合 ADR-0001 第 1 条
//	（目标集合固定白名单，调用方只传逻辑目标名）。
//
//	因此本实现**自己**跟随跳转（不走闸口的自动跟随），且每跳一步都校验
//	host 仍在白名单内；跳出白名单立即拒绝。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"nongyu-backend-plus/internal/platform"
)

// JiaowuAppID 是教务在统一身份认证平台上的应用标识。
// 实测值，来自知识库；换票时传错会拿到 INVALID_TICKET。
const JiaowuAppID = "b54a2f27-f7ba-4b91-bbf0-3b2b12244d14"

// 教务相关的固定路径与主机。
const (
	jiaowuHost         = "jiaowu.sicau.edu.cn"
	authHost           = "auth.sicau.edu.cn"
	casLoginPath       = "/jiaoshi/aspsso/caslogin.asp"
	academicPagePath   = "/xuesheng/dangan/banji/bjiben.asp"
	goAdminTgtCookie   = "go-admin-tgt"
	maxJiaowuBody      = 2 << 20 // 2 MiB
	maxJiaowuRedirects = 5       // 跟随跳转的最大跳数
)

// JiaowuOptions 是教务取数源构造参数。
type JiaowuOptions struct {
	// Doer 是出站闸口。
	Doer platform.HTTPDoer
	// AuthBaseURL 默认 https://auth.sicau.edu.cn
	AuthBaseURL string
	// JiaowuBaseURL 默认 https://jiaowu.sicau.edu.cn
	JiaowuBaseURL string
	// AllowedHosts 是允许跟随跳转的目标 host 集合；默认只有教务 host。
	// 传空表示用默认。**不要**把它配宽——那是 SSRF 口子。
	AllowedHosts []string
}

// JiaowuSource 实现 AcademicSource：取专业/班级/年级。
type JiaowuSource struct {
	doer         platform.HTTPDoer
	authBase     string
	jiaowuBase   string
	allowedHosts []string
}

// NewJiaowuSource 构造教务取数源。
func NewJiaowuSource(o JiaowuOptions) (*JiaowuSource, error) {
	if o.Doer == nil {
		return nil, fmt.Errorf("schoolauth: JiaowuSource 需要 HTTPDoer")
	}
	authBase := o.AuthBaseURL
	if authBase == "" {
		authBase = "https://" + authHost
	}
	jiaowuBase := o.JiaowuBaseURL
	if jiaowuBase == "" {
		jiaowuBase = "https://" + jiaowuHost
	}
	allowed := o.AllowedHosts
	if len(allowed) == 0 {
		allowed = []string{jiaowuHost}
	}
	return &JiaowuSource{
		doer:         o.Doer,
		authBase:     strings.TrimRight(authBase, "/"),
		jiaowuBase:   strings.TrimRight(jiaowuBase, "/"),
		allowedHosts: allowed,
	}, nil
}

// FetchAcademic 实现 AcademicSource。
//
// 凭据需要 go-admin-tgt（由客户端在 auth 域登录后交来）。
func (s *JiaowuSource) FetchAcademic(ctx context.Context, cred Credential) (RawAcademic, error) {
	tgt := cookieValue(cred, goAdminTgtCookie)
	if tgt == "" {
		return RawAcademic{}, fmt.Errorf("schoolauth: 缺少 %s（需客户端在统一身份认证登录后交来）", goAdminTgtCookie)
	}

	// 1) 换票：拿 service ticket。
	st, err := s.exchangeTicket(ctx, tgt)
	if err != nil {
		return RawAcademic{}, err
	}

	// 2) 回投建立教务会话（跟随白名单内的跳转）。
	sessionCookies, err := s.establishSession(ctx, st)
	if err != nil {
		return RawAcademic{}, err
	}

	// 3) 抓学籍页。
	body, err := s.fetchAcademicPage(ctx, sessionCookies)
	if err != nil {
		return RawAcademic{}, err
	}

	// 4) 解析（GBK 老页面）。
	acc, err := ParseAcademic(body)
	if err != nil {
		return RawAcademic{}, err
	}
	return RawAcademic{
		Major:     acc.Major,
		ClassName: acc.ClassName,
		Grade:     acc.Grade,
	}, nil
}

// exchangeTicket 用 go-admin-tgt 换一张教务的 service ticket。
func (s *JiaowuSource) exchangeTicket(ctx context.Context, tgt string) (string, error) {
	u := s.authBase + "/api/v1/cas/login?appid=" + url.QueryEscape(JiaowuAppID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("accept", "*/*")
	req.Header.Set("Cookie", goAdminTgtCookie+"="+tgt)

	resp, err := s.doer.Do(ctx, req)
	if err != nil {
		return "", fmt.Errorf("schoolauth: 换票请求失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxJiaowuBody))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("schoolauth: 换票返回 %d", resp.StatusCode)
	}

	// 换票响应与 ywtb 同构：requestId + 加密 data。
	var env ywtbEnvelope
	if err := jsonUnmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("schoolauth: 换票响应解析失败：%w", err)
	}
	if env.Code != 200 {
		return "", fmt.Errorf("schoolauth: 换票业务码 %d（%s）", env.Code, env.Msg)
	}
	plain, err := DecryptYWTB(env.RequestID, env.Data)
	if err != nil {
		return "", fmt.Errorf("schoolauth: 换票响应解密失败：%w", err)
	}
	var ticket struct {
		ST string `json:"st"`
	}
	if err := jsonUnmarshal(plain, &ticket); err != nil {
		return "", fmt.Errorf("schoolauth: 票据解析失败：%w", err)
	}
	if ticket.ST == "" {
		return "", fmt.Errorf("schoolauth: 换票未返回 service ticket")
	}
	return ticket.ST, nil
}

// establishSession 回投 ticket 建立教务会话，返回会话 cookie。
func (s *JiaowuSource) establishSession(ctx context.Context, st string) (string, error) {
	u := s.jiaowuBase + casLoginPath + "?appid=" + url.QueryEscape(JiaowuAppID) + "&ticket=" + url.QueryEscape(st)

	cookies := make([]string, 0, 4)
	current := u
	for hop := 0; hop < maxJiaowuRedirects; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("accept", "text/html,*/*")
		if len(cookies) > 0 {
			req.Header.Set("Cookie", strings.Join(cookies, "; "))
		}

		resp, err := s.doer.Do(ctx, req)
		if err != nil {
			return "", fmt.Errorf("schoolauth: 建立教务会话失败：%w", err)
		}

		// 收下会话 cookie（ASP 的 ASPSESSIONID*）。
		for _, c := range resp.Cookies() {
			cookies = append(cookies, c.Name+"="+c.Value)
		}

		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Header.Get("Location")
			_ = resp.Body.Close()
			if loc == "" {
				return "", fmt.Errorf("schoolauth: 跳转缺少 Location")
			}
			next, err := url.Parse(loc)
			if err != nil {
				return "", fmt.Errorf("schoolauth: 跳转地址非法：%w", err)
			}
			if !next.IsAbs() {
				base, _ := url.Parse(current)
				next = base.ResolveReference(next)
			}
			// 关键安全检查：只允许跳到白名单内的 host。
			if !s.hostAllowed(next.Hostname()) {
				return "", fmt.Errorf("schoolauth: 跳转目标 %s 不在白名单内，拒绝跟随", next.Hostname())
			}
			current = next.String()
			continue
		}

		// 非跳转：读回页面确认会话已建立（此时通常已是教务首页或目标页）。
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxJiaowuBody))
		_ = resp.Body.Close()
		if rerr != nil {
			return "", rerr
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("schoolauth: 教务返回 %d", resp.StatusCode)
		}
		if len(cookies) == 0 {
			return "", fmt.Errorf("schoolauth: 教务未下发会话 cookie")
		}
		// 会话建立成功；把 cookie 交给调用方去抓学籍页。
		// 若这一跳已经落在学籍页上，body 就是学籍内容，可直接用。
		_ = body
		return strings.Join(cookies, "; "), nil
	}
	return "", fmt.Errorf("schoolauth: 跳转次数超过上限")
}

// fetchAcademicPage 带着教务会话抓学籍页。
func (s *JiaowuSource) fetchAcademicPage(ctx context.Context, sessionCookies string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.jiaowuBase+academicPagePath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "text/html,*/*")
	req.Header.Set("Cookie", sessionCookies)

	resp, err := s.doer.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("schoolauth: 抓学籍页失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("schoolauth: 学籍页返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJiaowuBody))
	if err != nil {
		return nil, err
	}
	return body, nil
}

// hostAllowed 判断 host 是否在允许集合内。
func (s *JiaowuSource) hostAllowed(host string) bool {
	for _, h := range s.allowedHosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// cookieValue 从凭据里取指定 cookie 的值。
func cookieValue(cred Credential, name string) string {
	for _, c := range cred.Cookies {
		if c.Name == name {
			return c.Value
		}
	}
	// 从原始 Cookie 头里找。
	for _, part := range strings.Split(cred.RawCookie, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 && strings.TrimSpace(kv[0]) == name {
			return kv[1]
		}
	}
	return ""
}

// jsonUnmarshal 是 json.Unmarshal 的薄封装（便于统一错误包装）。
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
