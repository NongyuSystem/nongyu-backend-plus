package jiaowu

import (
	"bytes"
	"context"
	"encoding/json"
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
	doer    *GuardedDoer
	tickets platform.TicketStore
	cipher  platform.Cipher
	clock   platform.Clock
	logger  *slog.Logger
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
		doer:    newGuardedDoer(doer),
		tickets: tickets,
		cipher:  cipher,
		clock:   clock,
		logger:  logger,
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

// FetchResult 是 Fetch 的输出：上游状态码与响应体（已由闸口剥掉 Set-Cookie）。
type FetchResult struct {
	Status int
	Body   []byte
}

// Fetch 由后端经唯一出站闸口代取，并返回结果。
//
// 与 Address 的分工：Address 只给地址、客户端自己请求；Fetch 是「需要后端代做」
// 时的那条路。二者共用同一个 school.Resolve 白名单闸门——未知名在触达出站之前
// 就被以 CodeTargetDenied 拒绝，底层零请求。
//
// 302 / Set-Cookie 的处理全部在 GuardedDoer（实现层）完成，本方法只转述错误：
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
		return FetchResult{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return FetchResult{}, err
	}
	return FetchResult{Status: resp.StatusCode, Body: body}, nil
}

// ── #15 换票主路径 ────────────────────────────────────────────────────────

// ticketTTL 是换票成功后写入外置存储的过期窗口骨架期取值。真实 TTL 由统一认证侧
// 长期凭据的已知窗口决定（见 #17 的观测驱动续期），此处只给一个可测的非零窗口。
const ticketTTL = 9 * 24 * time.Hour

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
		return CaptchaResult{}, err
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
		return LoginResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return LoginResult{}, err
	}

	// 验证码错误判定：响应体含明确文案，或平台失败信封 code=500。
	if bytes.Contains(raw, []byte("验证码错误")) || platformEnvelopeFailed(raw) {
		s.audit(userID, "login", "captcha_error")
		return LoginResult{}, &school.Error{Code: school.CodeSessionExpired}
	}

	ticket := string(raw)
	sealed, err := s.cipher.Seal(raw)
	if err != nil {
		return LoginResult{}, err
	}
	expiresAt := s.clock.Now().Add(ticketTTL)
	if err := s.tickets.Put(ctx, userID, sealed, expiresAt); err != nil {
		return LoginResult{}, err
	}
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
