package config_test

import (
	"testing"

	"nongyu-backend-plus/internal/config"
)

// 生产环境不得用占位密钥启动：Load 必须拒绝。
func TestLoad_RejectsPlaceholderKeyInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("AUTH_KEY", "a-real-auth-signing-key-for-tests")
	t.Setenv("AUTH_KEY", "a-real-auth-signing-key-for-tests")
	t.Setenv("TICKET_KEY", "") // 触发默认占位值
	if _, err := config.Load(); err == nil {
		t.Fatal("prod 环境缺失 TICKET_KEY 时 Load 必须报错，不能静默用占位密钥启动")
	}
}

func TestLoad_RejectsShortKeyInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("AUTH_KEY", "a-real-auth-signing-key-for-tests")
	t.Setenv("TICKET_KEY", "short")
	if _, err := config.Load(); err == nil {
		t.Fatal("prod 环境密钥过短时 Load 必须报错")
	}
}

func TestLoad_AcceptsStrongKeyInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("AUTH_KEY", "a-real-auth-signing-key-for-tests")
	t.Setenv("TICKET_KEY", "a-32-byte-minimum-secret-key-here-abcdefg")
	t.Setenv("REDIS_ADDR", "127.0.0.1:6379")
	t.Setenv("APP_SIGNING_KEYS", "dev:bm9uZ3l1LWRldi1hcHAtc2lnbi1rZXktdjEtMzJiISE")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("强密钥应通过：%v", err)
	}
	if cfg.AppEnv != "prod" {
		t.Fatalf("AppEnv 不符：%q", cfg.AppEnv)
	}
}

func TestLoad_AllowsPlaceholderInDev(t *testing.T) {
	t.Setenv("APP_ENV", "dev")
	t.Setenv("TICKET_KEY", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("dev 应允许占位密钥以便本地起服务：%v", err)
	}
	if cfg.TicketKey == "" {
		t.Fatal("dev 应回落到一个非空占位密钥")
	}
}

// HTTP_ENFORCE 在生产下不得被关掉：安全不变量不能被环境变量弱化。
func TestLoad_RejectsDisabledEnforceInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("AUTH_KEY", "a-real-auth-signing-key-for-tests")
	t.Setenv("TICKET_KEY", "a-32-byte-minimum-secret-key-here-abcdefg")
	t.Setenv("HTTP_ENFORCE", "false")
	if _, err := config.Load(); err == nil {
		t.Fatal("prod 环境不得关闭出站闸口强制")
	}
}

func TestLoad_RejectsMissingAuthKeyInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("AUTH_KEY", "")
	t.Setenv("TICKET_KEY", "a-32-byte-minimum-secret-key-here-abcdefg")
	if _, err := config.Load(); err == nil {
		t.Fatal("prod 环境必须配置 AUTH_KEY")
	}
}

// 生产不得用进程内存存票据（重启即丢），必须配 REDIS_ADDR。
func TestLoad_RejectsMissingRedisInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("AUTH_KEY", "a-real-auth-signing-key-for-tests")
	t.Setenv("TICKET_KEY", "a-32-byte-minimum-secret-key-here-abcdefg")
	t.Setenv("REDIS_ADDR", "")
	if _, err := config.Load(); err == nil {
		t.Fatal("prod 环境必须配置 REDIS_ADDR")
	}
}

func TestLoad_RejectsMissingAppSigningKeysInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("AUTH_KEY", "a-real-auth-signing-key-for-tests")
	t.Setenv("TICKET_KEY", "a-32-byte-minimum-secret-key-here-abcdefg")
	t.Setenv("REDIS_ADDR", "127.0.0.1:6379")
	t.Setenv("APP_SIGNING_KEYS", "")
	if _, err := config.Load(); err == nil {
		t.Fatal("prod 环境必须配置 APP_SIGNING_KEYS")
	}
}

func TestParseAppSigningKeys(t *testing.T) {
	keys, err := config.ParseAppSigningKeys("dev:bm9uZ3l1LWRldi1hcHAtc2lnbi1rZXktdjEtMzJiISE")
	if err != nil {
		t.Fatalf("合法密钥应解析成功：%v", err)
	}
	if string(keys["dev"]) != "nongyu-dev-app-sign-key-v1-32b!!" {
		t.Fatalf("密钥字节不符：%q", keys["dev"])
	}
	if _, err := config.ParseAppSigningKeys("dev:YQ"); err == nil {
		t.Fatal("短密钥必须拒绝")
	}
	if _, err := config.ParseAppSigningKeys("not a key"); err == nil {
		t.Fatal("格式错误必须拒绝")
	}
}
