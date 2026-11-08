// Package config 负责从环境变量读取并校验启动时配置。
// 骨架阶段只提供最小配置集；后续新增配置时在此集中定义，禁止在业务包里直接读 os.Getenv。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const placeholderTicketKey = "dev-only-placeholder-key-change-me"

// minProductionTicketKeyLen 是 prod 环境 TICKET_KEY 的最小长度（字节）。
// 密钥经 SHA-256 派生为 AES-256 密钥，但弱口令仍易被字典攻击，故设下限。
const minProductionTicketKeyLen = 32

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
	// HTTPEnforce 是否启用出站闸口强制（白名单/超时/302/Set-Cookie）。
	// 缺省 false 保留骨架期 mock 行为；生产装配显式置 true。
	HTTPEnforce bool
	// AccelEnabled 是否启用免密换票加速路径（#17）。
	// 门户 appid 免密能力未验证，缺省 false；真实手工验收通过前不得置 true。
	AccelEnabled bool
	// AuthKey 是认证令牌 HMAC 签名密钥。生产必须显式配置（强校验）。
	// 空值表示「不挂认证中间件」，仅供本地开发与单测使用。
	AuthKey string
	// RateLimitPerMinute 是每用户每分钟允许的请求数。
	RateLimitPerMinute int
	// RedisAddr 是票据存储的 Redis 地址（形如 127.0.0.1:6379）。
	// 为空时回落到进程内存实现（仅本地开发；进程重启即丢票，不能上生产）。
	RedisAddr string
	// RedisPassword 是 Redis 密码。
	RedisPassword string
	// RedisDB 是 Redis 逻辑库编号。
	RedisDB int
	// RedisPrefix 是票据键名前缀，便于多环境隔离。
	RedisPrefix string
	// CacheDefaultTTLSec 是缓存正常值的默认存活秒数。
	CacheDefaultTTLSec int
	// CacheJitterRatio 是 TTL 抖动比例（防雪崩），0~1。
	CacheJitterRatio float64
	// CacheNotFoundTTLSec 是「查无此物」空值缓存的存活秒数（防穿透）。
	CacheNotFoundTTLSec int
	// CacheFlushRatePerSec 是降级恢复后回灌内存数据到 Redis 的每秒限速（防打垮）。
	CacheFlushRatePerSec int
	// CacheBreakerThreshold 是 Redis 连续失败多少次后打开熔断（快速失败）。
	CacheBreakerThreshold int
	// CacheBreakerCooldownSec 是熔断冷却秒数，期满转半开探测。
	CacheBreakerCooldownSec int
	// DatabaseURL 是 Postgres 连接串（审计流水等结构化数据）。
	// 为空时不启用持久层，审计只写 slog。
	DatabaseURL string
}

// Load 从环境变量装配 Config，并做最小校验。
// 环境变量缺项时回落到默认值，不因缺配置而拒绝启动。
func Load() (Config, error) {
	cfg := Config{
		Addr:               envOr("ADDR", ":8080"),
		AppEnv:             envOr("APP_ENV", "dev"),
		ShutdownTimeoutSec: envIntOr("SHUTDOWN_TIMEOUT_SEC", 10),
		// 缺项有安全默认值：占位密钥 + 5 秒超时 + 空白名单。
		TicketKey:               envOr("TICKET_KEY", placeholderTicketKey),
		HTTPTimeoutSec:          envIntOr("HTTP_TIMEOUT_SEC", 5),
		HTTPAllowlist:           envOr("HTTP_ALLOWLIST", ""),
		HTTPEnforce:             envBoolOr("HTTP_ENFORCE", true),   // 生产默认启用出站闸口
		AccelEnabled:            envBoolOr("ACCEL_ENABLED", false), // 门户免密未验证，默认关闭
		AuthKey:                 envOr("AUTH_KEY", ""),             // 生产强校验要求非空
		RateLimitPerMinute:      envIntOr("RATE_LIMIT_PER_MINUTE", 60),
		RedisAddr:               envOr("REDIS_ADDR", ""),
		RedisPassword:           envOr("REDIS_PASSWORD", ""),
		RedisDB:                 envIntOr("REDIS_DB", 0),
		RedisPrefix:             envOr("REDIS_PREFIX", "nongyu:"),
		CacheDefaultTTLSec:      envIntOr("CACHE_DEFAULT_TTL_SEC", 300),
		CacheJitterRatio:        envFloatOr("CACHE_JITTER_RATIO", 0.2),
		CacheNotFoundTTLSec:     envIntOr("CACHE_NOT_FOUND_TTL_SEC", 15),
		CacheFlushRatePerSec:    envIntOr("CACHE_FLUSH_RATE_PER_SEC", 100),
		CacheBreakerThreshold:   envIntOr("CACHE_BREAKER_THRESHOLD", 5),
		CacheBreakerCooldownSec: envIntOr("CACHE_BREAKER_COOLDOWN_SEC", 5),
		DatabaseURL:             envOr("DATABASE_URL", ""),
	}

	if cfg.ShutdownTimeoutSec <= 0 {
		return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT_SEC 必须为正整数，当前值：%d", cfg.ShutdownTimeoutSec)
	}
	if cfg.HTTPTimeoutSec <= 0 {
		return Config{}, fmt.Errorf("HTTP_TIMEOUT_SEC 必须为正整数，当前值：%d", cfg.HTTPTimeoutSec)
	}

	// 生产强校验：ADR-0001 的安全不变量不得被默认值或环境变量悄悄弱化。
	if isProduction(cfg.AppEnv) {
		if cfg.TicketKey == placeholderTicketKey || strings.TrimSpace(cfg.TicketKey) == "" {
			return Config{}, fmt.Errorf("prod 环境必须显式配置 TICKET_KEY，不得使用开发占位密钥")
		}
		if len(cfg.TicketKey) < minProductionTicketKeyLen {
			return Config{}, fmt.Errorf("prod 环境 TICKET_KEY 至少 %d 字节，当前 %d", minProductionTicketKeyLen, len(cfg.TicketKey))
		}
		if !cfg.HTTPEnforce {
			return Config{}, fmt.Errorf("prod 环境不得关闭 HTTP_ENFORCE（出站闸口强制）")
		}
		if strings.TrimSpace(cfg.AuthKey) == "" {
			return Config{}, fmt.Errorf("prod 环境必须显式配置 AUTH_KEY（认证令牌签名密钥）")
		}
		if cfg.RateLimitPerMinute <= 0 {
			return Config{}, fmt.Errorf("prod 环境 RATE_LIMIT_PER_MINUTE 必须为正整数")
		}
		if strings.TrimSpace(cfg.RedisAddr) == "" {
			return Config{}, fmt.Errorf("prod 环境必须配置 REDIS_ADDR（票据需外置持久，内存实现重启即丢）")
		}
	}
	return cfg, nil
}

// isProduction 判断运行环境是否为生产（大小写不敏感，prod/production 均算）。
func isProduction(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "prod", "production":
		return true
	}
	return false
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

// envFloatOr 读取浮点环境变量，缺失或非法时返回默认值。
func envFloatOr(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

// envBoolOr 读取布尔环境变量（true/1/yes/on 为真），缺失或非法时返回默认值。
func envBoolOr(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
