// Package app 是装配层（composition root）。
//
// 唯一职责：把 config / platform / server / 业务域 拼起来。
// 依赖注入只发生在这里；业务域之间不互相 import，需要协作也由本层编排。
// 这样「换数据库实现」「换框架」「拆微服务」都只动这一层。
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"nongyu-backend-plus/internal/config"
	identityflow "nongyu-backend-plus/internal/identity/flow"
	identitylogin "nongyu-backend-plus/internal/identity/login"
	identitysession "nongyu-backend-plus/internal/identity/session"
	identityuser "nongyu-backend-plus/internal/identity/user"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/jiaowu"
	"nongyu-backend-plus/internal/school/presence"
	"nongyu-backend-plus/internal/school/schoolauth"
	"nongyu-backend-plus/internal/server"

	"github.com/gin-gonic/gin"
)

// Capabilities 是装配层注入给业务域的全部外部能力快照。
// 业务域只依赖 platform 里的接口类型，不依赖具体实现；这里集中暴露便于测试与后续编排。
type Capabilities struct {
	Store   platform.Store
	Tickets platform.TicketStore
	Cipher  platform.Cipher
	Doer    platform.HTTPDoer
	Clock   platform.Clock
	Cache   *platform.RedisCache // 未配置 Redis 时为 nil
}

// renewInterval 是定时续期扫描的间隔（保守值，避免高频打扰学校侧）。
const renewInterval = 5 * time.Minute

// App 持有一个已装配完成的进程。
type App struct {
	cfg         config.Config
	engine      *gin.Engine
	server      *http.Server
	caps        Capabilities
	cancelRenew context.CancelFunc
	renewDone   <-chan struct{}
	renewOnce   sync.Once
	flushDone   <-chan struct{}
	closeStore  func() error
	cache       *platform.RedisCache
	pgAudit     *platform.PGAuditStore
	// 身份域资源（未配置 DATABASE_URL 时为 nil）
	identityStore *platform.PGIdentityStore
	sessionCache  *platform.RedisSessionCache
	logger        *slog.Logger
}

// New 完成全部装配，返回可直接 Run 的 App。
// 骨架期注入内存/Mock 实现；数据库层 / 真实出站闸口就绪后，只改这里注入即可，业务域零改动。
func New(cfg config.Config, logger *slog.Logger) (*App, error) {
	store := platform.NewMockStore()

	// 票据存储：配了 REDIS_ADDR 就用外置持久实现（生产必须），否则回落到进程内存（仅本地开发）。
	var tickets platform.TicketStore
	var closeTickets func() error
	if cfg.RedisAddr != "" {
		redisStore, err := platform.NewRedisTicketStore(platform.RedisOptions{
			Addr:     cfg.RedisAddr,
			Password: cfg.RedisPassword,
			DB:       cfg.RedisDB,
			Prefix:   cfg.RedisPrefix,
		})
		if err != nil {
			return nil, fmt.Errorf("票据存储（Redis）初始化失败：%w", err)
		}
		// 启动期探测：连不上就拒绝启动，避免带着坏依赖上线。
		pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := redisStore.Ping(pingCtx); err != nil {
			return nil, fmt.Errorf("票据存储（Redis）不可达 %s：%w", cfg.RedisAddr, err)
		}
		tickets = redisStore
		closeTickets = redisStore.Close
	} else {
		tickets = platform.NewMockTicketStore()
		logger.Warn("票据存储使用进程内存实现：重启即丢票，仅限本地开发/测试", "hint", "配置 REDIS_ADDR 以启用外置持久")
	}

	cipher := platform.NewMockCipher(cfg.TicketKey)

	// 通用缓存层：穿透/雪崩/击穿/降级回灌四类防护都在实现里。
	// 只有配了 Redis 才启用；未配置时 cache 为 nil，业务侧按「无缓存直接命中」处理。
	var cache *platform.RedisCache
	if cfg.RedisAddr != "" {
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
		cache = rc
	}

	// 出站 Doer：HTTPEnforce=true（config.Load() 默认 true）→ 真实实现 RealHTTPDoer，
	// 真的发网络请求；false → 保留骨架期 MockHTTPDoer（#11 契约测试 / 上层单测隔离用）。
	//
	// 注意（避免后人误读）：HTTPEnforce 这**一个**开关在当前装配层同时决定了两件事——
	//   ① 是否强制安全策略（白名单 / 不跟随重定向 / 超时 / 响应体上限）；
	//   ② 是否使用真实网络出站。
	// 这两件事在语义上其实是**正交**的（理论上可以「强制策略 + 走 mock」，或「真实网络
	// + 不强制」），但 config.Config 目前只有 HTTPEnforce 这一个字段，装配层也就只有这一个
	// 判据可用。作为本票（#22）的最小改动，这里让二者共用该开关是可接受的折中；不要据此
	// 推断「Enforce 的语义就等于真实网络」——Enforce 本义只是「强制安全策略」。后续若要把
	// 两者解耦，应在 config 增加独立字段（例如 HTTPUseRealDoer），而不是复用 Enforce。
	//
	// MockHTTPDoer 本身与其测试（caps_gateway_test.go 等）原样保留，零改动。
	var doer platform.HTTPDoer
	outboundDoer := "mock"
	if cfg.HTTPEnforce {
		doer = platform.NewRealHTTPDoer(platform.RealHTTPDoerOptions{
			AllowedHosts: outboundAllowlist(cfg.HTTPAllowlist),
			Timeout:      time.Duration(cfg.HTTPTimeoutSec) * time.Second,
		})
		outboundDoer = "real"
	} else {
		doer = platform.NewMockHTTPDoer(platform.DoerPolicy{
			Enforce:      false, // 骨架期：零值策略，只记录 + 返回可配置响应
			AllowedHosts: outboundAllowlist(cfg.HTTPAllowlist),
			Timeout:      time.Duration(cfg.HTTPTimeoutSec) * time.Second,
		})
	}
	clock := platform.NewRealClock()
	engine := server.New()

	// 请求追踪号：每个请求一个审计流水号，写入 context 与 X-Request-Id 响应头。
	// 必须最先挂，后续所有 handler 与审计都能拿到它。
	engine.Use(server.RequestIDMiddleware())

	// 审计持久层（Postgres）：配了 DATABASE_URL 才启用，否则审计只写 slog。
	var auditStore platform.AuditStore
	var pgAudit *platform.PGAuditStore
	if cfg.DatabaseURL != "" {
		st, err := platform.NewPGAuditStore(platform.PGOptions{
			DSN:         cfg.DatabaseURL,
			BufferSize:  2048,
			BatchSize:   64,
			FlushPeriod: time.Second,
		})
		if err != nil {
			return nil, fmt.Errorf("审计存储（Postgres）初始化失败：%w", err)
		}
		pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := st.Ping(pingCtx); err != nil {
			return nil, fmt.Errorf("审计存储（Postgres）不可达：%w", err)
		}
		auditStore = st
		pgAudit = st
	} else {
		logger.Warn("审计持久层未启用：审计只写 stdout", "hint", "配置 DATABASE_URL 以落库到 Postgres")
	}

	// 令牌签发/验签器：登录成功后用它把 session_id 包成客户端可用的令牌。
	// AuthKey 为空时（仅本地开发）不签发令牌，登录接口会因此不可用。
	var tokenIssuer *server.TokenVerifier
	if cfg.AuthKey != "" {
		tokenIssuer = server.NewTokenVerifier(cfg.AuthKey, clock)
	}

	// 学校认证适配器：把「客户端交上来的 ywtb 凭据」换成学校身份。
	// 认证走 ywtb getUserInfo（Bearer + laravel_session cookie），
	// 教务那一路（专业/班级/年级）暂未接，失败只标记画像不完整，不阻断登录。
	var schoolAuthAdapter identitylogin.SchoolAuthenticator
	// tgt 保管器：登录成功后按用户保存，之后取学籍不必再让客户端交凭据。
	var tgtMgr *schoolauth.TGTManager
	if authSrc, err := schoolauth.NewYWTBSource(schoolauth.YWTBOptions{Doer: doer}); err == nil {
		// 教务源：专业/班级/年级来自 bjiben.asp（GBK 老页面）。
		var academicSrc schoolauth.AcademicSource
		if jwSrc, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: doer}); err == nil {
			// tgt 保管：换票响应会带回新 tgt，服务端加密存 9 天并滚动续期，
			// 客户端只需首次登录时交一次，之后不必反复传凭据。
			if cfg.TicketKey != "" && tickets != nil {
				if mgr, err := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
					Store:  tickets,
					Cipher: platform.NewMockCipher(cfg.TicketKey + ":tgt"),
					Source: jwSrc,
					Clock:  clock,
				}); err == nil {
					jwSrc.WithTGT(mgr)
					tgtMgr = mgr
					logger.Info("已启用 tgt 保管：换票续期由服务端自动完成")
				} else {
					logger.Warn("tgt 保管未启用，每次取学籍需客户端提供凭据", "err", err)
				}
			}
			academicSrc = jwSrc
		} else {
			logger.Warn("教务取数源构造失败，专业/班级/年级将不可用", "err", err)
		}
		if a, err := schoolauth.New(schoolauth.Options{Auth: authSrc, Academic: academicSrc}); err == nil {
			schoolAuthAdapter = schoolauthBridge{a: a}
		} else {
			logger.Warn("学校认证适配器构造失败，学校认证登录将不可用", "err", err)
		}
	} else {
		logger.Warn("ywtb 取数源构造失败，学校认证登录将不可用", "err", err)
	}

	// 身份域持久层：与审计共用同一个 Postgres（库 nongyu），但连接池独立。
	// 身份域只在配了 DATABASE_URL 时启用——没有持久化就无法承载用户与会话。
	var identityStore *platform.PGIdentityStore
	var sessionCache *platform.RedisSessionCache
	var identityUserSvc *identityuser.Service
	var identitySessionSvc *identitysession.Service
	var identityLoginSvc *identitylogin.Service
	if cfg.DatabaseURL != "" {
		st, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: cfg.DatabaseURL})
		if err != nil {
			return nil, fmt.Errorf("身份域存储初始化失败：%w", err)
		}
		pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err = st.Ping(pingCtx)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("身份域存储（Postgres）不可达：%w", err)
		}
		identityStore = st

		identityUserSvc = identityuser.New(st, st)
		identitySessionSvc = identitysession.New(st, st, clock)

		// 会话 Redis 副本：可用则加速鉴权读，不可用不影响正确性（回落 PG）。
		if cfg.RedisAddr != "" {
			if rc, err := platform.NewRedisSessionCache(platform.RedisOptions{
				Addr:     cfg.RedisAddr,
				Password: cfg.RedisPassword,
				DB:       cfg.RedisDB,
				Prefix:   cfg.RedisPrefix,
			}); err == nil {
				pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				if err := rc.Ping(pingCtx); err == nil {
					sessionCache = rc
					identitySessionSvc.WithCache(rc)
				} else {
					logger.Warn("会话 Redis 副本不可用，鉴权将直读 Postgres", "err", err)
					_ = rc.Close()
				}
				cancel()
			}
		}

		// 邮件投递：配了 SMTP_HOST 就用真实发信，否则回落到日志实现（仅本地调试）。
		var mailer platform.Mailer
		if cfg.SMTPHost != "" {
			smtpMailer, err := platform.NewSMTPMailer(platform.MailOptions{
				Host: cfg.SMTPHost, Port: cfg.SMTPPort,
				Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
				From: cfg.SMTPFrom, Insecure: cfg.SMTPInsecure,
			}, logger)
			if err != nil {
				return nil, fmt.Errorf("SMTP 发信初始化失败：%w", err)
			}
			mailer = smtpMailer
			logger.Info("邮件验证码使用 SMTP 发信", "host", cfg.SMTPHost, "port", cfg.SMTPPort)
		} else {
			mailer = &platform.LogMailer{Logger: logger}
			logger.Warn("未配置 SMTP_HOST：验证码只写日志，仅限本地开发")
		}

		// 邮箱验证码存储需要 Redis；没有 Redis 时该登录方式不可用。
		var mailCodes *platform.RedisMailCodeStore
		if cfg.RedisAddr != "" {
			if mc, err := platform.NewRedisMailCodeStore(platform.RedisOptions{
				Addr:     cfg.RedisAddr,
				Password: cfg.RedisPassword,
				DB:       cfg.RedisDB,
				Prefix:   cfg.RedisPrefix,
			}); err == nil {
				pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				if err := mc.Ping(pingCtx); err == nil {
					mailCodes = mc
				} else {
					logger.Warn("验证码存储（Redis）不可用，邮箱验证码登录将不可用", "err", err)
					_ = mc.Close()
				}
				cancel()
			}
		}

		// 登录子域：密码镜像与身份证必须用**两把独立密钥**（ADR-0002 第 2 条二修）。
		if cfg.TicketKey != "" {
			identityLoginSvc = identitylogin.New(identitylogin.Options{
				Users:       st,
				Profiles:    st,
				Methods:     st,
				MailCodes:   mailCodes,
				Mailer:      mailer,
				Clock:       clock,
				UserService: identityUserSvc,
				PasswordBox: platform.NewMockCipher(cfg.TicketKey + ":password"),
				IDCardBox:   platform.NewMockCipher(cfg.TicketKey + ":idcard"),
				// 学校认证：用客户端交上来的 Bearer token + cookie 走 ywtb 取数。
				School: schoolAuthAdapter,
				// 登录成功后按用户保管 tgt；后续取学籍/续期不再需要客户端交凭据。
				TGTSaver: tgtSaverBridge{mgr: tgtMgr},
			})
		}
	} else {
		logger.Warn("身份域未启用：未配置 DATABASE_URL，用户与会话能力不可用")
	}

	// 认证与限流：AuthKey 非空时挂到所有业务路由之前（gin 的 Use 必须先于路由注册）。
	//
	// 身份域可用时用**可吊销**中间件：令牌只带会话标识，验签后还要查会话，
	// 因此能踢人（单设备顶替、登出、账号禁用即时生效）。
	// 身份域不可用时退化为旧的自包含令牌中间件，并在日志里显式告警——
	// 那种模式下吊销不生效，只适合本地开发。
	if cfg.AuthKey != "" {
		verifier := tokenIssuer
		limiter := server.NewRateLimiter(clock, cfg.RateLimitPerMinute, time.Minute)
		if identityStore != nil {
			resolver := server.NewSessionStoreResolver(identityStore, identityStore, clock)
			engine.Use(server.RevocableAuthMiddleware(verifier, resolver, server.ScopeApp),
				server.RateLimitMiddleware(limiter))
		} else {
			logger.Warn("使用不可吊销的自包含令牌鉴权：登出与踢人不会生效，仅限本地开发")
			engine.Use(server.AuthMiddleware(verifier), server.RateLimitMiddleware(limiter))
		}
	}

	// 编排：把 platform 能力通过接口注入教务子域，再把子域路由注册到 gin.Engine。
	// 业务子域不 import app/server/config；装配是这一层唯一职责（架构落点草图 §8）。
	// #17 起用 NewWithAccel + RegisterAccel：加速开关从 cfg.AccelEnabled 读（默认 false），
	// 端点在生产装配里真正可达；续期调度器把观测周期回喂给 Service 的写票 TTL。
	scheduler := jiaowu.NewRenewScheduler(tickets, clock)
	jiaowuSvc := jiaowu.NewWithAccel(doer, tickets, cipher, clock, logger, cfg.AccelEnabled)
	jiaowuSvc.BindRenewScheduler(scheduler)
	jiaowuSvc.BindAuditStore(auditStore)
	jiaowu.RegisterAccel(engine, jiaowuSvc)

	// 身份域路由：登录/登出走编排层 flow（它负责「登录成功后签发会话」），
	// 画像读取与修改走 user 子域。身份全部来自 context，不读请求体里的 userID。
	if identityStore != nil {
		if identityLoginSvc != nil && identitySessionSvc != nil && identityUserSvc != nil {
			flowSvc := identityflow.New(identityLoginSvc, identitySessionSvc, identityUserSvc, clock)
			identityflow.Register(engine, flowSvc, tokenIssuer)
		}
		if identityUserSvc != nil {
			identityuser.Register(engine, identityUserSvc, server.CurrentUserID, identityStore)
		}
	}

	// 定时续期循环：调度器只挑候选/错峰，真正执行换票交给 Service.RenewFor（经出站闸口）。
	// ctx 由 App 持有，Shutdown 时 cancel + Stop，保证优雅关停、不留悬挂 goroutine。
	renewCtx, cancelRenew := context.WithCancel(context.Background())
	renewDone := scheduler.Run(renewCtx, renewInterval, jiaowuSvc.RenewFor)

	// 缓存回灌循环：Redis 不可用期间写入内存的条目，在恢复后按限速（每秒 N 条）
	// 慢慢同步回 Redis，避免恢复瞬间把 Redis 打满。无缓存时退化为空循环。
	var flushDone <-chan struct{}
	if cache != nil {
		flushDone = runCacheFlusher(renewCtx, cache, logger)
	}

	// #16 校内/校外状态：给 presence 一个**独立**的票据存储实例，避免与后续票据
	// 共用 userID 单键互相覆盖（#16 子代理回报里提示的键冲突，此处拍板隔离）。
	// 后端配置先用常量 ModeDirect、陈旧阈值显式传 5 分钟；两者若要可配置需扩 config，
	// 属后续 effort，本期不动 config 以免超出票范围。
	presenceSvc := presence.New(
		platform.NewMockTicketStore(),
		clock,
		presence.ModeDirect,
		5*time.Minute,
	)
	presence.Register(engine, presenceSvc)

	a := &App{
		cfg:    cfg,
		engine: engine,
		server: &http.Server{
			Addr:              cfg.Addr,
			Handler:           engine,
			ReadHeaderTimeout: 5 * time.Second,
		},
		caps: Capabilities{
			Store:   store,
			Tickets: tickets,
			Cipher:  cipher,
			Doer:    doer,
			Clock:   clock,
			Cache:   cache,
		},
		cancelRenew:   cancelRenew,
		renewDone:     renewDone,
		flushDone:     flushDone,
		closeStore:    closeTickets,
		cache:         cache,
		pgAudit:       pgAudit,
		identityStore: identityStore,
		sessionCache:  sessionCache,
		logger:        logger,
	}
	logger.Info("装配完成", "addr", cfg.Addr, "env", cfg.AppEnv, "store", "mock", "capabilities", 6,
		"outboundEnforce", cfg.HTTPEnforce, "outboundDoer", outboundDoer,
		"accelEnabled", cfg.AccelEnabled,
		"authEnabled", cfg.AuthKey != "", "cacheEnabled", cache != nil)
	// 无鉴权是「失败开放」形态：只在本地开发/单测可接受，必须显式告警，
	// 避免有人用字面量 Config 起服务时静默开放（prod 由 config.Load 强校验拦住）。
	if cfg.AuthKey == "" {
		logger.Warn("认证中间件未启用：所有业务端点无身份校验，仅限本地开发/测试使用",
			"reason", "AUTH_KEY 为空")
	}
	return a, nil
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

// outboundAllowlist 合并「内建的三个真实目标 host」与配置里的额外白名单。
// 内建项保证经 school.Resolve + Encode 产出的 webvpn 域地址一定可出站；
// 配置项供部署时追加（如测试环境域名），改配置即时生效。
func outboundAllowlist(extra string) []string {
	base := []string{
		"jiaowu.sicau.edu.cn",
		"auth.sicau.edu.cn",
		"webvpn.sicau.edu.cn",
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

// Capabilities 返回装配层注入的全部外部能力（只读快照）。
func (a *App) Capabilities() Capabilities { return a.caps }

// Handler 返回已装配好的 HTTP handler，供测试与存活探针校验。
func (a *App) Handler() http.Handler { return a.engine }

// Run 阻塞监听 HTTP 请求。
func (a *App) Run() error {
	return a.server.ListenAndServe()
}

// Shutdown 在给定超时内优雅关停：先停定时续期循环，再等待在途请求结束。
func (a *App) Shutdown(timeout time.Duration) error {
	a.stopRenew()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := a.server.Shutdown(ctx)
	// 释放外置存储连接池（Redis 未配置时 closeStore 为空）。
	if a.closeStore != nil {
		_ = a.closeStore()
	}
	return err
}

// stopRenew 幂等地停止定时续期循环并等待其 goroutine 退出（有上界，避免关停挂死）。
func (a *App) stopRenew() {
	a.renewOnce.Do(func() {
		if a.cancelRenew != nil {
			a.cancelRenew()
		}
		if a.renewDone != nil {
			select {
			case <-a.renewDone:
			case <-time.After(2 * time.Second):
			}
		}
		// 等缓存回灌循环退出（同一 ctx，cancel 后应立即返回）。
		if a.flushDone != nil {
			select {
			case <-a.flushDone:
			case <-time.After(2 * time.Second):
			}
		}
		// 关停前再冲一次积压，尽量不丢降级期间写入的内存条目。
		if a.cache != nil {
			if n, err := a.cache.FlushOnce(context.Background()); err == nil && n > 0 && a.logger != nil {
				a.logger.Info("关停前最后一次缓存回灌", "flushed", n)
			}
			_ = a.cache.Close()
		}
		// 审计：关停前把缓冲中的记录落库（审计是旁路，失败不影响退出）。
		if a.pgAudit != nil {
			flushCtx, flushCancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err := a.pgAudit.Flush(flushCtx); err != nil && a.logger != nil {
				a.logger.Warn("关停前审计落库失败", "err", err)
			}
			flushCancel()
			if dropped := a.pgAudit.Dropped(); dropped > 0 && a.logger != nil {
				a.logger.Warn("本次运行有审计记录被丢弃（审计是旁路，不影响业务）", "dropped", dropped)
			}
			_ = a.pgAudit.Close()
		}
		// 身份域资源：先关会话缓存，再关存储（顺序避免关闭后仍有读）。
		if a.sessionCache != nil {
			_ = a.sessionCache.Close()
		}
		if a.identityStore != nil {
			_ = a.identityStore.Close()
		}
	})
}
