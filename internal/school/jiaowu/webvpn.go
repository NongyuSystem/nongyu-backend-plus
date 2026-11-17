package jiaowu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nongyu-backend-plus/internal/school"
)

// WebVPN 网关票（wengine_vpn_ticket）的保管与判定。
//
// 为什么票必须由客户端交上来：这张票只在网关**服务端已登记该会话为已登录**时有效，
// 而那种会话只有客户端真实登录网关（跑完 /wui/ 的 SPA）才会产生。后端用 tgt 走
// 门户 appid 换票再回投，只能拿到 16 位游客票，打编码地址一律 302。详见
// internal/school/schoolauth/jiaowu.go 里 IssueGatewayCookies 删除处的说明。
//
// 因此本子域只做三件事：收票 → 加密保管 → 用票代取。绝不自造票。

// ErrWebVPNTicketMissing 表示该用户没有交过网关票（或已过期被清）。
var ErrWebVPNTicketMissing = errors.New("jiaowu: 该用户没有可用的 WebVPN 网关票")

// ErrWebVPNTicketExpired 表示网关拒绝了这张票（302 回 /login），需要客户端重新登录。
var ErrWebVPNTicketExpired = errors.New("jiaowu: WebVPN 网关票已失效，需要客户端重新登录网关")

// webvpnTicketTTL 是本地保管窗口的**上限**。
//
// 网关票是会话级 Cookie、无 Max-Age，学校未公布空闲超时——所以我们不知道它到底
// 能活多久。这里取一个保守上限（12 小时），到期即清，强迫客户端重新走一次网关登录；
// 这比「永久保管一张可能早已失效的票」更安全，也避免拿死票反复打学校站。
// 真正的失效信号仍是网关返回 302——本 TTL 只是兜底。
const webvpnTicketTTL = 12 * time.Hour

// WebVPNTicketStore 保管客户端交来的网关票（加密后按 userID 存）。
type WebVPNTicketStore interface {
	Put(ctx context.Context, userID string, ticket string, expiresAt time.Time) error
	Get(ctx context.Context, userID string) (ticket string, expiresAt time.Time, err error)
	Delete(ctx context.Context, userID string) error
}

// SubmitWebVPNTicket 接收客户端登录网关后拿到的票。
//
// 只校验形态（非空、长度合理、无空白控制字符），**不校验有效性**——
// 有效性只有网关知道，任何本地判断都会误判（有效票与游客票同为 16 位十六进制）。
// 真正的校验发生在第一次代取时：302 回 /login 即判定失效并清票。
func (s *Service) SubmitWebVPNTicket(ctx context.Context, userID, ticket string) (time.Time, error) {
	ticket = strings.TrimSpace(ticket)
	if userID == "" {
		return time.Time{}, &school.Error{Code: school.CodeSessionExpired}
	}
	if !LooksLikeWebVPNTicket(ticket) {
		s.audit(ctx, userID, "webvpn_ticket", "shape_rejected", string(school.TargetWebvpn))
		return time.Time{}, fmt.Errorf("jiaowu: 网关票形态不合法（应为 8..512 位可见 ASCII，且不含空白）")
	}
	if s.webvpnTickets == nil {
		return time.Time{}, ErrCacheUnavailable
	}
	expiresAt := s.clock.Now().Add(webvpnTicketTTL)
	if err := s.webvpnTickets.Put(ctx, userID, ticket, expiresAt); err != nil {
		return time.Time{}, err
	}
	// 审计只记结果，绝不记票值。
	s.audit(ctx, userID, "webvpn_ticket", "stored", string(school.TargetWebvpn))
	return expiresAt, nil
}

// WebVPNTicketStatus 是「这张票还在不在」的只读查询，不下发票值。
type WebVPNTicketStatus struct {
	Present   bool      `json:"present"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
}

// WebVPNTicket 查询当前用户是否有可用的网关票。永不回显票值。
func (s *Service) WebVPNTicket(ctx context.Context, userID string) WebVPNTicketStatus {
	if s.webvpnTickets == nil || userID == "" {
		return WebVPNTicketStatus{}
	}
	_, exp, err := s.webvpnTickets.Get(ctx, userID)
	if err != nil {
		return WebVPNTicketStatus{}
	}
	return WebVPNTicketStatus{Present: true, ExpiresAt: exp}
}

// ClearWebVPNTicket 删除用户保管的网关票（登出、或判定失效时调用）。
func (s *Service) ClearWebVPNTicket(ctx context.Context, userID string) error {
	if s.webvpnTickets == nil || userID == "" {
		return nil
	}
	return s.webvpnTickets.Delete(ctx, userID)
}

// LooksLikeWebVPNTicket 做形态校验。
//
// 判据刻意宽松：网关票的具体格式随版本变化（当前是 16 位十六进制），
// 这里只排除明显不对的东西（空串、含空白/控制字符、超长、像 HTML）。
// **不用长度判断有效性**——有效票与游客票同长。
func LooksLikeWebVPNTicket(t string) bool {
	if len(t) < 8 || len(t) > 512 {
		return false
	}
	for _, r := range t {
		// 只允许可见 ASCII（0x21..0x7e），排除空格与控制字符。
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	// 出现尖括号基本是把 HTML 片段当票了（实测过这种坑：302 跳转页被当票存下）。
	if strings.ContainsAny(t, "<>\"'") {
		return false
	}
	return true
}

// webvpnTicketOf 取出票，供代取使用。缺失时返回 ErrWebVPNTicketMissing。
func (s *Service) webvpnTicketOf(ctx context.Context, userID string) (string, error) {
	if s.webvpnTickets == nil {
		return "", ErrCacheUnavailable
	}
	ticket, _, err := s.webvpnTickets.Get(ctx, userID)
	if err != nil {
		return "", ErrWebVPNTicketMissing
	}
	if strings.TrimSpace(ticket) == "" {
		return "", ErrWebVPNTicketMissing
	}
	return ticket, nil
}

// FetchViaWebVPN 用客户端交来的网关票，经编码地址代取教务页面。
//
// 这是校外访问的服务端实现：只有**编码地址 + 网关票**两样东西，不需要 tgt、
// 不需要教务 st、也不碰 auth 域。网关按票在服务端补出上游 ASPSESSIONID。
//
// 三个失败分支必须可区分（客户端动作完全不同）：
//   - ErrWebVPNTicketMissing：没交过票 → 引导客户端去登录网关并提交；
//   - ErrWebVPNTicketExpired：网关 302 回 /login → 客户端要**重新登录网关**；
//   - 其它错误：网络/内部问题，可重试。
//
// 失效时顺手清掉这张死票，避免下次继续拿它打学校站。
func (s *Service) FetchViaWebVPN(ctx context.Context, userID string, dt DataType) (FetchResult, error) {
	ticket, err := s.webvpnTicketOf(ctx, userID)
	if err != nil {
		s.audit(ctx, userID, "fetch_webvpn", "ticket_missing", string(school.TargetWebvpn))
		return FetchResult{}, err
	}

	recipe := recipeOf(dt, AccessVPN)
	if recipe.Address == "" {
		// 该类型需要用户输入（教室/教师），无法由后端独立拼出地址。
		s.audit(ctx, userID, "fetch_webvpn", "needs_input", string(school.TargetWebvpn))
		return FetchResult{}, fmt.Errorf("jiaowu: 该类型需要用户输入（%s），请用 basePath 自行拼装后直连", recipe.Needs)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, recipe.Address, nil)
	if err != nil {
		return FetchResult{}, err
	}
	// 只带网关票。上游的 ASPSESSIONID 由网关自己在服务端补，这里贴反而干扰。
	req.Header.Set("Cookie", gatewayTicketCookieName+"="+ticket)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Referer", "https://webvpn.sicau.edu.cn/")

	resp, err := s.doer.Do(ctx, req)
	if err != nil {
		s.audit(ctx, userID, "fetch_webvpn", "outbound_error", string(school.TargetWebvpn))
		return FetchResult{}, mapOutboundError(err)
	}
	if resp == nil {
		s.audit(ctx, userID, "fetch_webvpn", "nil_response", string(school.TargetWebvpn))
		return FetchResult{}, errors.New("jiaowu: nil response from outbound doer")
	}
	defer resp.Body.Close()

	// 302 回网关登录页 = 票失效。这是唯一可靠的失效信号（票长/内容都不可信）。
	if isRedirectStatus(resp.StatusCode) {
		if isGatewayLoginRedirect(resp.Header.Get("Location")) {
			_ = s.ClearWebVPNTicket(ctx, userID)
			s.audit(ctx, userID, "fetch_webvpn", "ticket_expired", string(school.TargetWebvpn))
			return FetchResult{}, ErrWebVPNTicketExpired
		}
		// 其它 3xx（例如教务内部的跳转闸门）原样回给调用方判断，不当作票失效。
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		s.audit(ctx, userID, "fetch_webvpn", "read_error", string(school.TargetWebvpn))
		return FetchResult{}, err
	}
	s.audit(ctx, userID, "fetch_webvpn", "ok", string(school.TargetWebvpn))
	return FetchResult{Status: resp.StatusCode, Body: body, Headers: resp.Header.Clone()}, nil
}

// maxFetchBytes 是代取响应体的上限（教务最大页实测约 65KB，留足余量）。
const maxFetchBytes = 8 << 20

// gatewayTicketCookieName 是网关会话票的 cookie 名。
// 与 schoolauth 包里的同名常量保持一致；本包不 import 它，各写一份避免跨包耦到实现细节。
const gatewayTicketCookieName = "wengine_vpn_ticketwebvpn_sicau_edu_cn"

// isGatewayLoginRedirect 判断 302 是不是「回网关登录页」——那是票失效的唯一可靠信号。
//
// 判定依据是 Location 的 host 为网关域且路径含 /login。实测失效时 Location 形如
// `https://webvpn.sicau.edu.cn/login` 或带 `/wui/#/?...service=.../login?cas_login=true`。
// 教务内部的跳转闸门（如 /jiaoshi/bangong/index.asp）不算票失效，不能误清票。
func isGatewayLoginRedirect(location string) bool {
	if strings.TrimSpace(location) == "" {
		return false
	}
	u, err := url.Parse(location)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		// 相对 Location：只有以 /login 开头才算（网关根域下的登录路径）。
		return strings.HasPrefix(u.Path, "/login")
	}
	if !strings.EqualFold(host, "webvpn.sicau.edu.cn") {
		return false
	}
	// 网关域下的 /login，或 /wui/ 里带 login 的 service 参数。
	if strings.HasPrefix(u.Path, "/login") {
		return true
	}
	return strings.Contains(location, "login?cas_login")
}
