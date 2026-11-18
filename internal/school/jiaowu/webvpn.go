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

// webvpnExpiredSentinel 是「票已被网关判失效」的存储标记。
//
// /fetch 判定票失效时不直接删记录，而是写入这个哨兵：票值本身是可见 ASCII，
// 哨兵用 NUL 开头天然区分；这样 /fetch 再次收到请求时能直接报 expired，
// 不再拿死票打学校站。客户端重新登录交票会覆盖它，超时后自然回落。
const webvpnExpiredSentinel = "\x00expired"
const webvpnExpiredMarkTTL = 30 * time.Minute

// webvpnCookiesForClient 从保管库取出网关票，包装成 cache_miss 要下发的 cookie。
//
// 模型（2026-11-18 拍板）：客户端什么都没有、全在后端取。它拿 cache_miss 的
// address + cookies 交给后端代取链路，自己不碰网关地址、不碰任何 cookie 罐。
// 所以 vpn 形态必须把保管库里的 wengine_vpn_ticket 放进 cookies。
//
// 失效哨兵/本地过期不下发（返回 nil）：拿不到票就是拿不到票，弹窗由客户端
// 自己判 302。后端零出站，本来看不出网关侧是否仍认这张票，不造状态字段。
func (s *Service) webvpnCookiesForClient(ctx context.Context, userID string) []IssuedCookie {
	ticket, err := s.webvpnTicketOf(ctx, userID)
	if err != nil {
		return nil
	}
	return []IssuedCookie{{Name: gatewayTicketCookieName, Value: ticket}}
}

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
	ticket, exp, err := s.webvpnTickets.Get(ctx, userID)
	if err != nil {
		return WebVPNTicketStatus{}
	}
	// 被判死的哨兵不算「有票」——查询端点也不该对客户端谎称票还在。
	if isExpiredSentinel(ticket) {
		return WebVPNTicketStatus{}
	}
	return WebVPNTicketStatus{Present: true, ExpiresAt: exp}
}

// markWebVPNTicketExpired 记录「这张票已被网关判死」，供后续 cache_miss 复用。
//
// 不直接删记录：删了就只能报 missing，客户端会把「登录过、票死了」误当成
// 「从没登录过」，弹窗文案和引导动作都会错。
func (s *Service) markWebVPNTicketExpired(ctx context.Context, userID string) {
	if s.webvpnTickets == nil || userID == "" {
		return
	}
	_ = s.webvpnTickets.Put(ctx, userID, webvpnExpiredSentinel,
		s.clock.Now().Add(webvpnExpiredMarkTTL))
}

func isExpiredSentinel(ticket string) bool {
	return ticket == webvpnExpiredSentinel
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
	ticket, exp, err := s.webvpnTickets.Get(ctx, userID)
	if err != nil {
		return "", ErrWebVPNTicketMissing
	}
	if isExpiredSentinel(ticket) {
		// 上次代取已被网关判死，别再拿去打学校站；直接让客户端重新登录。
		return "", ErrWebVPNTicketExpired
	}
	if strings.TrimSpace(ticket) == "" {
		return "", ErrWebVPNTicketMissing
	}
	// 本地 TTL 兜底：过期即视为失效（不打学校站验证）。
	if !exp.IsZero() && !s.clock.Now().Before(exp) {
		_ = s.ClearWebVPNTicket(ctx, userID)
		return "", ErrWebVPNTicketExpired
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

	// 先在网关服务端建立「该票已登录教务」的状态（用保管 tgt 换 st 回投 caslogin）。
	// 这一步是幂等的：重复做只是刷新网关侧状态，无害。
	// 教务会话绑在网关票上，客户端零 cookie；建完再打需登录页才不会返回登录壳。
	if err := s.establishWebVPNSession(ctx, userID, ticket); err != nil {
		s.audit(ctx, userID, "fetch_webvpn", "cas_failed", string(school.TargetWebvpn))
		return FetchResult{}, err
	}

	result, err := s.fetchEncoded(ctx, recipe.Address, ticket)
	if err != nil {
		s.audit(ctx, userID, "fetch_webvpn", "outbound_error", string(school.TargetWebvpn))
		return FetchResult{}, err
	}

	// 票失效：302 回网关 /login。
	if isRedirectStatus(result.Status) && isGatewayLoginRedirect(result.Headers.Get("Location")) {
		s.markWebVPNTicketExpired(ctx, userID)
		s.audit(ctx, userID, "fetch_webvpn", "ticket_expired", string(school.TargetWebvpn))
		return FetchResult{}, ErrWebVPNTicketExpired
	}

	// 教务把「未登录」渲染成 HTTP 200 + 1856 字节 SPA 登录壳，不是 302。
	// 建会话后仍出现这个壳，说明会话没建上（st 被拒等），必须明确报错，
	// 绝不能把它当成功页面透给客户端去解析。
	if isAuthLoginShell(result.Body) {
		s.audit(ctx, userID, "fetch_webvpn", "jiaowu_login_required", string(school.TargetWebvpn))
		return FetchResult{}, fmt.Errorf("jiaowu: 教务未建立登录会话（拿到统一身份认证登录页）")
	}

	s.audit(ctx, userID, "fetch_webvpn", "ok", string(school.TargetWebvpn))
	return result, nil
}

// fetchEncoded 用网关票打一个编码地址，返回原始响应（不跟随重定向）。
func (s *Service) fetchEncoded(ctx context.Context, address, ticket string) (FetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return FetchResult{}, err
	}
	// 只带网关票。教务会话在网关服务端，这里贴其它 cookie 反而干扰。
	req.Header.Set("Cookie", gatewayTicketCookieName+"="+ticket)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Referer", "https://webvpn.sicau.edu.cn/")

	resp, err := s.doer.Do(ctx, req)
	if err != nil {
		return FetchResult{}, mapOutboundError(err)
	}
	if resp == nil {
		return FetchResult{}, errors.New("jiaowu: nil response from outbound doer")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		return FetchResult{}, err
	}
	return FetchResult{Status: resp.StatusCode, Body: body, Headers: resp.Header.Clone()}, nil
}

// isAuthLoginShell 识别「教务未登录」被渲染成 200 + SPA 登录壳的情况。
//
// 实测：未建会话时 /fetch 拿到 1856 字节、title 含「川农e认证」的壳。
// 按内容特征判，不靠状态码（它是 200）也不靠长度阈值 alone（避免误伤小页面）。
func isAuthLoginShell(body []byte) bool {
	return strings.Contains(string(body), "川农e认证") ||
		strings.Contains(string(body), "统一身份认证")
}

// maxFetchBytes 是代取响应体的上限（教务最大页实测约 65KB，留足余量）。
const maxFetchBytes = 8 << 20

// maxJiaowuCASRedirects 是跟随 caslogin 跳转的最大跳数。
// 真实链路 3 跳（caslogin.asp → check_cas.asp?sign=.. → note.asp → index1.asp），留 2 跳余量。
const maxJiaowuCASRedirects = 5

// jiaowuCASLoginPath 是教务 CAS 回投路径（编码前的直连路径）。
const jiaowuCASLoginPath = "/jiaoshi/aspsso/caslogin.asp"

// establishWebVPNSession 在 WebVPN 网关服务端建立「该网关票已登录教务」的状态。
//
// 实测（2026-11-18）：经编码域访问教务时，教务会话**不在客户端 cookie 罐里**，
// 也不在后端——它绑在网关服务端、按 wengine_vpn_ticket 索引。三跳跑完累计的
// Set-Cookie 为空是正常的，不要按「没拿到 cookie」报错退出。
//
// 做法是拿 st 回投**编码后的** caslogin.asp，手动跟随完整跳转链，每跳都带网关票。
// 之后同一张网关票打课表/成绩等编码页直接通（实测 32848 字节真实课表）。
//
// 与 schoolauth.establishSession 的区别：那条走直连教务域（校外不可达）且要
// 累计 ASPSESSIONID；这条走编码域且不需要任何 cookie。两者不可复用。
func (s *Service) establishWebVPNSession(ctx context.Context, userID, ticket string) error {
	if s.jiaowuST == nil {
		return fmt.Errorf("jiaowu: 未接入教务 st 换票能力，无法建立网关侧教务会话")
	}
	st, err := s.jiaowuST.IssueJiaowuST(ctx, userID)
	if err != nil {
		// 错误文本可能带凭据前缀：只记审计结果，绝不把 err 写进响应/日志。
		s.audit(ctx, userID, "webvpn_cas", "st_failed", string(school.TargetWebvpn))
		return fmt.Errorf("jiaowu: 换教务 st 失败")
	}

	tgt, err := school.Resolve(school.TargetJiaowu)
	if err != nil {
		return err
	}
	// 先拼完整直连 URL 再 Encode（Encode 对无 path 地址会补 "/"，直接接 path 会双斜杠）。
	directCAS := tgt.Direct.String() + jiaowuCASLoginPath +
		"?appid=" + url.QueryEscape(schoolauthJiaowuAppID) + "&ticket=" + url.QueryEscape(st)
	encoded, err := school.Encode(directCAS)
	if err != nil {
		return err
	}

	current := encoded
	for hop := 0; hop < maxJiaowuCASRedirects; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Cookie", gatewayTicketCookieName+"="+ticket)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
		req.Header.Set("Referer", "https://webvpn.sicau.edu.cn/")

		resp, err := s.doer.Do(ctx, req)
		if err != nil {
			return mapOutboundError(err)
		}
		if resp == nil {
			return errors.New("jiaowu: nil response from outbound doer")
		}
		_ = resp.Body.Close()

		if !isRedirectStatus(resp.StatusCode) {
			// 落在 index1.asp（200）即建立完成。
			if resp.StatusCode == http.StatusOK {
				s.audit(ctx, userID, "webvpn_cas", "ok", string(school.TargetWebvpn))
				return nil
			}
			return fmt.Errorf("jiaowu: 教务 CAS 回投返回 %d", resp.StatusCode)
		}

		loc := resp.Header.Get("Location")
		if loc == "" {
			return fmt.Errorf("jiaowu: CAS 跳转缺少 Location")
		}
		next, err := url.Parse(loc)
		if err != nil {
			return fmt.Errorf("jiaowu: CAS 跳转地址非法")
		}
		base, _ := url.Parse(current)
		current = base.ResolveReference(next).String()
		// SSRF 护栏：只允许留在网关域。
		if u, e := url.Parse(current); e != nil || !strings.EqualFold(u.Hostname(), "webvpn.sicau.edu.cn") {
			s.audit(ctx, userID, "webvpn_cas", "redirect_denied", string(school.TargetWebvpn))
			return fmt.Errorf("jiaowu: CAS 跳转目标不在网关域，拒绝跟随")
		}
	}
	return fmt.Errorf("jiaowu: CAS 跳转次数超过上限")
}

// schoolauthJiaowuAppID 是教务在统一身份认证平台上的 appid。
// 与 schoolauth.JiaowuAppID 同值；本包不 import schoolauth，各写一份避免跨包耦合。
const schoolauthJiaowuAppID = "b54a2f27-f7ba-4b91-bbf0-3b2b12244d14"

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
