package jiaowu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
)

// Service 是教务子域的服务。Address 只返回地址；Fetch 由后端经出站闸口代取；
// Captcha/Login/Logout 是 #15 的换票主路径。
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

// BindRenewScheduler 把续期调度器接入 Service：写票过期时刻改由观测周期决定，
// 换票成功/失败时刻回喂给调度器，形成「观测 → 周期 → 写票 TTL」闭环（#7 决议）。
func (s *Service) BindRenewScheduler(sched *RenewScheduler) {
	s.scheduler = sched
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
func (s *Service) Address(_ context.Context, name school.TargetName, path string) (AddressResult, error) {
	tgt, err := school.Resolve(name)
	if err != nil {
		return AddressResult{}, err
	}

	// 从「已校验过的直连形态」出发，拼上可选 path，再交给纯函数 Encode 产出
	// 经网关的编码地址（含固定 KEY_HEX 前缀）。Resolve + Encode 两步都在本入口内
	// 显式走一遍，不绕过白名单、不读任何外部状态、不发请求。
	raw := tgt.Direct.String() + normalizePath(path)
	encoded, err := school.Encode(raw)
	if err != nil {
		return AddressResult{}, err
	}

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

// Fetch 由后端经唯一出站闸口代取，并返回结果。
//
// 与 Address 的分工：Address 只给地址、客户端自己请求；Fetch 是「需要后端代做」
// 时的那条路。二者共用同一个 school.Resolve 白名单闸门——未知名在触达出站之前
// 就被以 CodeTargetDenied 拒绝，底层零请求。
//
// 302 / Set-Cookie 的处理全部在 platform 出站闸口（实现层）完成，本方法只转述错误：
// CodeBlockedRedirect、CodeSessionExpired 由调用方（handler）映射为不同响应。
func (s *Service) Fetch(ctx context.Context, name school.TargetName, path string) (FetchResult, error) {
	tgt, err := school.Resolve(name)
	if err != nil {
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
		return FetchResult{}, mapOutboundError(err)
	}
	if resp == nil {
		return FetchResult{}, errors.New("jiaowu: nil response from outbound doer")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return FetchResult{}, err
	}
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
func (s *Service) Captcha(ctx context.Context, name school.TargetName) (CaptchaResult, error) {
	tgt, err := school.Resolve(name)
	if err != nil {
		return CaptchaResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tgt.Vpn.String()+"/api/v1/captcha", nil)
	if err != nil {
		return CaptchaResult{}, err
	}
	resp, err := s.doer.Do(ctx, req)
	if err != nil {
		return CaptchaResult{}, mapOutboundError(err)
	}
	if resp == nil {
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
	return CaptchaResult{UUID: "cap-" + string(name), Image: p.Image}, nil
}

// Login 用「码 + 事务标识」向统一认证侧换票。
//
// 错误路径：上游响应体含「验证码错误」或业务码 500 → 可区分错误，且**不落任何票据**。
// 成功路径：票据密封后按用户键控写入外置存储、记录过期时刻，并按 ADR-0001 第 3 条
// 下发给已认证的本人。审计只记「谁/何时/目标/结果码」，绝不记录票据值。
func (s *Service) Login(ctx context.Context, userID, code, uuid string) (LoginResult, error) {
	if userID == "" || code == "" {
		return LoginResult{}, &school.Error{Code: school.CodeSessionExpired}
	}
	// 骨架期不真的回传验证码事务；用 auth 逻辑目标构造一次换票请求，由假出站
	// 实现注入「成功票据」或「验证码错误」两种上游行为。
	tgt, err := school.Resolve(school.TargetAuth)
	if err != nil {
		return LoginResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tgt.Vpn.String()+"/api/v1/cas/login", nil)
	if err != nil {
		return LoginResult{}, err
	}
	resp, err := s.doer.Do(ctx, req)
	if err != nil {
		return LoginResult{}, mapOutboundError(err)
	}
	if resp == nil {
		return LoginResult{}, errors.New("jiaowu: nil response from outbound doer")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return LoginResult{}, err
	}

	// 验证码错误判定：响应体含明确文案，或平台失败信封 code=500。
	if bytes.Contains(raw, []byte("验证码错误")) || platformEnvelopeFailed(raw) {
		s.audit(userID, "login", "captcha_error")
		s.recordLoginOutcome(false)
		return LoginResult{}, &school.Error{Code: school.CodeSessionExpired}
	}

	ticket := string(raw)
	sealed, err := s.cipher.Seal(raw)
	if err != nil {
		return LoginResult{}, err
	}
	expiresAt := s.clock.Now().Add(s.currentTicketTTL())
	if err := s.tickets.Put(ctx, userID, sealed, expiresAt); err != nil {
		return LoginResult{}, err
	}
	s.recordLoginOutcome(true)
	s.audit(userID, "login", "ok")
	// uuid 参与事务标识但绝不进审计，避免任何间接泄漏。
	_ = uuid
	return LoginResult{Ticket: ticket, ExpiresAt: expiresAt}, nil
}

// Logout 删除该用户的票据记录（幂等）。
func (s *Service) Logout(ctx context.Context, userID string) error {
	if err := s.tickets.Delete(ctx, userID); err != nil {
		return err
	}
	s.audit(userID, "logout", "ok")
	return nil
}

// audit 只写结构化字段，绝不把票据/密文/验证码/事务标识放进任何字段。
func (s *Service) audit(userID, action, result string) {
	s.logger.Info("school.audit",
		slog.String("userID", userID),
		slog.String("action", action),
		slog.String("result", result),
		slog.String("target", "auth"),
	)
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

// accelEnvelopeOK 判定免密换票是否真的成立：必须是 code=200 且 data 里含非空 st。
// 一个 200 的登录页 HTML / 空 data / 失败信封都不算（N3 收紧）。
func accelEnvelopeOK(raw []byte) bool {
	var env struct {
		Code int `json:"code"`
		Data struct {
			ST string `json:"st"`
		} `json:"data"`
		ST string `json:"st"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return false
	}
	if env.Code != 0 && env.Code != 200 {
		return false
	}
	return env.Data.ST != "" || env.ST != ""
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
	s.audit("accel-user", "login_accel", "ok")
	return AccelResult{NeedCaptcha: false}, nil
}

func (s *Service) fallbackToCaptcha() (AccelResult, error) {
	return AccelResult{
		NeedCaptcha: true,
		Code:        string(school.CodeTicketExpired),
	}, nil
}
