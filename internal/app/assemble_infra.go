package app

// 本文件装配**外部能力**：数据库迁移、票据存储、加解密、通用缓存、出站 Doer、对象存储。
//
// 这些能力的共同点：它们都来自 platform（最底层），与任何业务域无关。
// 放在一个文件里，是为了让「本服务到底依赖哪些外部系统」可以一眼看全。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/platform/objectstore"
)

// infraDeps 是装配第一阶段产出的外部能力集合。
// 用结构体而不是长参数列表，避免后续加一个依赖就改一次签名。
type infraDeps struct {
	tickets       platform.TicketStore // 教务 tgt（只给 TGTManager 用）
	presenceStore platform.TicketStore // 校内/校外上报状态
	webvpnStore   platform.TicketStore // 客户端交上来的 wengine_vpn_ticket
	closeTickets  func() error
	cipher        platform.Cipher
	cache         *platform.RedisCache // 未配置 Redis 时为 nil
	doer          platform.HTTPDoer
	outboundDoer  string // "real" / "mock"，仅用于启动日志
	clock         platform.Clock
}

// buildInfra 一次装配全部外部能力。
func buildInfra(cfg config.Config, logger *slog.Logger) (infraDeps, error) {
	var d infraDeps

	if err := migrateDatabase(cfg); err != nil {
		return d, err
	}

	tickets, presenceStore, webvpnStore, closeTickets, err := buildTicketStores(cfg, logger)
	if err != nil {
		return d, err
	}
	d.tickets, d.presenceStore, d.webvpnStore, d.closeTickets = tickets, presenceStore, webvpnStore, closeTickets

	// 票据主密钥用的是真实 AES-256-GCM（NewAESCipher 是 NewMockCipher 的真名别名，
	// 见 #21 M9：骨架期命名误导，装配层新代码一律用 NewAESCipher）。
	d.cipher = platform.NewAESCipher(cfg.TicketKey)

	if d.cache, err = buildCache(cfg); err != nil {
		// 错误路径上释放已建的 Redis 连接，避免半装配泄漏。
		if closeTickets != nil {
			_ = closeTickets()
		}
		return d, err
	}

	d.doer, d.outboundDoer = buildOutboundDoer(cfg)
	d.clock = platform.NewRealClock()
	return d, nil
}

// migrateDatabase 在装配最开始幂等执行 migrations/*.sql。
//
// 迁移（issue #23 / Spec M4）：配了 DATABASE_URL 时，启动即把 migrations/*.sql
// 幂等执行一遍。必须排在**所有 Postgres 仓储构造之前**——PGAuditStore 的
// Ping、PGIdentityStore 的 Ping 都只探测连通性、不建表，业务查询要求表已存在。
// Migrate 自带小连接池、执行完即关；search_path 由 DSN 透传，测试隔离同样生效。
// 失败即拒绝启动：带着半成品 schema 上线比直接失败更危险。
func migrateDatabase(cfg config.Config) error {
	if cfg.DatabaseURL == "" {
		return nil
	}
	migCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := platform.Migrate(migCtx, cfg.DatabaseURL); err != nil {
		return fmt.Errorf("启动迁移失败：%w", err)
	}
	return nil
}

// buildTicketStores 构造三个**用途独立**的票据存储。
//
// **必须按用途分命名空间**（2026-11-17 事故）：TicketStore 的 key 只有
// `<prefix>ticket:<userID>`，而「教务 tgt」「校内状态」「网关票」三种东西都要
// 「按用户存一段不透明字节」。共用实例会互相当成对方的记录覆盖——一次
// POST /school/login 或续期回调把上游返回的 HTML 写进去，就把真正的
// {TGT, CookieHeader} 顶掉了。症状是次日 cache_miss 全部 session=unavailable。
//
// 现在每个用途一个独立实例（Redis 用不同 Prefix 段、内存用独立 map），
// 从存储层保证「谁也不会写到别人的记录上」，不依赖调用方自觉。
func buildTicketStores(cfg config.Config, logger *slog.Logger) (tickets, presenceStore, webvpnStore platform.TicketStore, closeAll func() error, err error) {
	if cfg.RedisAddr == "" {
		// 内存实现：三个独立实例，互不覆盖。
		logger.Warn("票据存储使用进程内存实现：重启即丢票，仅限本地开发/测试", "hint", "配置 REDIS_ADDR 以启用外置持久")
		return platform.NewMockTicketStore(),
			platform.NewMockTicketStore(),
			platform.NewMockTicketStore(),
			nil, nil
	}

	newStore := func(kind string) (*platform.RedisTicketStore, error) {
		s, e := platform.NewRedisTicketStore(platform.RedisOptions{
			Addr:     cfg.RedisAddr,
			Password: cfg.RedisPassword,
			DB:       cfg.RedisDB,
			Prefix:   cfg.RedisPrefix + kind + ":",
		})
		if e != nil {
			return nil, e
		}
		// 启动期探测：连不上就拒绝启动，避免带着坏依赖上线。
		pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if e := s.Ping(pingCtx); e != nil {
			_ = s.Close()
			return nil, fmt.Errorf("票据存储（Redis）不可达 %s：%w", cfg.RedisAddr, e)
		}
		return s, nil
	}

	tgtStore, e := newStore("tgt")
	if e != nil {
		return nil, nil, nil, nil, fmt.Errorf("票据存储（Redis）初始化失败：%w", e)
	}
	presenceRedis, e := newStore("presence")
	if e != nil {
		_ = tgtStore.Close()
		return nil, nil, nil, nil, fmt.Errorf("校内状态存储（Redis）初始化失败：%w", e)
	}
	webvpnRedis, e := newStore("webvpn")
	if e != nil {
		_ = tgtStore.Close()
		_ = presenceRedis.Close()
		return nil, nil, nil, nil, fmt.Errorf("网关票存储（Redis）初始化失败：%w", e)
	}

	return tgtStore, presenceRedis, webvpnRedis, func() error {
		return errors.Join(tgtStore.Close(), presenceRedis.Close(), webvpnRedis.Close())
	}, nil
}

// buildCache 构造通用缓存层。
//
// 通用缓存层：穿透/雪崩/击穿/降级回灌四类防护都在实现里。
// 只有配了 Redis 才启用；未配置时 cache 为 nil，业务侧按「无缓存直接命中」处理。
func buildCache(cfg config.Config) (*platform.RedisCache, error) {
	if cfg.RedisAddr == "" {
		return nil, nil
	}
	rc, err := platform.NewRedisCache(platform.RedisOptions{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
		Prefix:   cfg.RedisPrefix,
	}, platform.CacheOptions{
		DefaultTTL:       time.Duration(cfg.CacheDefaultTTLSec) * time.Second,
		JitterRatio:      cfg.CacheJitterRatio,
		NotFoundTTL:      time.Duration(cfg.CacheNotFoundTTLSec) * time.Second,
		RebuildTimeout:   2 * time.Second,
		FallbackEnabled:  true,
		FlushRatePerSec:  cfg.CacheFlushRatePerSec,
		BreakerThreshold: cfg.CacheBreakerThreshold,
		BreakerCooldown:  time.Duration(cfg.CacheBreakerCooldownSec) * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("缓存层（Redis）初始化失败：%w", err)
	}
	return rc, nil
}

// buildOutboundDoer 构造出站 Doer。
//
// 出站 Doer：HTTPEnforce=true（config.Load() 默认 true）→ 真实实现 RealHTTPDoer，
// 真的发网络请求；false → 保留骨架期 MockHTTPDoer（#11 契约测试 / 上层单测隔离用）。
//
// ⚠️ 注意（避免后人误读）：HTTPEnforce 这**一个**开关在当前装配层同时决定了两件事——
//  1. 是否强制安全策略（白名单 / 不跟随重定向 / 超时 / 响应体上限）；
//  2. 是否使用真实网络出站。
//
// 这两件事在语义上其实是**正交**的（理论上可以「强制策略 + 走 mock」，或「真实网络
// + 不强制」），但 config.Config 目前只有 HTTPEnforce 这一个字段，装配层也就只有这一个
// 判据可用。作为本票（#22）的最小改动，这里让二者共用该开关是可接受的折中；不要据此
// 推断「Enforce 的语义就等于真实网络」——Enforce 本义只是「强制安全策略」。
//
// 后续若要把两者解耦，应在 config 增加独立字段（例如 HTTPUseRealDoer），
// 而不是复用 Enforce。**注意 Enforce=false 时 MockHTTPDoer 没有 SSRF 防护**，
// 关掉这个开关等于同时关掉出站闸口——生产环境必须保持 true。
func buildOutboundDoer(cfg config.Config) (doer platform.HTTPDoer, kind string) {
	if cfg.HTTPEnforce {
		return platform.NewRealHTTPDoer(platform.RealHTTPDoerOptions{
			AllowedHosts: outboundAllowlist(cfg.HTTPAllowlist),
			Timeout:      time.Duration(cfg.HTTPTimeoutSec) * time.Second,
		}), "real"
	}
	return platform.NewMockHTTPDoer(platform.DoerPolicy{
		Enforce:      false, // 骨架期：零值策略，只记录 + 返回可配置响应
		AllowedHosts: outboundAllowlist(cfg.HTTPAllowlist),
		Timeout:      time.Duration(cfg.HTTPTimeoutSec) * time.Second,
	}), "mock"
}

// buildObjectStore 构造对象存储：配了 MINIO_ENDPOINT 就用真实 MinIO，否则回落内存实现。
//
// 与 PG 不同：这里**允许降级到内存**并显式告警，因为附件是可丢的
// （Spec 里附件清理本身就是旁路），不像数据持久化那样必须硬失败。
// 但告警必须显眼，否则「生产附件重启即丢」会一直没人发现。
func buildObjectStore(cfg config.Config, logger *slog.Logger) (platform.ObjectStore, string, error) {
	if cfg.MinIOEndpoint == "" {
		logger.Warn("对象存储使用内存实现：重启即丢附件，仅限本地开发/测试",
			"hint", "配置 MINIO_ENDPOINT 以启用真实存储")
		return platform.NewMemoryObjectStore(), "memory", nil
	}
	ms, err := objectstore.New(context.Background(), objectstore.Options{
		Endpoint:  cfg.MinIOEndpoint,
		AccessKey: cfg.MinIOAccessKey,
		SecretKey: cfg.MinIOSecretKey,
		Bucket:    cfg.MinIOBucket,
		UseSSL:    cfg.MinIOUseSSL,
	})
	if err != nil {
		return nil, "", err
	}
	logger.Info("对象存储已装配", "impl", "minio", "endpoint", cfg.MinIOEndpoint, "bucket", cfg.MinIOBucket)
	return ms, "minio", nil
}

// outboundAllowlist 合并「内建的真实目标 host」与配置里的额外白名单。
// 内建项保证经 school.Resolve + Encode 产出的 webvpn 域地址一定可出站；
// 配置项供部署时追加（如测试环境域名），改配置即时生效。
//
// 注意：内建清单必须覆盖**所有后端真正会请求的学校域**。漏一个的症状是
// `platform: outbound target denied`——请求在白名单校验处就被拦下、零出站，
// 看起来像「学校站不可达」或「tgt 失效」，实际只是 host 没登记。
// xsgz.sicau.edu.cn（学工系统）曾是漏项：它是当前唯一数据源，
// 换 token 与取档案都打它，漏了会导致登录成功但画像全空（2026-11-13 实测踩到）。
func outboundAllowlist(extra string) []string {
	base := []string{
		"jiaowu.sicau.edu.cn",
		"auth.sicau.edu.cn",
		"webvpn.sicau.edu.cn",
		// 学工系统：唯一数据源。tgt 换 access token、enterHall、student/detail 全打它。
		"xsgz.sicau.edu.cn",
	}
	return append(base, parseAllowlist(extra)...)
}

// parseAllowlist 把逗号分隔的白名单串拆成去空白、去空项的主机名列表。
func parseAllowlist(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if h := strings.TrimSpace(p); h != "" {
			out = append(out, h)
		}
	}
	return out
}
