package config_test

// 0-day 回归测试（lead 写，覆盖 config 层修复）。
// 修复前均为 RED：
//   - V3：prod 强校验接受 1 字节 AUTH_KEY（令牌 HMAC 密钥可被穷举伪造）
//   - V4：TRUSTED_PROXIES 填 0.0.0.0/0 会被接受（等于重新开放 XFF 伪造）

import (
	"testing"

	"nongyu-backend-plus/internal/config"
)

func TestZZRegression_ProdRejectsShortAuthKey(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("AUTH_KEY", "x") // 1 字节
	t.Setenv("TICKET_KEY", "a-32-byte-production-ticket-key!!")
	t.Setenv("APP_SIGNING_KEYS", "kid:"+base64URL32())
	t.Setenv("REDIS_ADDR", "127.0.0.1:6379")
	t.Setenv("HTTP_ENFORCE", "true")
	t.Setenv("RATE_LIMIT_PER_MINUTE", "60")
	t.Setenv("TRUSTED_PROXIES", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("短 AUTH_KEY 仍通过 prod 强校验：任意人可穷举伪造会话令牌")
	}
	t.Logf("修复后正确拒绝：%v", err)
}

func TestZZRegression_ProdAcceptsStrongAuthKey(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("AUTH_KEY", "a-32-byte-production-auth-key!!!!")
	t.Setenv("TICKET_KEY", "a-32-byte-production-ticket-key!!")
	t.Setenv("APP_SIGNING_KEYS", "kid:"+base64URL32())
	t.Setenv("REDIS_ADDR", "127.0.0.1:6379")
	t.Setenv("HTTP_ENFORCE", "true")
	t.Setenv("RATE_LIMIT_PER_MINUTE", "60")
	t.Setenv("TRUSTED_PROXIES", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("合法强配置不应被拒：%v", err)
	}
	if len(cfg.AuthKey) < 32 {
		t.Fatalf("强密钥应保持，实际长度 %d", len(cfg.AuthKey))
	}
}

// staging 是准生产，必须与 prod 一样走密钥强校验；否则会用占位 TICKET_KEY
// 起服务，而占位密钥是源码公开常量，tgt / 身份证密文可被直接解开。
func TestZZRegression_StagingEnforcesStrongKeys(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	t.Setenv("AUTH_KEY", "a-32-byte-production-auth-key!!!!")
	t.Setenv("TICKET_KEY", "") // 触发默认占位值
	t.Setenv("APP_SIGNING_KEYS", "kid:"+base64URL32())
	t.Setenv("REDIS_ADDR", "127.0.0.1:6379")
	t.Setenv("HTTP_ENFORCE", "true")
	t.Setenv("RATE_LIMIT_PER_MINUTE", "60")
	t.Setenv("TRUSTED_PROXIES", "")

	if _, err := config.Load(); err == nil {
		t.Fatal("staging 缺失 TICKET_KEY 时 Load 必须报错，不能静默用占位密钥启动")
	}
}

func TestZZRegression_TrustedProxiesRejectsUnsafeCIDR(t *testing.T) {
	for _, raw := range []string{"0.0.0.0/0", "::/0", "0.0.0.0/0,10.0.0.1"} {
		if _, err := config.ParseTrustedProxies(raw); err == nil {
			t.Fatalf("TRUSTED_PROXIES=%q 会重新开放全网 XFF 伪造，必须拒绝", raw)
		}
	}
	for _, raw := range []string{"", "10.0.0.1", "10.0.0.0/8,2001:db8::/32"} {
		if _, err := config.ParseTrustedProxies(raw); err != nil {
			t.Fatalf("TRUSTED_PROXIES=%q 是合法部署形态，不应被拒：%v", raw, err)
		}
	}
}

// base64URL32 生成 32 字节全零密钥的 base64url 形式（测试专用，非真实凭据）。
func base64URL32() string {
	return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
}
