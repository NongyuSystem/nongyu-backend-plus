package app

// 本文件装配 **业务域路由**：教务 → 校内状态/校外访问 → 二课 → 身份路由
// → 内容（公告/帖子/皮肤/埋点）→ 消息 → 管理端 → 观测出口。
//
// 每个域都遵循同一个三段式：
//
//	构造 Service（注入 Repo / Clock / 可选依赖）
//	→ 注入可选能力（BindXxx，缺依赖时不 bind、端点不挂）
//	→ 注册路由（RegisterXxx）
//
// 「缺依赖」一律走**告警 + 不挂路由**，绝不让进程起不来——其它域不该被一个可选依赖拖死。
// 唯一的例外是 schema 校验失败时宁可不挂端点：挂上去也只会写入未校验的数据。

import (
	"context"
	"fmt"
	"log/slog"
	jiaowustore "nongyu-backend-plus/internal/school/jiaowu/store"
	"time"

	"nongyu-backend-plus/internal/app/notify"
	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/content/announcement"
	"nongyu-backend-plus/internal/content/post"
	identityflow "nongyu-backend-plus/internal/identity/flow"
	identityuser "nongyu-backend-plus/internal/identity/user"
	"nongyu-backend-plus/internal/message"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/platform/mail"
	"nongyu-backend-plus/internal/school/jiaowu"
	"nongyu-backend-plus/internal/school/presence"
	"nongyu-backend-plus/internal/school/secondclass"
	"nongyu-backend-plus/internal/server"
	"nongyu-backend-plus/internal/skin"
	"nongyu-backend-plus/internal/track"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// domainDeps 是装配第四阶段的产物：需要挂到 App 上的运行时句柄。
type domainDeps struct {
	cancelRenew context.CancelFunc
	renewDone   <-chan struct{}
	flushDone   <-chan struct{}
	notifier    *notify.Notifier
	trackSvc    *track.Service
}

// buildDomains 构造并注册全部业务域路由。
func buildDomains(cfg config.Config, infra infraDeps, httpStack *httpStack, id identityDeps, repos repoBundle, pgPool *pgxpool.Pool, logger *slog.Logger, auditStore mail.AuditStore, pgAudit *mail.PGAuditStore) (domainDeps, error) {
	var d domainDeps
	engine := httpStack.engine
	clock := infra.clock

	// ── 教务域 ────────────────────────────────────────────────────
	//
	// 编排：把 platform 能力通过接口注入教务子域，再把子域路由注册到 gin.Engine。
	// 业务子域不 import app/server/config；装配是这一层唯一职责（架构落点草图 §8）。
	// #17 起用 NewWithAccel + RegisterAccel：加速开关从 cfg.AccelEnabled 读（默认 false），
	// 端点在生产装配里真正可达；续期调度器把观测周期回喂给 Service 的写票 TTL。
	scheduler := jiaowu.NewRenewScheduler(infra.tickets, clock)
	jiaowuSvc := jiaowu.NewWithAccel(infra.doer, infra.tickets, infra.cipher, clock, logger, cfg.AccelEnabled)
	jiaowuSvc.BindRenewScheduler(scheduler)
	jiaowuSvc.BindAuditStore(auditStore)

	// 公共类取数账号池：只有 tgt + webvpn 票、不对应任何 app 用户的专用凭据，
	// 由管理员通过管理端维护。配了 DATABASE_URL 走 PG（加密落库），否则走内存。
	var fetchAccountAdmin jiaowu.FetchAccountAdminStore
	if pgPool != nil {
		repo := jiaowustore.NewFetchAccountPGRepo(pgPool, cfg.TicketKey, clock.Now)
		jiaowuSvc.BindFetchAccountPool(repo)
		fetchAccountAdmin = repo
	} else {
		repo := jiaowustore.NewFetchAccountMemoryRepo()
		jiaowuSvc.BindFetchAccountPool(repo)
		fetchAccountAdmin = repo
	}
	if id.jwSrc != nil {
		// 取数账号的 st 用整套 auth cookie 换，不进 TGTManager 的用户级保管。
		jiaowuSvc.BindFetchAccountSTIssuer(fetchAccountSTBridge{src: id.jwSrc})
		// 两种能力共用同一个桥：
		//   - SessionIssuer：direct 形态下用已保管 tgt 换 ASPSESSIONID（要求能到 jiaowu）
		//   - TGTRefresher：续期回调走真正的 tgt 续期
		//   - JiaowuSTIssuer：vpn 形态下用已保管 tgt 换教务 st，回投编码 caslogin
		//     建网关侧教务会话（校外取课表等需登录页必需）
		// vpn 形态的**网关票**仍只能由客户端真实登录产生，后端换不到；
		// 这里补的是「教务会话」，两者不是一回事。
		bridge := jiaowuSessionBridge{src: id.jwSrc}
		jiaowuSvc.BindSessionIssuer(bridge)
		jiaowuSvc.BindTGTRefresher(bridge)
		jiaowuSvc.BindJiaowuSTIssuer(bridge)
	}
	jiaowu.RegisterAccel(engine, jiaowuSvc)

	// 回传缓存子域（#40/#41/#32/#34）：把缓存层与 schema 校验器注入教务子域，
	// 再挂 GET/POST/DELETE /school/jiaowu/cache/:dataType。
	//
	// 这两件能力都是「有则启用、无则端点回 503」的可选依赖：
	//   - cache 为 nil：没配 REDIS_ADDR，纯缓存语义无处落地；
	//   - schema 编译失败：意味着某类回传永远不会被校验，宁可让端点不可用，
	//     也不能放未校验的数据进缓存。
	cacheSubdomainReady := false
	if infra.cache != nil {
		validator, verr := jiaowu.NewSchemaValidator()
		if verr != nil {
			// fail-fast 的边界：这里不让整个进程起不来（其它子域不受影响），
			// 但必须显式告警且不挂缓存路由——挂上去也只会写入未校验数据。
			logger.Error("回传缓存子域未启用：schema 校验器编译失败", "err", verr)
		} else {
			jiaowuSvc.BindCache(infra.cache)
			jiaowuSvc.BindSchemaValidator(validator)
			jiaowu.RegisterCache(engine, jiaowuSvc)
			cacheSubdomainReady = true
		}
	}
	if !cacheSubdomainReady {
		logger.Warn("回传缓存端点未挂载：需要 REDIS_ADDR 且 schema 全部编译通过",
			"cache_configured", infra.cache != nil)
	}

	// 校内/校外状态（#16）：用独立命名空间的 presenceStore（不再临时 NewMockTicketStore——
	// 那会让上报状态在配了 Redis 的生产里也丢，且与 tgt 争同一个 key）。
	// 后端配置先用常量 ModeDirect、陈旧阈值显式传 5 分钟；两者若要可配置需扩 config，
	// 属后续 effort，本期不动 config 以免超出票范围。
	presenceSvc := presence.New(infra.presenceStore, clock, presence.ModeDirect, 5*time.Minute)
	presence.Register(engine, presenceSvc)

	// 校外访问：客户端交上来的 wengine_vpn_ticket 按用户加密保管，
	// 后端用它经编码地址代取教务页（不需要 tgt、不需要教务 st）。
	//
	// 票的来源只能是客户端真实登录网关——后端换不到（见 schoolauth 里删除
	// IssueGatewayCookies 的说明）。所以这里只做保管与代取，绝不自造票。
	if infra.webvpnStore != nil {
		jiaowuSvc.BindWebVPNTicketStore(webvpnTicketStore{
			store:  infra.webvpnStore,
			cipher: platform.NewAESCipher(cfg.TicketKey + ":webvpn"),
		})
		jiaowu.RegisterWebVPN(engine, jiaowuSvc)
	} else {
		logger.Warn("WebVPN 代取端点未挂载：需要可用的票据存储")
	}

	// ── 学校二课（#71 / spec-学校二课接入.md）─────────────────────
	//
	// 后端只做「请求配方下发」与「记录」，对二课零出站、零凭据。
	// 配方与写类端点只依赖包级静态表（当前只读）+ 审计，不依赖 Redis/schema，
	// 故无条件挂载；读副本三条端点才需要缓存与 schema 校验，走与教务同款的降级 gate。
	//
	// 两个降级分支都必须「告警 + 不挂对应路由 + 进程照常起」，绝不静默降级，
	// 也绝不因可选依赖缺失让整个进程起不来（其它子域不受影响）。
	secondclassSvc := secondclass.New(secondclass.Options{
		Clock:  clock,
		Logger: logger,
		Audit:  auditStore,
	})
	// 写类配方端点：无条件可用。没配 Redis 时客户端仍能拿到「怎么请求」的配方。
	secondclass.RegisterRecipe(engine, secondclassSvc)

	secondclassCacheReady := false
	if infra.cache != nil {
		validator, verr := secondclass.NewSchemaValidator()
		if verr != nil {
			// schema 编译失败意味着某类回传永远不会被校验，宁可不挂记录端点，
			// 也不能放未校验的数据进缓存（与教务同款 fail-fast 边界）。
			logger.Error("二课记录端点未启用：schema 校验器编译失败", "err", verr)
		} else {
			secondclassSvc.BindCache(infra.cache)
			secondclassSvc.BindSchemaValidator(validator)
			secondclass.RegisterCache(engine, secondclassSvc)
			secondclassCacheReady = true
		}
	}
	if !secondclassCacheReady {
		logger.Warn("二课记录端点未挂载（写类配方端点仍可用）：需要 REDIS_ADDR 且 schema 全部编译通过",
			"cache_configured", infra.cache != nil)
	}

	// ── 身份域路由 ────────────────────────────────────────────────
	//
	// 登录/登出走编排层 flow（它负责「登录成功后签发会话」），
	// 画像读取与修改走 user 子域。身份全部来自 context，不读请求体里的 userID。
	if id.store != nil {
		if id.loginSvc != nil && id.sessionSvc != nil && id.userSvc != nil {
			// 注入 UserRepo 以记录登录审计（IP/设备/时间，issue #29）。
			flowSvc := identityflow.New(id.loginSvc, id.sessionSvc, id.userSvc, clock).WithUserRepo(id.store)
			if id.jwSrc != nil {
				// 登出时清掉学校凭据（tgt）：ADR-0001 第 3 条「退出登录即清除」。
				// 用同一个桥，它的 ClearCredential 直接委托 TGTManager.Clear。
				flowSvc.WithCredentialCleaner(jiaowuSessionBridge{src: id.jwSrc})
			}
			identityflow.Register(engine, flowSvc, httpStack.tokenV)
		}
		if id.userSvc != nil {
			// 本人端点挂普通 engine；按学号读画像属于管理台，挂到下面的
			// adminGroup（ScopeAdmin + AdminOnly）。两者不可混挂：
			// 早先整个 Register 都挂在 engine 上，导致任意 role=0 的 App
			// 会话可读全校学生画像（spec-身份域与平台底座:72 只允许管理员）。
			identityuser.RegisterPublic(engine, id.userSvc, server.CurrentUserID)
		}
	}

	// ── 定时循环 ──────────────────────────────────────────────────
	//
	// 定时续期循环：调度器只挑候选/错峰，真正执行换票交给 Service.RenewFor（经出站闸口）。
	// ctx 由 App 持有，Shutdown 时 cancel + Stop，保证优雅关停、不留悬挂 goroutine。
	renewCtx, cancelRenew := context.WithCancel(context.Background())
	d.cancelRenew = cancelRenew
	d.renewDone = scheduler.Run(renewCtx, renewInterval, jiaowuSvc.RenewFor)

	// 缓存回灌循环：Redis 不可用期间写入内存的条目，在恢复后按限速（每秒 N 条）
	// 慢慢同步回 Redis，避免恢复瞬间把 Redis 打满。无缓存时退化为空循环。
	if infra.cache != nil {
		d.flushDone = runCacheFlusher(renewCtx, infra.cache, logger)
	}

	// ── 内容域（map #43 / Spec 六类常见功能）─────────────────────
	//
	// 装配要点：
	//   - 读者视图由装配层注入：内容域不 import 身份域，只声明需要哪些事实。
	//   - 管理端挂在**独立路由组**上：全局中间件只校验会话存在与有效，
	//     这里再叠一层 admin scope 与角色门槛。两者缺一不可——
	//     scope 回答「这套令牌是不是给管理台签的」，role 回答「这个人够不够格」。
	announcementSvc := announcement.New(announcement.Options{
		Repo:  repos.Announcements,
		Clock: clock,
	})
	announcement.RegisterPublic(engine, announcementSvc, announcementViewerOf)

	// 对象存储：配了 MINIO_ENDPOINT 就用真实 MinIO，否则回落内存实现（见 buildObjectStore）。
	objectStore, objectStoreKind, err := buildObjectStore(cfg, logger)
	if err != nil {
		// 此时续期循环已经启动，直接 return 会留下悬挂的 goroutine 与未释放的 ctx。
		cancelRenew()
		if id.store != nil {
			_ = id.store.Close()
		}
		return d, err
	}

	// 帖子域（map #43 / Spec 六类常见功能）。
	//
	// 读者侧两条是**公开读**（未登录也能看列表与详情），写侧十一条需要会话。
	// 两者都挂在 engine 上——写侧的保护来自全局鉴权中间件，但每个写 handler
	// 仍会自己 fail-closed 取用户（双保险，不假设中间件一定生效）。
	//
	// 缓存只在配了 Redis 时注入；未配时帖子域按「无缓存直查」处理。
	// 必须传适配器而不是 *RedisCache：业务域只认 Get/Set/Delete。
	var postCacheOpt post.Cache
	if infra.cache != nil {
		postCacheOpt = postCache{infra.cache}
	}
	postSvc := post.New(post.Options{
		Repo:        repos.Posts,
		Clock:       clock,
		ObjectStore: objectStore,
		Cache:       postCacheOpt,
		Logger:      logger,
	})
	post.RegisterPublic(engine, postSvc, currentUserIDOf)
	post.RegisterSession(engine, postSvc, currentUserIDOf)
	logger.Info("帖子域已装配：读者侧 2 条 + 会话侧 11 条（含附件上传）")

	// 皮肤包上传 / 下载服务（issue #53 / ADR-0003、0004、0006）。
	//
	// 落点：包体 zip 存对象存储（配了 MINIO_ENDPOINT 就是 MinIO，否则内存实现），
	// 元数据与「全局单指针」存 Postgres（配了 DATABASE_URL）/ 内存实现。
	// 二者与帖子域共用同一个 objectStore 与 repos 切换口径，不另起一套存储。
	//
	// App 侧三条只读路由挂在 engine 上：免用户会话（已加入 publicReadablePaths），
	// 但仍要过 NY1 请求签名中间件——客户端首帧后拉取目录/包体时可能尚未登录，
	// 但必须证明来自持有密钥的客户端。B 端上传走 /admin/*（见下方 adminGroup）。
	skinSvc := skin.New(skin.Options{
		Repo:        repos.Skins,
		ObjectStore: objectStore,
		Clock:       clock,
		Logger:      logger,
	})
	skin.RegisterPublic(engine, skinSvc)
	logger.Info("皮肤域已装配：App 侧 3 条只读（目录/详情/包体）")

	// 用户行为埋点（map #43 / Spec 六类常见功能）。
	//
	// 三条硬约束都在业务侧兜住：信封统一、脱敏三步顺序、缓冲满不返 503。
	// 这里只负责构造与注册。
	trackSvc := track.New(track.Options{
		Repo:  repos.Track,
		Clock: clock,
	})
	track.Register(engine, trackSvc, currentUserIDOf)
	d.trackSvc = trackSvc
	logger.Info("埋点域已装配：上报端点 1 条")

	// ── 站内消息域 + 通知派生编排 ────────────────────────────────
	//
	// 三处依赖都必须注入，缺一不可：
	//   - UserExists：收件人存在性（内部信 fail-closed 的前提）；不注入则所有内部信
	//     都会被拒成「收件人不存在」——这是刻意的 fail-closed，不是 bug。
	//   - Limiter：内部信按发送者限频；复用既有 RateLimiter，口径与其它端点一致。
	//   - notify.Sink：把 message.Emit 投影给编排层，两域互不 import。
	//
	// 消息端点一律要求会话，因此只在 AuthKey 非空时挂；否则挂上去也是无人可调。
	if cfg.AuthKey != "" {
		var userExists message.UserExistsFunc
		if id.store != nil {
			userExists = messageUserExists(id.store)
		} else {
			logger.Warn("消息域收件人存在性未启用：未配置 DATABASE_URL，内部信将一律被拒")
		}
		msgSvc := message.New(message.Options{
			Repo:       repos.Messages,
			Clock:      clock,
			UserExists: userExists,
			Limiter:    messageLimiter(httpStack.limiter),
		})
		message.Register(engine, msgSvc, currentUserIDOf)

		// 通知派生编排：异步旁路，队列满/写入失败只记 dropped，绝不拖垮主业务。
		d.notifier = notify.New(notify.Options{
			Sink:     notifySink{svc: msgSvc},
			Capacity: 1024,
			Audit:    notifyAudit(auditStore, clock),
			Logger:   logger,
		})
		// 把两个源域的窄钩子接到编排层（跨域编排只在装配层发生）。
		postSvc.SetNotifyHook(postNotifyAdapter{posts: postSvc, notify: d.notifier}.Handle)
		announcementSvc.SetPublishHook(announcementNotifyAdapter{notify: d.notifier}.Handle)
		logger.Info("消息域已装配：端点 /messages* + 通知派生编排（已接帖子 4 触发点 / 公告发布）")
	} else {
		logger.Warn("消息域端点未挂载：需要 AUTH_KEY 才能要求会话")
	}

	// ── 管理端路由组 ──────────────────────────────────────────────
	//
	// 仅当身份域可用时才挂（否则 admin 会话无从校验）。
	if id.store != nil && cfg.AuthKey != "" {
		adminGroup := engine.Group("/",
			server.RevocableAuthMiddleware(httpStack.tokenV,
				server.NewSessionStoreResolver(id.store, id.store, clock),
				server.ScopeAdmin),
			server.AdminOnly(platform.RoleAdmin),
		)
		announcement.RegisterAdmin(adminGroup, announcementSvc, announcementUserIDOf)
		post.RegisterAdmin(adminGroup, postSvc)
		track.RegisterAdmin(adminGroup, trackSvc)
		// 教务公共服务取数账号 + 公共缓存失效（仅管理员）。
		jiaowu.RegisterFetchAccountAdmin(adminGroup, fetchAccountAdmin, jiaowuSvc)
		// 皮肤包 B 端上传 / 列表 / 激活（issue #53）。与管理台同一套
		// admin 会话 + role 门槛；CreatedBy 取已认证会话的用户 ID 字符串。
		skin.RegisterAdmin(adminGroup, skinSvc, func(c *gin.Context) (string, bool) {
			uid := c.GetString(server.CtxUserID)
			return uid, uid != "" && uid != "0"
		})
		// 按学号读画像：与 /admin/* 同一套门槛（admin 会话 + role>=1）。
		if id.userSvc != nil {
			identityuser.RegisterAdmin(adminGroup, id.userSvc, id.store)
		}
	} else {
		logger.Warn("管理端端点未挂载：需要身份域（DATABASE_URL）与 AuthKey 才能校验 admin 会话")
	}

	// ── 观测出口 /status ──────────────────────────────────────────
	//
	// #24 / Spec M6：观测出口 /status。与 /healthz 分开——后者只报进程存活、不探下游。
	// 探针用真实依赖的 Ping，Redis 掉线后本端点会如实报 down。
	var pgPing, redisPing func(context.Context) error
	if pgAudit != nil {
		pgPing = pgAudit.Ping
	} else if id.store != nil {
		pgPing = id.store.Ping
	}
	if infra.cache != nil {
		redisPing = infra.cache.Ping
	} else if rt, ok := infra.tickets.(*platform.RedisTicketStore); ok {
		redisPing = rt.Ping
	}
	breakerName := "none"
	if infra.cache != nil {
		breakerName = fmt.Sprint(infra.cache.BreakerState())
	}
	// storage 必须如实反映业务仓储实际用的实现，不能只看 DATABASE_URL：
	// 配了库但走内存（或反之）时，运维看 /status 会被误导。
	storageKind := string(repos.Mode)
	// 埋点缓冲计数接进 /status：dropped 是「缓冲满就丢」的静默行为的唯一观测面。
	RegisterStatus(engine, newStatusProbe(pgPing, redisPing, func() string {
		if infra.cache != nil {
			return fmt.Sprint(infra.cache.BreakerState())
		}
		return breakerName
	}, storageKind, func() (int64, int64) {
		if trackSvc == nil {
			return 0, 0
		}
		return trackSvc.Written(), trackSvc.Dropped()
	}, func() int64 {
		if postSvc == nil {
			return 0
		}
		return postSvc.CleanupFailureCount()
	}, objectStoreKind))

	return d, nil
}
