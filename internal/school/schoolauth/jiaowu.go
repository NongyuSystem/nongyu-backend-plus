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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
	// tgt 是可选的 tgt 保管器；接上后优先用它（不必每次让客户端重交凭据）。
	tgt *TGTManager
}

// WithTGT 接上 tgt 保管器：优先用服务端保管的 tgt，客户端凭据只在缺省时作为兜底。
func (s *JiaowuSource) WithTGT(m *TGTManager) *JiaowuSource {
	s.tgt = m
	return s
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

// FetchAcademicWithTicket 在**已经拿到 service ticket** 的情况下取学籍。
//
// 用于：调用方已通过别的方式换到票（例如已缓存的 tgt 换了一次），
// 或需要复用同一张票做多件事时，避免重复换票。
func (s *JiaowuSource) FetchAcademicWithTicket(ctx context.Context, st string) (Academic, error) {
	if st == "" {
		return Academic{}, fmt.Errorf("schoolauth: 需要 service ticket")
	}
	sessionCookies, err := s.establishSession(ctx, st)
	if err != nil {
		return Academic{}, err
	}
	body, err := s.fetchAcademicPage(ctx, sessionCookies)
	if err != nil {
		return Academic{}, err
	}
	return ParseAcademic(body)
}

// FetchAcademic 实现 AcademicSource。
//
// 凭据需要 go-admin-tgt（由客户端在 auth 域登录后交来）。
func (s *JiaowuSource) FetchAcademic(ctx context.Context, cred Credential) (RawAcademic, error) {
	// 取一个可用的 tgt：
	//   - 接上 TGTManager 时：优先用服务端保管的（未临期直接用，临期自动换票续期）；
	//     客户端交来的只作为兜底。
	//   - 没接 TGTManager 时：必须由客户端每次提供。
	tgt := cookieValue(cred, goAdminTgtCookie)
	if s.tgt != nil {
		userID := ""
		if cred.UserID != 0 {
			userID = strconv.FormatInt(cred.UserID, 10)
		}
		fresh, err := s.tgt.EnsureFresh(ctx, userID, cred)
		if err != nil {
			return RawAcademic{}, err
		}
		tgt = fresh
	}
	if tgt == "" {
		return RawAcademic{}, fmt.Errorf("schoolauth: 缺少 %s（需客户端在统一身份认证登录后交来）", goAdminTgtCookie)
	}

	// 1) 换票：拿 service ticket（同时带回新 tgt）。
	//
	// 注意：换票**必须带客户端交来的整套 cookie**，不能只带 go-admin-tgt。
	// 实测：同一枚 tgt，只带它自己时换票返回 code=500「未登录」；
	// 带上同域的 laravel_session 等配套 cookie 才返回 200。
	// 整套 cookie（含 tgt 的最新值）：换票必须带全套，不能只带 tgt。
	st, refreshedTGT, err := s.exchangeTicket(ctx, tgt, cred.FullCookieHeader(tgt))
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
		Major:        acc.Major,
		ClassName:    acc.ClassName,
		Grade:        acc.Grade,
		RefreshedTGT: refreshedTGT,
	}, nil
}

// TicketResult 是一次换票的产物。
type TicketResult struct {
	// ST 是给教务的 service ticket。
	ST string
	// TGT 是响应里带回的**新** go-admin-tgt（实测存在），可用于滚动续期。
	TGT string
}

// 换票失败的语义分类（供上层区分「用户需重登」与「基础设施故障」）。
var (
	// ErrTicketRejected 表示学校侧**明确拒绝**了这组凭据（业务码非 200）。
	// 这是用户需要重新登录的场景。
	ErrTicketRejected = errors.New("schoolauth: 学校侧拒绝凭据")
	// ErrTicketInfra 表示换票过程的基础设施故障（网络不可达、响应非法、解密失败等）。
	// 这不是用户的错，重登也无用，应排查网络/协议。
	ErrTicketInfra = errors.New("schoolauth: 换票基础设施故障")
)

// exchangeTicketOnly 只换票，不建会话、不抓学籍。
//
// cookieHeader 必须是**整套**同域 cookie（实测单枚 tgt 会返回 code=500 未登录）。
func (s *JiaowuSource) exchangeTicketOnly(ctx context.Context, tgt, cookieHeader string) (TicketResult, error) {
	st, newTGT, err := s.exchangeTicket(ctx, tgt, cookieHeader)
	if err != nil {
		return TicketResult{}, err
	}
	return TicketResult{ST: st, TGT: newTGT}, nil
}

// casUser 是换票响应明文里携带的用户信息（实测 2026-11-09 确认存在）。
//
// 一次换票请求同时给两样东西：st（给教务建会话）+ user（身份），
// 所以「取身份」不必再调 ywtb getUserInfo（那条路要求 Bearer，#28）。
type casUser struct {
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

// exchangeTicket 用客户端的会话 cookie 换一张教务的 service ticket。
//
// cookieHeader 为整套同域 cookie；若只给了 tgt，退化为只带 tgt。
// **实测（2026-11-09）只带 go-admin-tgt 也能换票成功**（code=200）——
// 早前「必须整套 cookie」的结论对当前平台已不成立，不再作为前置要求。
func (s *JiaowuSource) exchangeTicket(ctx context.Context, tgt, cookieHeader string) (string, string, error) {
	st, newTGT, _, err := s.exchangeTicketFull(ctx, tgt, cookieHeader)
	return st, newTGT, err
}

// exchangeTicketFull 换票并**一并解出用户信息**（#28）。
//
// 返回值：st、新 tgt、换票响应里的 user。
// 抽出这一层是为了让「取身份」复用同一次请求——换票响应本就带 user，
// 没必要为了拿学号/姓名再走一趟要求 Bearer 的 getUserInfo。
func (s *JiaowuSource) exchangeTicketFull(ctx context.Context, tgt, cookieHeader string) (string, string, casUser, error) {
	u := s.authBase + "/api/v1/cas/login?appid=" + url.QueryEscape(JiaowuAppID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", casUser{}, err
	}
	req.Header.Set("accept", "*/*")
	// 整套 cookie 原样透传；若调用方只给了 tgt（无其它 cookie），退化为只带 tgt。
	if strings.TrimSpace(cookieHeader) != "" {
		req.Header.Set("Cookie", cookieHeader)
	} else {
		req.Header.Set("Cookie", goAdminTgtCookie+"="+tgt)
	}

	resp, err := s.doer.Do(ctx, req)
	if err != nil {
		return "", "", casUser{}, fmt.Errorf("%w：换票请求失败：%v", ErrTicketInfra, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxJiaowuBody))
	if err != nil {
		return "", "", casUser{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", casUser{}, fmt.Errorf("%w：换票返回 %d", ErrTicketInfra, resp.StatusCode)
	}

	// 换票响应与 ywtb 同构：requestId + 加密 data。
	var env ywtbEnvelope
	if err := jsonUnmarshal(raw, &env); err != nil {
		return "", "", casUser{}, fmt.Errorf("%w：换票响应解析失败：%v", ErrTicketInfra, err)
	}
	if env.Code != 200 {
		return "", "", casUser{}, fmt.Errorf("%w：换票业务码 %d（%s）", ErrTicketRejected, env.Code, env.Msg)
	}
	plain, err := DecryptYWTB(env.RequestID, env.Data)
	if err != nil {
		return "", "", casUser{}, fmt.Errorf("%w：换票响应解密失败：%v", ErrTicketInfra, err)
	}
	var ticket struct {
		ST   string  `json:"st"`
		TGT  string  `json:"tgt"`
		User casUser `json:"user"`
	}
	if err := jsonUnmarshal(plain, &ticket); err != nil {
		return "", "", casUser{}, fmt.Errorf("%w：票据解析失败：%v", ErrTicketInfra, err)
	}
	if ticket.ST == "" {
		return "", "", casUser{}, fmt.Errorf("%w：换票未返回 service ticket", ErrTicketInfra)
	}
	return ticket.ST, ticket.TGT, ticket.User, nil
}

// FetchPage 用 tgt 取**任意**教务页面，返回原始 body。
//
// 用途：学院/身份证/家庭地址/性别/考号等字段不在换票响应里，只在教务档案页；
// 各字段的确切页面需要逐个探明，所以先提供一个通用取页能力，探明后直接接解析。
//
// path 必须是**以 / 开头的站内路径**。拒绝绝对 URL 与协议相对 URL（防 SSRF）：
// 只允许在本源下取页，不允许借 tgt 去请求别的主机。
func (s *JiaowuSource) FetchPage(ctx context.Context, cred Credential, path string) ([]byte, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, fmt.Errorf("schoolauth: 页面路径必须是以 / 开头的站内路径，收到 %q", path)
	}
	// 路径里不得藏协议（防 "https://..." 绕过上面的前缀判断之外的情况）
	if strings.Contains(path, "://") {
		return nil, fmt.Errorf("schoolauth: 页面路径不得含协议，收到 %q", path)
	}

	tgt := cookieValue(cred, goAdminTgtCookie)
	if s.tgt != nil {
		userID := ""
		if cred.UserID != 0 {
			userID = strconv.FormatInt(cred.UserID, 10)
		}
		fresh, err := s.tgt.EnsureFresh(ctx, userID, cred)
		if err != nil {
			return nil, err
		}
		tgt = fresh
	}
	if strings.TrimSpace(tgt) == "" {
		return nil, fmt.Errorf("schoolauth: 缺少 %s（需客户端在统一身份认证登录后交来）", goAdminTgtCookie)
	}

	st, _, err := s.exchangeTicket(ctx, tgt, cred.FullCookieHeader(tgt))
	if err != nil {
		return nil, err
	}
	sessionCookies, err := s.establishSession(ctx, st)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.jiaowuBase+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "text/html,*/*")
	req.Header.Set("Cookie", sessionCookies)

	resp, err := s.doer.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("schoolauth: 取页面失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("schoolauth: 页面 %s 返回 %d", path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxJiaowuBody))
}

// ExchangeForIdentity 用 tgt 换票并返回身份（实现 TicketExchanger，#28）。
//
// 换票响应本就带 user，所以这是「取身份」最短的一条路：
// 一次出站请求同时拿到身份，且**不需要 Bearer**（真实客户端没有）。
func (s *JiaowuSource) ExchangeForIdentity(ctx context.Context, cred Credential) (TicketIdentity, error) {
	tgt := cookieValue(cred, goAdminTgtCookie)
	if s.tgt != nil {
		userID := ""
		if cred.UserID != 0 {
			userID = strconv.FormatInt(cred.UserID, 10)
		}
		fresh, err := s.tgt.EnsureFresh(ctx, userID, cred)
		if err != nil {
			return TicketIdentity{}, err
		}
		tgt = fresh
	}
	if strings.TrimSpace(tgt) == "" {
		return TicketIdentity{}, fmt.Errorf("schoolauth: 缺少 %s", goAdminTgtCookie)
	}

	_, _, user, err := s.exchangeTicketFull(ctx, tgt, cred.CookieHeader())
	if err != nil {
		return TicketIdentity{}, err
	}
	if strings.TrimSpace(user.Number) == "" {
		return TicketIdentity{}, fmt.Errorf("schoolauth: 换票响应未返回学号")
	}
	ti := TicketIdentity{StudentNo: user.Number, Name: user.Name, Phone: user.Phone}
	if len(user.Group) > 0 {
		ti.Category = user.Group[0].Alias
	}
	return ti, nil
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

		// 非跳转：这已经是最终页（真实链路 3 跳后落在 index1.asp）。
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxJiaowuBody))
		_ = resp.Body.Close()
		if rerr != nil {
			return "", rerr
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("schoolauth: 教务返回 %d", resp.StatusCode)
		}
		// 注意：会话 cookie 可能在**任意一跳**的 302 上下发（实测在中间跳），
		// 所以这里判断的是「整个跟随过程中累计收到的 cookie」，
		// 不能只看最终这一页有没有 Set-Cookie。
		if len(cookies) == 0 {
			return "", fmt.Errorf("schoolauth: 教务未下发会话 cookie")
		}
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
