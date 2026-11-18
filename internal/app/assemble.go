package app

// 本文件是装配的**总流程**：只负责「按什么顺序调用哪些阶段」，不承载具体构造细节。
//
// 拆分原则：这里每加一行都应该是「调用某个装配阶段」或「把阶段产物串起来」。
// 一旦出现具体的 `platform.NewXxx(...)` 长参数、if/else 降级分支、错误文案，
// 就说明它应该被挪进对应的 assemble_*.go。
//
// 装配顺序不可随意调整，理由写在每一步上方。总原则：
//
//	配置校验 → 外部能力 → HTTP 骨架 → 业务仓储 → 审计 → 身份域 → 业务域
//
// 原因：业务域依赖身份域（要用户存在性、要会话解析），身份域依赖外部能力
// （要 PG / Redis），而所有域都要能在 HTTP 中间件挂好之后再注册路由。

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/platform"
)

// New 完成全部装配，返回可直接 Run 的 App。
// 骨架期注入内存/Mock 实现；数据库层 / 真实出站闸口就绪后，只改这里注入即可，业务域零改动。
func New(cfg config.Config, logger *slog.Logger) (*App, error) {
	// ① 配置前置校验：失败要在碰任何外部依赖之前返回，避免「起了半个进程再报错」。
	if err := validateAuthConfig(cfg, logger); err != nil {
		return nil, err
	}
	if err := checkAppSigningConfig(cfg, logger); err != nil {
		return nil, err
	}

	// ② 外部能力：迁移（必须最先）、票据、加解密、缓存、出站 Doer。
	infra, err := buildInfra(cfg, logger)
	if err != nil {
		return nil, err
	}

	// ③ HTTP 骨架：引擎与最先挂的中间件（请求号、请求签名）。
	//    认证中间件要等 identityStore 就绪后再挂，见 ⑥。
	httpStack, err := buildHTTPStack(cfg, infra, logger)
	if err != nil {
		if infra.closeTickets != nil {
			_ = infra.closeTickets()
		}
		return nil, err
	}
	engine := httpStack.engine

	// ④ 业务仓储：按 DATABASE_URL 一次性决定四个域走内存还是 PG。
	//
	// 必须在 Migrate 之后（表要先建好）、在业务域构造之前。
	// 配了但连不上时返回错误而**不降级成内存**——静默降级会让
	// 「生产看起来正常、实际一条数据都没落库」。
	pgPool, err := openPGPool(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	// 注意：**不要在这里 defer pool.Close()**。
	// 曾经写过那样的 defer，结果它在装配**成功**返回时也执行，
	// 把连接池关掉，所有 PG 查询报 "closed pool"——而日志里只看到 500。
	// 池的生命周期归 App：由 App.Shutdown 关闭（见 App.pgPool）。
	// 中途失败时的泄漏可接受（进程即将退出，OS 会回收 fd）。
	repos, err := newRepoBundle(pgPool, infra.clock.Now)
	if err != nil {
		return nil, err
	}
	logger.Info("业务仓储已装配", "mode", string(repos.Mode))

	// ⑤ 审计持久层：配了 DATABASE_URL 才启用，否则审计只写 slog。
	auditStore, pgAudit, err := buildAuditStore(cfg, logger)
	if err != nil {
		return nil, err
	}

	// ⑥ 身份域：学校认证适配器、PG 存储、登录/会话/用户三个子域。
	//    教务源 jwSrc 同时供身份域（换票、登出清凭据）与教务域（续期、网关票）使用，
	//    所以由身份域阶段产出、向后续阶段传递。
	//    认证与限流中间件也在这一阶段挂载（它依赖 identityStore）。
	idDeps, err := buildIdentityDeps(cfg, infra, httpStack, repos, logger, auditStore)
	if err != nil {
		return nil, err
	}

	// ⑦ 业务域路由：教务 → 二课 → 身份路由 → 内容 → 消息 → 管理端 → 观测。
	//    管理端必须最后挂：它要引用前面所有已构造的域实例。
	domains, err := buildDomains(cfg, infra, httpStack, idDeps, repos, pgPool, logger, auditStore, pgAudit)
	if err != nil {
		return nil, err
	}

	a := &App{
		cfg:           cfg,
		engine:        engine,
		server:        httpStack.server,
		caps:          httpStack.caps,
		cancelRenew:   domains.cancelRenew,
		renewDone:     domains.renewDone,
		flushDone:     domains.flushDone,
		closeStore:    infra.closeTickets,
		cache:         infra.cache,
		pgAudit:       pgAudit,
		identityStore: idDeps.store,
		sessionCache:  idDeps.sessionCache,
		notifier:      domains.notifier,
		trackSvc:      domains.trackSvc,
		pgPool:        pgPool,
		storeMode:     repos.Mode,
		logger:        logger,
	}

	logAssembled(cfg, logger, infra, idDeps, pgAudit)
	if cfg.AuthKey == "" {
		// 无鉴权是「失败开放」形态：只在本地开发/单测可接受，必须显式告警，
		// 避免有人用字面量 Config 起服务时静默开放（prod 由 config.Load 强校验拦住）。
		logger.Warn("认证中间件未启用：所有业务端点无身份校验，仅限本地开发/测试使用",
			"reason", "AUTH_KEY 为空")
	}
	warnAppSigningDisabled(cfg, logger)
	return a, nil
}

// validateAuthConfig 在装配最开始校验「认证是否可能静默关闭」。
//
// #21 M3：认证默认开启。生产环境 AUTH_KEY 为空由 config.Load() 强校验拦住（不在此重写）。
// 此处补 dev 侧的显式开关：真实 dev/prod 装配（AppEnv 非空）时，AUTH_KEY 为空
// 且未显式开启 AllowInsecureDev → 拒绝装配，避免「所有业务端点无身份校验」悄悄发生。
// 判据抽到 config.IsProductionLike（单一来源）：prod / production / staging 都必须
// 挂认证；dev 由 AllowInsecureDev 显式开关兜底；test/空串是单测注入，跳过。
// 修评审 M1：之前 isRealEnv 漏了 staging，导致准生产可静默以「全端点无鉴权」启动。
func validateAuthConfig(cfg config.Config, logger *slog.Logger) error {
	if config.IsProductionLike(cfg.AppEnv) && strings.TrimSpace(cfg.AuthKey) == "" && !cfg.AllowInsecureDev {
		return fmt.Errorf("app: AUTH_KEY 为空且未开启 ALLOW_INSECURE_DEV：认证中间件不会挂载，所有业务端点将无身份校验；生产/预发布必须配置 AUTH_KEY，dev 需显式开启 ALLOW_INSECURE_DEV")
	}
	if config.IsProductionLike(cfg.AppEnv) && strings.TrimSpace(cfg.AuthKey) == "" && cfg.AllowInsecureDev {
		logger.Warn("ALLOW_INSECURE_DEV 已开启：AUTH_KEY 为空，认证中间件未挂载，所有业务端点无身份校验；仅限本地开发")
	}
	return nil
}

// logAssembled 打印装配摘要。
//
// 字段必须说真话，不写死字面量（issue #23）：
// 早先那个 "store","mock" 只被塞进 Capabilities.Store 且无任何消费者，写死 mock 会让人
// 误判「用户/会话/票据都没落库」。现在逐项报告真实装配：
//   - store   = 身份/审计的持久后端：配了 DATABASE_URL 是 postgres，否则 none；
//   - tickets = 票据后端：配了 REDIS_ADDR 是 redis（外置持久），否则 memory（重启即丢）；
//   - 其余字段同样取自真实装配变量，而非常量。
func logAssembled(cfg config.Config, logger *slog.Logger, infra infraDeps, id identityDeps, pgAudit *platform.PGAuditStore) {
	storeBackend := "none"
	if cfg.DatabaseURL != "" {
		storeBackend = "postgres"
	}
	ticketsBackend := "memory"
	if cfg.RedisAddr != "" {
		ticketsBackend = "redis"
	}
	logger.Info("装配完成", "addr", cfg.Addr, "env", cfg.AppEnv,
		"store", storeBackend, "tickets", ticketsBackend,
		"outboundEnforce", cfg.HTTPEnforce, "outboundDoer", infra.outboundDoer,
		"accelEnabled", cfg.AccelEnabled,
		"authEnabled", cfg.AuthKey != "", "appSignEnabled", len(cfg.AppSigningKeys) > 0,
		"cacheEnabled", infra.cache != nil,
		"identityEnabled", id.store != nil, "auditEnabled", pgAudit != nil,
		"sessionCacheEnabled", id.sessionCache != nil)
}

// buildAuditStore 构造审计持久层；未配库时返回 nil（审计只写 slog）。
func buildAuditStore(cfg config.Config, logger *slog.Logger) (platform.AuditStore, *platform.PGAuditStore, error) {
	if cfg.DatabaseURL == "" {
		logger.Warn("审计持久层未启用：审计只写 stdout", "hint", "配置 DATABASE_URL 以落库到 Postgres")
		return nil, nil, nil
	}
	st, err := platform.NewPGAuditStore(platform.PGOptions{
		DSN:         cfg.DatabaseURL,
		BufferSize:  2048,
		BatchSize:   64,
		FlushPeriod: time.Second,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("审计存储（Postgres）初始化失败：%w", err)
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := st.Ping(pingCtx); err != nil {
		return nil, nil, fmt.Errorf("审计存储（Postgres）不可达：%w", err)
	}
	return st, st, nil
}

// runCacheFlusher 是缓存回灌循环：每秒调一次 FlushOnce，调用频次 × 每次条数 = 限速。
// Redis 仍不可用时 FlushOnce 返回错误，这里只记一次日志并继续（不刷屏、不重试风暴）。
func runCacheFlusher(ctx context.Context, cache *platform.RedisCache, logger *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		failing := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if cache.PendingBackfill() == 0 {
					if failing {
						logger.Info("缓存回灌：积压已清空")
						failing = false
					}
					continue
				}
				n, err := cache.FlushOnce(ctx)
				if err != nil {
					if !failing {
						logger.Warn("缓存回灌受阻（Redis 仍不可用），保留积压下轮再试", "err", err)
						failing = true
					}
					continue
				}
				if n > 0 {
					logger.Info("缓存回灌进度", "flushed", n, "pending", cache.PendingBackfill())
				}
			}
		}
	}()
	return done
}
