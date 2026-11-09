package platform

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"time"
)

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
