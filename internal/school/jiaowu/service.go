package jiaowu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/platform/mail"
	"nongyu-backend-plus/internal/school"
)

// Service 是教务子域的服务。
//
// 职责现状（2026-11-17）：
//   - Address：只返回经网关编码的地址，零出站；
//   - Fetch：经出站闸口代取页面（逃生口，非主路径）；
//   - Captcha：取验证码图；
//   - Login：**已停用**（不再落库，见该方法注释），真实登录走身份域 /identity/login；
//   - RenewFor：把续期委托给 TGTRefresher（真正的 tgt 续期）；
//   - CacheGet/CacheSubmit/CacheInvalidate：回传缓存（见 cache.go）。
//
// 进程无状态：Service 没有任何可变字段，票据与上报状态全在 platform 外置能力里。
type Service struct {
	doer         platform.HTTPDoer
	tickets      platform.TicketStore
	cipher       platform.Cipher
	clock        platform.Clock
	logger       *slog.Logger
	accelEnabled bool
	scheduler    *RenewScheduler
	// audit_store 是可选的审计持久化通道；为 nil 时只写 slog。
	audit_store mail.AuditStore
	// cache 是回传数据的缓存层，可选依赖：为 nil 时缓存端点回 503。
	// 由 BindCache 注入，只依赖 CacheStore 接口，不绑 *platform.RedisCache。
	cache CacheStore
	// schema 是回传体的形状校验器（#41），由 BindSchemaValidator 注入。
	// 为 nil 时缓存读写直接报错——没有校验器就绝不能放数据进缓存。
	schema *SchemaValidator
	// sessions 在 cache_miss 时用已保管的 tgt 建教务会话，把 ASPSESSIONID 交给本人。
	// 为 nil 时 miss 仍成功，只是 cookies 为空、session=unavailable。
	sessions SessionIssuer
	// tgtRefresher 让续期回调走真正的 tgt 续期，而不是自己拼假请求。
	tgtRefresher TGTRefresher
	// webvpnTickets 保管客户端交来的 wengine_vpn_ticket（校外代取用）。
	// 为 nil 时提交/查询端点回 503，校外代取不可用。
	webvpnTickets WebVPNTicketStore
	// jiaowuST 用保管 tgt 换教务 st，供 /fetch 建立网关侧教务会话。
	// 为 nil 时 /fetch 只带网关票，需登录页会拿到登录壳（不静默成功，见 FetchViaWebVPN）。
	jiaowuST JiaowuSTIssuer
	// fetchAccounts 是公共类（全站共有页）的取数账号池，由管理员维护。
	// 为 nil 时公共类 cache_miss 回 fetch_account_unavailable，绝不回退到学生自己
	// 的凭据（那会让公共数据按人分叉）。
	fetchAccounts FetchAccountPool
	// fetchST 用取数账号的整套 cookie 换 st（公共类建网关侧会话用）。
	fetchST FetchAccountSTIssuer
	// semesterEnds 是稳定类缓存的学期末切换日（MM-DD），由 BindSemesterEndDates 注入。
	// 为 nil 时稳定类不套用学期末失效，仍按 dataTypeSpec TTL（未绑定配置时的兼容行为）。
	semesterEnds []semesterEnd
}

// New 构造 Service（#13 起保持的签名，装配层 app.New 一行无需改动）。
// 内部用安全的默认内存实现补齐全套能力；需要精确控制（测试/后续真实实现）时用 NewWithAuth。
func New(doer platform.HTTPDoer) *Service {
	return NewWithAuth(
		doer,
		platform.NewMockTicketStore(),
		platform.NewMockCipher(""),
		platform.NewRealClock(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

// NewWithAuth 用注入的全套外部能力构造 Service，供装配层与测试精确控制。
// 业务子域仍只依赖 platform 接口，不 import app/server/config。
func NewWithAuth(doer platform.HTTPDoer, tickets platform.TicketStore, cipher platform.Cipher, clock platform.Clock, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{
		doer:         doer, // platform 注入的守卫实例（全系统唯一强制点）
		tickets:      tickets,
		cipher:       cipher,
		clock:        clock,
		logger:       logger,
		accelEnabled: false, // ADR-0001 / #7：门户 appid 免密换票未验证，加速路径默认关闭
	}
}

// NewWithAccel 在 NewWithAuth 的全套能力之上设定「免密加速路径」开关。
// enabled=false（默认）时 LoginWithAccel 的行为与主路径完全一致。
func NewWithAccel(doer platform.HTTPDoer, tickets platform.TicketStore, cipher platform.Cipher, clock platform.Clock, logger *slog.Logger, enabled bool) *Service {
	svc := NewWithAuth(doer, tickets, cipher, clock, logger)
	svc.accelEnabled = enabled
	return svc
}

// BindAuditStore 接入审计持久化通道（PG）。未接入时审计只写 slog。
func (s *Service) BindAuditStore(store mail.AuditStore) {
	s.audit_store = store
}

// BindSessionIssuer 接入教务会话下发。未接入时 cache_miss 不建会话。
func (s *Service) BindSessionIssuer(issuer SessionIssuer) {
	s.sessions = issuer
}

// BindTGTRefresher 接入 tgt 续期能力（由装配层把 schoolauth.TGTManager 适配进来）。
func (s *Service) BindTGTRefresher(r TGTRefresher) {
	s.tgtRefresher = r
}

// BindWebVPNTicketStore 接入网关票保管（校外代取用）。
func (s *Service) BindWebVPNTicketStore(store WebVPNTicketStore) {
	s.webvpnTickets = store
}

// BindJiaowuSTIssuer 接入「用保管 tgt 换教务 st」的能力（校外需登录页取数用）。
func (s *Service) BindJiaowuSTIssuer(i JiaowuSTIssuer) {
	s.jiaowuST = i
}

// BindFetchAccountPool 接入公共类取数账号池（管理员维护）。
func (s *Service) BindFetchAccountPool(p FetchAccountPool) {
	s.fetchAccounts = p
}

// BindFetchAccountSTIssuer 接入「取数账号整套 cookie 换 st」的能力。
func (s *Service) BindFetchAccountSTIssuer(i FetchAccountSTIssuer) {
	s.fetchST = i
}

// BindSemesterEndDates 接入稳定类缓存的学期末切换日（如 "07-10,01-10"）。
// 绑定后，稳定类（课表/成绩等）写入后整个学期命中，到学期末 + userID 哈希偏移
// （0~7 天）才判 cache_miss。未调用时稳定类仍按 dataTypeSpec TTL。
func (s *Service) BindSemesterEndDates(raw string) {
	s.semesterEnds = parseSemesterEndDates(raw)
}

// BindRenewScheduler 把续期调度器接入 Service：写票过期时刻改由观测周期决定，// 换票成功/失败时刻回喂给调度器，形成「观测 → 周期 → 写票 TTL」闭环（#7 决议）。
func (s *Service) BindRenewScheduler(sched *RenewScheduler) {
	s.scheduler = sched
}

// TicketTTL 返回当前写票窗口（由观测周期决定，未绑定调度器时回落 9 天）。
// 导出是给装配层与测试观测「观测 → 周期 → TTL」闭环是否真正打通。
func (s *Service) TicketTTL() time.Duration {
	return s.currentTicketTTL()
}

// currentTicketTTL 返回本次写票应使用的过期窗口：绑定调度器时取观测周期，
// 未绑定（纯单测场景）时回落到观察期初始上限。绝不硬编码未公开的 TTL。
func (s *Service) currentTicketTTL() time.Duration {
	if s.scheduler != nil {
		return s.scheduler.ObservedPeriod()
	}
	return ticketTTL
}

func (s *Service) recordLoginOutcome(ok bool) {
	if s.scheduler == nil {
		return
	}
	if ok {
		s.scheduler.RecordSuccess(s.clock.Now())
	} else {
		s.scheduler.RecordFailure(s.clock.Now())
	}
}

// AddressResult 是 Address 的输出：逻辑目标名 + 客户端可直接使用的编码地址。
type AddressResult struct {
	Target  string `json:"target"`
	Address string `json:"address"`
}

// Address 只返回地址，绝不发起任何出站请求。
//
// 白名单闸门是 school.Resolve：调用方只能传逻辑目标名，未知名字 / 把 URL 当名字 /
// 把 host 当名字，一律以 *school.Error{Code: CodeTargetDenied} 拒绝。闸门在任何
// 出站之前生效（本入口压根不碰 doer），因此「零出站」对成功与拒绝两条路径都成立。
//
// path 是可选查询条件（形如 /xxx 或 /xxx?k=v），以明文挂在编码地址后面；它只能
// 成为白名单 host 上的路径，无法替换目标 host —— 结构性 SSRF 保证仍然成立。
func (s *Service) Address(ctx context.Context, userID string, name school.TargetName, path string) (AddressResult, error) {
	tgt, err := school.Resolve(name)
	if err != nil {
		s.audit(ctx, userID, "address", "target_denied", string(name))
		return AddressResult{}, err
	}

	// 从「已校验过的直连形态」出发，拼上可选 path，再交给纯函数 Encode 产出
	// 经网关的编码地址（含固定 KEY_HEX 前缀）。Resolve + Encode 两步都在本入口内
	// 显式走一遍，不绕过白名单、不读任何外部状态、不发请求。
	raw := tgt.Direct.String() + normalizePath(path)
	encoded, err := school.Encode(raw)
	if err != nil {
		s.audit(ctx, userID, "address", "encode_error", string(tgt.Name))
		return AddressResult{}, err
	}

	s.audit(ctx, userID, "address", "ok", string(tgt.Name))
	return AddressResult{Target: string(tgt.Name), Address: encoded}, nil
}

// normalizePath 把可选查询条件规范成挂在直连地址后面的 path(+query) 段：
// 空值不动；缺前导斜杠时补上，保证它只能成为白名单 host 上的路径而非新目标。
func normalizePath(path string) string {
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

// FetchResult 是 Fetch 的输出：上游状态码、响应体与响应头
// （已由 platform 出站闸口剥掉 Set-Cookie 等敏感头）。
type FetchResult struct {
	Status  int
	Body    []byte
	Headers http.Header
}

// 3xx 的目标判定放在**本调用方**（见 Fetch 内的说明）：真实 HTTPDoer 必须保留
// 3xx 响应给三跳建会话用，只有代取页面这条路径需要把 3xx 转成统一错误码。
// 口径与 platform 出站闸口的骨架期实现一致，避免两处判定漂移。
func isGatewayHost(host string) bool {
	return strings.EqualFold(host, "webvpn.sicau.edu.cn")
}

func isRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// Fetch 由后端经唯一出站闸口代取，并返回结果。
//
// 与 Address 的分工：Address 只给地址、客户端自己请求；Fetch 是「需要后端代做」
// 时的那条路。二者共用同一个 school.Resolve 白名单闸门——未知名在触达出站之前
// 就被以 CodeTargetDenied 拒绝，底层零请求。
// 3xx 的 ADR-0001 判定在本方法内完成（见下方），错误码由 handler 映射为不同响应。
func (s *Service) Fetch(ctx context.Context, userID string, name school.TargetName, path string) (FetchResult, error) {
	tgt, err := school.Resolve(name)
	if err != nil {
		s.audit(ctx, userID, "fetch", "target_denied", string(name))
		return FetchResult{}, err
	}

	// 从已校验过的经网关形态出发，拼上可选的明文 path。
	raw := tgt.Vpn.String() + normalizePath(path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return FetchResult{}, err
	}

	resp, err := s.doer.Do(ctx, req)
	if err != nil {
		s.audit(ctx, userID, "fetch", "outbound_error", string(tgt.Name))
		return FetchResult{}, mapOutboundError(err)
	}
	if resp == nil {
		s.audit(ctx, userID, "fetch", "nil_response", string(tgt.Name))
		return FetchResult{}, errors.New("jiaowu: nil response from outbound doer")
	}
	defer resp.Body.Close()

	// ADR-0001 第 5 条：上游 302 指向真站时立即失败，不跟随、不回显 Location。
	//
	// 这段判定必须在**本调用方**完成，不能放回 HTTPDoer 实现层：
	//   - 真实 RealHTTPDoer 必须把 3xx 原样交回，因为 schoolauth 三跳建会话
	//     要从中间跳的 302 读 Set-Cookie 与 Location；
	//   - 而本方法（代取页面）对 3xx 的语义是「会话失效 / 真站不可达」，必须转成统一错误码。
	// 骨架期 MockHTTPDoer(Enforce=true) 曾在 Doer 层代做这件事；换真实 Doer 后
	// 该责任上移到这里，否则 302 会连 Location 一起回显给客户端（#22 回归，评审 B1）。
	if isRedirectStatus(resp.StatusCode) {
		loc := resp.Header.Get("Location")
		target, perr := url.Parse(loc)
		_ = resp.Body.Close()
		if perr == nil && target.Host != "" && isGatewayHost(target.Hostname()) {
			s.audit(ctx, userID, "fetch", "session_expired", string(tgt.Name))
			return FetchResult{}, &school.Error{Code: school.CodeSessionExpired}
		}
		s.audit(ctx, userID, "fetch", "blocked_redirect", string(tgt.Name))
		return FetchResult{}, &school.Error{Code: school.CodeBlockedRedirect}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		s.audit(ctx, userID, "fetch", "read_error", string(tgt.Name))
		return FetchResult{}, err
	}
	s.audit(ctx, userID, "fetch", "ok", string(tgt.Name))
	return FetchResult{Status: resp.StatusCode, Body: body, Headers: resp.Header.Clone()}, nil
}

// ── #15 换票主路径 ────────────────────────────────────────────────────────

// ticketTTL 是未绑定续期调度器时的写票窗口回落值（等同观察期初始上限）。
// 绑定 scheduler 后，写票过期时刻一律取 currentTicketTTL()——由「成功/首次失败
// 时刻」反推的保守周期，而非猜一个未公开的 TTL（#7 / #17 决议）。
const ticketTTL = observationWindow

// CaptchaResult 是取验证码的输出：可展示图像 + 事务标识。
type CaptchaResult struct {
	UUID  string `json:"uuid"`
	Image string `json:"image"`
}

// LoginResult 是换票成功的输出：下发给本人的票据 + 过期时刻。
type LoginResult struct {
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// captchaPayload / loginPayload 是内部传输形状，不暴露给上层。
type captchaPayload struct {
	Image string `json:"image"`
}

// Captcha 取验证码图并返回给客户端展示。上游响应体即图像内容（骨架期用假实现注入），
// 事务标识由后端生成，客户端提交时原样回传。
func (s *Service) Captcha(ctx context.Context, userID string, name school.TargetName) (CaptchaResult, error) {
	tgt, err := school.Resolve(name)
	if err != nil {
		s.audit(ctx, userID, "captcha", "target_denied", string(name))
		return CaptchaResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tgt.Vpn.String()+"/api/v1/captcha", nil)
	if err != nil {
		return CaptchaResult{}, err
	}
	resp, err := s.doer.Do(ctx, req)
	if err != nil {
		s.audit(ctx, userID, "captcha", "outbound_error", string(tgt.Name))
		return CaptchaResult{}, mapOutboundError(err)
	}
	if resp == nil {
		s.audit(ctx, userID, "captcha", "nil_response", string(tgt.Name))
		return CaptchaResult{}, errors.New("jiaowu: nil response from outbound doer")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return CaptchaResult{}, err
	}
	var p captchaPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		// 上游给的是裸图像字节（骨架期假实现），包成可展示的图像字段。
		p.Image = string(raw)
	}
	s.audit(ctx, userID, "captcha", "ok", string(tgt.Name))
	return CaptchaResult{UUID: "cap-" + string(name), Image: p.Image}, nil
}

// Login 是骨架期遗留的「码 + 事务标识」换票端点，**已停止写入票据存储**。
//
// 2026-11-17 改：真实登录走身份域的 `POST /identity/login`（LoginBySchool → 换票 →
// tgtSaverBridge 保管 tgt）。这里是另一条独立的老路径，它做两件错事：
//  1. 打的是 WebVPN 编码后的 auth 地址且**不带 appid**，学校侧不会按 CAS 语义回应；
//  2. 把**上游响应原文**封进与 TGTManager 同一个 key——上游回 HTML 就把 HTML 当票存，
//     并直接顶掉真正的 {TGT, CookieHeader}。
//
// 保留端点形态（避免既有调用方 404），但只做换票探测、**不落任何存储**，
// 并明确返回「请改用身份域登录」。
func (s *Service) Login(ctx context.Context, userID, code, uuid string) (LoginResult, error) {
	_ = code
	_ = uuid
	// 不再出站、不再写存储：落库会污染 tgt 记录（见上面注释）。
	// userID 为空也要记审计——那本身就是「未认证状态调用已停用端点」的观测点。
	s.audit(ctx, userID, "login", "deprecated_no_store", string(school.TargetAuth))
	return LoginResult{}, &school.Error{Code: school.CodeSessionExpired}
}

// Logout 删除该用户的票据记录（幂等）。
func (s *Service) Logout(ctx context.Context, userID string) error {
	if err := s.tickets.Delete(ctx, userID); err != nil {
		return err
	}
	s.audit(ctx, userID, "logout", "ok", string(school.TargetAuth))
	return nil
}

// audit 只写结构化字段，绝不把票据/密文/验证码/事务标识放进任何字段。
// target 必须是被访问的真实逻辑目标名（jiaowu / auth / webvpn），否则「谁、何时、
// 访问了哪个目标、结果如何」这条审计链会失真（ADR-0001 第 4 条）。
//
// 两条通道：slog 供 stdout→采集器→ES 检索；AuditStore 供 PG 结构化留存。
// 审计是旁路，写库失败/丢弃绝不影响业务（AuditStore 实现内部吞掉错误并计数）。
func (s *Service) audit(ctx context.Context, userID, action, result, target string) {
	s.logger.Info("school.audit",
		slog.String("userID", userID),
		slog.String("action", action),
		slog.String("result", result),
		slog.String("target", target),
	)
	if s.audit_store == nil {
		return
	}
	reqID := ""
	if ctx != nil {
		if v, ok := ctx.Value(school.CtxRequestID).(string); ok {
			reqID = v
		}
	}
	_ = s.audit_store.Append(ctx, mail.AuditEntry{
		RequestID:  reqID,
		UserID:     userID,
		Action:     action,
		Target:     target,
		Result:     result,
		OccurredAt: s.clock.Now(),
	})
}

// platformEnvelopeFailed 判定统一认证平台的失败信封（code=500 / status=error）。
func platformEnvelopeFailed(raw []byte) bool {
	var env struct {
		Code   int    `json:"code"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return false
	}
	return env.Code == 500 || env.Status == "error"
}

// accelEnvelopeOK 判定免密换票是否真的成立：必须 code=200 且 data.st 非空。
// 一个 200 的登录页 HTML / 空 data / 失败信封 / 非 200 都不算（N3 收紧）。
func accelEnvelopeOK(raw []byte) bool {
	var env struct {
		Code int `json:"code"`
		Data struct {
			ST string `json:"st"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return false
	}
	return env.Code == 200 && env.Data.ST != ""
}

// ── #17 免密加速路径（可开关）+ 失败回落 ──────────────────────────────────

// AccelResult 是免密加速路径的输出。NeedCaptcha=true 表示「请走主路径（输码）」，
// 是本票交付的核心行为——门户 appid 免密能力未被实测证实，失败即回落到它。
type AccelResult struct {
	NeedCaptcha bool   `json:"needCaptcha"`
	Code        string `json:"code,omitempty"`
}

// LoginWithAccel 尝试免密续期；开关关闭或免密失败时都回落到账密+验证码主路径。
//
// 关键约束（ADR-0001 / #7 决议）：
//   - 默认关闭；关掉时行为与主路径完全一致（零出站、要求输码）；
//   - 开关打开但失败 → 返回 CodeTicketExpired 引导用户重新输码，**绝不静默重试刷屏**
//     （全程恰好一次出站）；不落任何票据；
//   - 本票交付的是「回落行为」，不是「免密能力已支持」。真实门户换票能力保留为
//     手工验收项，未通过前不得写成已支持。
func (s *Service) LoginWithAccel(ctx context.Context) (AccelResult, error) {
	if !s.accelEnabled {
		return AccelResult{NeedCaptcha: true}, nil
	}

	tgt, err := school.Resolve(school.TargetWebvpn)
	if err != nil {
		return AccelResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tgt.Vpn.String()+"/api/v1/cas/login?appid=portal", nil)
	if err != nil {
		return AccelResult{}, err
	}
	resp, err := s.doer.Do(ctx, req)
	if err != nil {
		// 出站层拒绝（含闸口拦截）同样按「免密不成立」回落，不重试。
		s.recordLoginOutcome(false)
		return s.fallbackToCaptcha()
	}
	if resp == nil {
		s.recordLoginOutcome(false)
		return s.fallbackToCaptcha()
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		s.recordLoginOutcome(false)
		return s.fallbackToCaptcha()
	}
	// 只有「含 st 的有效成功信封」才算免密换票成立；一个 200 登录页 HTML
	// 或失败信封都不算（N3）。门户 appid 免密能力未验证，默认关闭 + 严格判定。
	if !accelEnvelopeOK(raw) {
		s.recordLoginOutcome(false)
		return s.fallbackToCaptcha()
	}

	// 成功：票据密封后按用户键控落库（加速路径不下发给客户端，由主路径统一负责）。
	sealed, err := s.cipher.Seal(raw)
	if err != nil {
		return AccelResult{}, err
	}
	if err := s.tickets.Put(ctx, "accel-user", sealed, s.clock.Now().Add(s.currentTicketTTL())); err != nil {
		return AccelResult{}, err
	}
	s.recordLoginOutcome(true)
	s.audit(ctx, "accel-user", "login_accel", "ok", string(school.TargetWebvpn))
	return AccelResult{NeedCaptcha: false}, nil
}

func (s *Service) fallbackToCaptcha() (AccelResult, error) {
	return AccelResult{
		NeedCaptcha: true,
		Code:        string(school.CodeTicketExpired),
	}, nil
}

// RenewFor 是 RenewScheduler 的执行回调：对一名即将过期的用户真正续期一次。
//
// 2026-11-17 重写。旧实现是骨架期的占位：拼一个 `appid=portal&renew=<userID>` 的假请求，
// 把**上游响应原文**封进 TicketStore。它有两个致命问题：
//  1. `appid=portal` 不是真实 appid（门户是 aea49a21-…），`renew` 参数是编的，学校侧不会认；
//  2. 它写的是与 TGTManager 同一个 key，于是每轮续期都会把真正的 {TGT, CookieHeader}
//     覆盖成一段 HTML 或错误信封——这是 cache_miss 第二天全部 session=unavailable 的直接原因。
//
// 现在只做一件事：把续期委托给 tgtManager（它持有整套凭据、会滚动保存新 tgt）。
// 没有接入 tgtManager 时**什么都不做**——绝不写任何占位数据。
func (s *Service) RenewFor(ctx context.Context, userID string) {
	if userID == "" {
		return
	}
	if s.tgtRefresher == nil {
		// 未接入续期能力：这是配置问题，不是业务失败；不写占位票、不动任何记录。
		s.audit(ctx, userID, "renew", "skipped_no_refresher", string(school.TargetAuth))
		return
	}
	if _, err := s.tgtRefresher.RefreshTGT(ctx, userID); err != nil {
		// 续期失败只回喂观测周期并记审计，不覆盖已有记录（失败时保留旧票仍可用）。
		s.recordLoginOutcome(false)
		s.audit(ctx, userID, "renew", "refresh_failed", string(school.TargetAuth))
		return
	}
	s.recordLoginOutcome(true)
	s.audit(ctx, userID, "renew", "ok", string(school.TargetAuth))
}
