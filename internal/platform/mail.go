package platform

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ---- from mail.go ----
// 邮件发送能力。
//
// 两个实现：
//   - SMTPMailer：真实发信，配置全部来自环境变量（维护者要求所有配置进环境变量）；
//   - LogMailer：未配置 SMTP 时的回落实现，把验证码打进日志，**仅供本地调试**。
//
// 安全要点：
//   - 凭据只从配置读，绝不出现在日志里；
//   - 必须有超时，否则 SMTP 挂起会把登录线程拖死；
//   - 收件地址进日志前做部分掩码，避免日志成为通讯录泄漏面。

// Mailer 是邮件投递能力。身份域的「邮箱验证码」登录依赖它。
type Mailer interface {
	SendMailCode(ctx context.Context, to, code string) error
}

// MailOptions 是 SMTP 配置（由 config 层从环境变量装配）。
type MailOptions struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	// Timeout 是连接与读写的总超时；<=0 时用默认 10s。
	Timeout time.Duration
	// Insecure 为真时跳过 TLS 证书校验（仅用于自签证书的内网 SMTP）。
	Insecure bool
}

// SMTPMailer 是真实的 SMTP 发信实现。
type SMTPMailer struct {
	opts   MailOptions
	logger *slog.Logger
}

// NewSMTPMailer 构造 SMTP 发信器。
func NewSMTPMailer(opts MailOptions, logger *slog.Logger) (*SMTPMailer, error) {
	if opts.Host == "" {
		return nil, fmt.Errorf("platform: SMTPMailer 需要 Host")
	}
	if opts.Port == 0 {
		opts.Port = 587
	}
	if opts.From == "" {
		opts.From = opts.Username
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SMTPMailer{opts: opts, logger: logger}, nil
}

// SendMailCode 把验证码发给收件人。
func (m *SMTPMailer) SendMailCode(ctx context.Context, to, code string) error {
	subject := "农屿登录验证码"
	body := fmt.Sprintf("你的农屿登录验证码是：%s\r\n\r\n有效期 5 分钟，请勿转发给他人。\r\n如非本人操作，请忽略本邮件。", code)
	return m.send(ctx, to, subject, body)
}

func (m *SMTPMailer) send(ctx context.Context, to, subject, body string) error {
	addr := net.JoinHostPort(m.opts.Host, fmt.Sprintf("%d", m.opts.Port))
	dialer := &net.Dialer{Timeout: m.opts.Timeout}

	// 465 走隐式 TLS，其它端口走明文连接后 STARTTLS（QQ/163 等邮箱服务均支持后者）。
	var conn net.Conn
	var err error
	if m.opts.Port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
			ServerName:         m.opts.Host,
			InsecureSkipVerify: m.opts.Insecure, //nolint:gosec // 由配置显式开启，仅内网自签证书场景
			MinVersion:         tls.VersionTLS12,
		})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("连接 SMTP 失败：%w", err)
	}
	defer conn.Close()

	// 总超时兜底：SMTP 协商阶段不受 ctx 直接控制，用 deadline 保证不挂死。
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(m.opts.Timeout))
	}

	c, err := smtp.NewClient(conn, m.opts.Host)
	if err != nil {
		return fmt.Errorf("SMTP 握手失败：%w", err)
	}
	defer func() { _ = c.Close() }()

	// 非隐式 TLS 时尝试升级。
	if m.opts.Port != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{
				ServerName:         m.opts.Host,
				InsecureSkipVerify: m.opts.Insecure, //nolint:gosec
				MinVersion:         tls.VersionTLS12,
			}); err != nil {
				return fmt.Errorf("STARTTLS 失败：%w", err)
			}
		}
	}

	if m.opts.Username != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			auth := smtp.PlainAuth("", m.opts.Username, m.opts.Password, m.opts.Host)
			if err := c.Auth(auth); err != nil {
				// 注意：错误里可能带用户名的部分信息，但不会带密码。
				return fmt.Errorf("SMTP 认证失败：%w", err)
			}
		}
	}

	if err := c.Mail(m.opts.From); err != nil {
		return fmt.Errorf("设置发件人失败：%w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("设置收件人失败：%w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("开始写正文失败：%w", err)
	}
	msg := buildMessage(m.opts.From, to, subject, body)
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("写正文失败：%w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("提交正文失败：%w", err)
	}
	if err := c.Quit(); err != nil {
		// Quit 失败通常说明邮件已提交但连接收尾异常，按成功处理但记日志。
		m.logger.Warn("SMTP 退出异常（邮件可能已发出）", "err", err)
	}
	m.logger.Info("验证码邮件已发送", "to", maskEmail(to))
	return nil
}

// buildMessage 组装 RFC 5322 邮件。用 CRLF，正文 UTF-8 + base64 避免乱码。
func buildMessage(from, to, subject, body string) string {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: =?UTF-8?B?" + base64Std(subject) + "?=\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("\r\n")
	b.WriteString(base64Wrap(body))
	b.WriteString("\r\n")
	return b.String()
}

// LogMailer 是未配置 SMTP 时的回落实现：把验证码写进日志，仅供本地调试。
//
// **生产禁用**：日志里的验证码等于把登录凭据公开。启用时由装配层显式告警。
type LogMailer struct {
	Logger *slog.Logger
}

// SendMailCode 实现 Mailer。
func (m *LogMailer) SendMailCode(_ context.Context, to, code string) error {
	if m.Logger != nil {
		m.Logger.Warn("[开发模式] 未配置 SMTP，验证码只写日志",
			"to", maskEmail(to), "code", code)
	}
	return nil
}

// maskEmail 把邮箱做部分掩码：`abc***@qq.com`。
func maskEmail(s string) string {
	at := strings.IndexByte(s, '@')
	if at <= 1 {
		return "***"
	}
	keep := at / 3
	if keep > 3 {
		keep = 3
	}
	return s[:keep] + "***" + s[at:]
}

// base64Std 用于 Subject 的 RFC 2047 编码。
func base64Std(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// base64Wrap 按 76 列折行编码正文（RFC 2045 要求）。
func base64Wrap(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	return b.String()
}

// ---- from mailcode.go ----
// 邮箱验证码存储（Redis）。
//
// 决策依据：docs/tech/架构落点-身份仓.md 第六节 —— 验证码存 Redis，
// **TTL 既是有效期也是发送冷却锁**；只存哈希，不存明文；一次性消费。
//
// 为什么适合 Redis：验证码天然短命，丢了让用户重新获取即可，不值得为它建表。

// maxMailCodeAttempts 是单条验证码允许的错误尝试次数，超限即作废。
const maxMailCodeAttempts = 5

// RedisMailCodeStore 是验证码的 Redis 实现。
type RedisMailCodeStore struct {
	client *redis.Client
	prefix string
	// secret 用于对验证码做 HMAC 后再存；避免库里存明文码。
	secret []byte
}

// NewRedisMailCodeStore 构造验证码存储。
func NewRedisMailCodeStore(opts RedisOptions) (*RedisMailCodeStore, error) {
	if opts.Addr == "" {
		return nil, fmt.Errorf("platform: RedisMailCodeStore 需要 Addr")
	}
	if opts.Prefix == "" {
		opts.Prefix = "nongyu:"
	}
	// 哈希密钥独立派生：即使 Redis 泄露，也无法还原出 6 位数字码。
	sum := sha256.Sum256([]byte("mailcode:" + opts.Password + ":" + opts.Prefix))
	return &RedisMailCodeStore{
		client: redis.NewClient(&redis.Options{
			Addr:         opts.Addr,
			Password:     opts.Password,
			DB:           opts.DB,
			MaxRetries:   1,
			DialTimeout:  500 * time.Millisecond,
			ReadTimeout:  500 * time.Millisecond,
			WriteTimeout: 500 * time.Millisecond,
			PoolTimeout:  500 * time.Millisecond,
		}),
		prefix: opts.Prefix + "mailcode:",
		secret: sum[:],
	}, nil
}

// Ping 探测连通性。
func (s *RedisMailCodeStore) Ping(ctx context.Context) error { return s.client.Ping(ctx).Err() }

// Close 释放连接池。
func (s *RedisMailCodeStore) Close() error { return s.client.Close() }

func (s *RedisMailCodeStore) codeKey(email string) string     { return s.prefix + "code:" + email }
func (s *RedisMailCodeStore) attemptsKey(email string) string { return s.prefix + "try:" + email }

// hash 对验证码做 HMAC，避免明文落 Redis。
func (s *RedisMailCodeStore) hash(code string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(code))
	return hex.EncodeToString(m.Sum(nil))
}

// GenerateCode 生成指定位数的数字验证码（默认 6 位），使用 crypto/rand。
func GenerateMailCode(digits int) (string, error) {
	if digits <= 0 {
		digits = 6
	}
	max := big.NewInt(1)
	for i := 0; i < digits; i++ {
		max.Mul(max, big.NewInt(10))
	}
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", digits, n.Int64()), nil
}

// Issue 写入验证码。TTL 既是有效期也是冷却窗口：
// 调用方应先查 Cooldown，若仍处于冷却则**拒绝发送**（不覆盖旧码，避免绕过冷却）。
//
// 返回 ErrCooldown 表示 TTL 内已发过，调用方应拒绝本次请求。
func (s *RedisMailCodeStore) Issue(ctx context.Context, email, code string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	// SET NX：只有在没有未过期码时才写入 → 天然实现「TTL 内不可重复发送」。
	ok, err := s.client.SetNX(ctx, s.codeKey(email), s.hash(code), ttl).Result()
	if err != nil {
		return err
	}
	if !ok {
		return ErrCooldown
	}
	// 尝试计数与新码同寿命。
	_ = s.client.Del(ctx, s.attemptsKey(email)).Err()
	return nil
}

// Cooldown 返回该邮箱还剩多久可以再次申请；0 表示可以立即发。
func (s *RedisMailCodeStore) Cooldown(ctx context.Context, email string) (time.Duration, error) {
	ttl, err := s.client.TTL(ctx, s.codeKey(email)).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if ttl <= 0 {
		return 0, nil
	}
	return ttl, nil
}

// Verify 校验验证码。成功即删除（一次性）；失败累计次数，超限作废。
func (s *RedisMailCodeStore) Verify(ctx context.Context, email, code string) (bool, error) {
	stored, err := s.client.Get(ctx, s.codeKey(email)).Result()
	if err == redis.Nil {
		return false, nil // 没有码 → 不通过
	}
	if err != nil {
		return false, err
	}

	// 常量时间比较，避免计时侧信道。
	if subtle.ConstantTimeCompare([]byte(stored), []byte(s.hash(code))) != 1 {
		// 记一次失败；达到上限则把码删掉（作废，需重新获取）。
		n, err := s.client.Incr(ctx, s.attemptsKey(email)).Result()
		if err != nil {
			return false, err
		}
		if n >= maxMailCodeAttempts {
			_ = s.client.Del(ctx, s.codeKey(email)).Err()
			_ = s.client.Del(ctx, s.attemptsKey(email)).Err()
		}
		return false, nil
	}

	// 成功：一次性消费。
	_ = s.client.Del(ctx, s.codeKey(email)).Err()
	_ = s.client.Del(ctx, s.attemptsKey(email)).Err()
	return true, nil
}

// Clear 删除该邮箱的验证码与计数（测试与「重新获取」用）。
func (s *RedisMailCodeStore) Clear(ctx context.Context, email string) error {
	pipe := s.client.TxPipeline()
	pipe.Del(ctx, s.codeKey(email))
	pipe.Del(ctx, s.attemptsKey(email))
	_, err := pipe.Exec(ctx)
	return err
}

// ErrCooldown 表示验证码仍在有效期内，不允许重复发送。
var ErrCooldown = &cooldownError{}

type cooldownError struct{}

func (e *cooldownError) Error() string { return "platform: 验证码仍有效，请稍后再发" }

// IsCooldown 判断错误是否为「仍在冷却中」。
func IsCooldown(err error) bool {
	_, ok := err.(*cooldownError)
	return ok
}

// ---- from audit.go ----
// AuditEntry 是一条审计流水。
//
// 铁律（ADR-0001 第 4 条）：这里**只允许**放标识性字段。
// 绝不添加票据、密文、验证码、上游响应体等字段——审计表不能成为泄漏面。
type AuditEntry struct {
	// RequestID 是一次请求链路的追踪号（审计流水号）。
	RequestID string
	// UserID 是已认证身份（school.CtxUserID）；未认证场景为空串。
	UserID string
	// Action 是业务动作：login / logout / address / fetch / captcha / login_accel / renew
	Action string
	// Target 是逻辑目标名：jiaowu / auth / webvpn
	Target string
	// Result 是结果码：ok / target_denied / outbound_error / captcha_error / ...
	Result string
	// OccurredAt 是事件发生时刻（由应用时钟给出）。
	OccurredAt time.Time
}

// AuditStore 是审计流水的持久化能力。
//
// 审计是**旁路**：写入失败绝不能影响业务主流程，所以实现应当异步批量写入，
// 并把错误通过 LastError 之类的观察点暴露，而不是让调用方处理。
type AuditStore interface {
	// Append 投递一条审计（实现应为非阻塞；满缓冲后可丢弃并计数）。
	Append(ctx context.Context, entry AuditEntry) error
	// Flush 把缓冲区内的记录同步落库（关停前调用）。
	Flush(ctx context.Context) error
	// Dropped 返回因缓冲满而被丢弃的条数（可观测性）。
	Dropped() int64
	// Close 释放连接。
	Close() error
}
