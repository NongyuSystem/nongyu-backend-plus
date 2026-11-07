// Package config 负责从环境变量读取并校验启动时配置。
// 骨架阶段只提供最小配置集；后续新增配置时在此集中定义，禁止在业务包里直接读 os.Getenv。
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config 是进程启动所需的全部外部配置。
// 约定：所有字段都有安全默认值，保证无 .env 时也能直接 go run 起一个健康检查服务。
type Config struct {
	// Addr 是 HTTP 监听地址，形如 ":8080"。
	Addr string
	// AppEnv 是运行环境标识（dev / staging / prod），仅用于日志与行为开关。
	AppEnv string
	// ShutdownTimeoutSec 是优雅关停等待在途请求的最长秒数。
	ShutdownTimeoutSec int
	// TicketKey 是 Cipher 实现对票据做封装所用的密钥。
	// 生产必须显式配置；缺项时回落到仅用于本地开发的占位值（保证无 .env 也能起服务）。
	TicketKey string
	// HTTPTimeoutSec 是出站请求（HTTPDoer 实现层）的单次超时秒数。
	HTTPTimeoutSec int
	// HTTPAllowlist 是出站目标白名单，逗号分隔的主机名列表。
	// 完整强制策略由 HTTPDoer 实现层落地；缺项时为空（骨架期不强制）。
	HTTPAllowlist string
}

// Load 从环境变量装配 Config，并做最小校验。
// 环境变量缺项时回落到默认值，不因缺配置而拒绝启动。
func Load() (Config, error) {
	cfg := Config{
		Addr:               envOr("ADDR", ":8080"),
		AppEnv:             envOr("APP_ENV", "dev"),
		ShutdownTimeoutSec: envIntOr("SHUTDOWN_TIMEOUT_SEC", 10),
		// 缺项有安全默认值：占位密钥 + 5 秒超时 + 空白名单（骨架期不强制）。
		TicketKey:      envOr("TICKET_KEY", "dev-only-placeholder-key-change-me"),
		HTTPTimeoutSec: envIntOr("HTTP_TIMEOUT_SEC", 5),
		HTTPAllowlist:  envOr("HTTP_ALLOWLIST", ""),
	}

	if cfg.ShutdownTimeoutSec <= 0 {
		return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT_SEC 必须为正整数，当前值：%d", cfg.ShutdownTimeoutSec)
	}
	if cfg.HTTPTimeoutSec <= 0 {
		return Config{}, fmt.Errorf("HTTP_TIMEOUT_SEC 必须为正整数，当前值：%d", cfg.HTTPTimeoutSec)
	}
	return cfg, nil
}

// envOr 读取字符串环境变量，缺失或为空时返回默认值。
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envIntOr 读取整数环境变量，缺失或非法时返回默认值。
func envIntOr(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
