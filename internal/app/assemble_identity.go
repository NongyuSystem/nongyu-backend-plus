package app

// 本文件装配 **身份域**：学校认证适配器 → 身份持久层 → 登录/会话/用户三个子域。
//
// 这一段是全仓依赖最密集的地方，所以单独成文件：
// 它要同时摸到 学校认证（schoolauth）、平台存储（PG、Redis）、邮件、配置密钥。
//
// 顺序上有一个不可调的约束：**xsgz 取到档案后的落库回调要引用 identityStore**，
// 而 identityStore 只在 `if cfg.DatabaseURL != ""` 分支里构造——所以先声明
// `identityStore` 指针、把回调闭包挂在指针上，分支里再赋值。回调只会在真实
// 请求到达时执行，那一刻 store 早已就绪。

import (
	"context"
	"fmt"
	"log/slog"
	"nongyu-backend-plus/internal/app/bridge"
	"time"

	"nongyu-backend-plus/internal/config"
	identitylogin "nongyu-backend-plus/internal/identity/login"
	identitysession "nongyu-backend-plus/internal/identity/session"
	identityuser "nongyu-backend-plus/internal/identity/user"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/platform/mail"
	"nongyu-backend-plus/internal/school/schoolauth"
)

// identityDeps 是装配第三阶段的产物：身份域全部可注入件。
type identityDeps struct {
	store        *platform.PGIdentityStore
	sessionCache *platform.RedisSessionCache
	userSvc      *identityuser.Service
	sessionSvc   *identitysession.Service
	loginSvc     *identitylogin.Service
	tgtMgr       *schoolauth.TGTManager
	// jwSrc 是教务换票源：身份域用它换 tgt / 登出清凭据，
	// 教务域用它做续期与网关票。为 nil 表示教务链路不可用。
	jwSrc *schoolauth.JiaowuSource
}

// buildIdentityDeps 装配身份域。
//
// 未配置 DATABASE_URL 时：身份域整体不可用（没有持久化就无法承载用户与会话），
// 但**不报错**——本地开发仍要能起服务跑公开端点。此时返回的 store 为 nil，
// 调用方据此跳过依赖身份域的路由。
func buildIdentityDeps(cfg config.Config, infra infraDeps, httpStack *httpStack, repos repoBundle, logger *slog.Logger, auditStore mail.AuditStore) (identityDeps, error) {
	var d identityDeps

	// 学校认证适配器：把「客户端交上来的 go-admin-tgt」换成学校身份。
	//
	// **唯一数据源是学工系统 xsgz**（2026-11-10 维护者决定）：
	// 客户端只做一次统一登录，之后交出 go-admin-tgt（确认身份）与
	// xsgz access token（取全量档案），后端负责所有取数与入库。
	// jiaowu 的 GBK 页面解析与 ywtb 的 getUserInfo 取数**已 disable**：
	// 它们给不出身份证号、家庭住址、考生号、紧急联系人、教育经历等字段。
	var schoolAuthAdapter identitylogin.SchoolAuthenticator
	// 身份域持久层（在下方初始化；xsgz 落库回调需要引用它，故用指针提前声明）。
	// 身份域只在配了 DATABASE_URL 时启用——没有持久化就无法承载用户与会话。
	var identityStore *platform.PGIdentityStore

	// 教务源：这里只用它的「换票」能力（确认学号 + 滚动续期 tgt），
	// **不再用它抓 bjiben.asp 取专业/班级/年级**（那条路径已 disable）。
	if s, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: infra.doer}); err == nil {
		d.jwSrc = s
		// tgt 保管：换票响应会带回新 tgt，服务端加密存 9 天并滚动续期，
		// 客户端只需首次登录时交一次，之后不必反复传凭据。
		if cfg.TicketKey != "" && infra.tickets != nil {
			if mgr, err := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
				Store:  infra.tickets,
				Cipher: platform.NewAESCipher(cfg.TicketKey + ":tgt"),
				Source: s,
				Clock:  infra.clock,
			}); err == nil {
				s.WithTGT(mgr)
				d.tgtMgr = mgr
				logger.Info("已启用 tgt 保管：换票续期由服务端自动完成")
			} else {
				logger.Warn("tgt 保管未启用，每次取数需客户端提供凭据", "err", err)
			}
		}
	} else {
		logger.Warn("换票源构造失败，学校认证登录将不可用", "err", err)
	}

	// 唯一取身份源：xsgz detail 端点（13 个分组，字段最全）。
	if authSrc, err := schoolauth.NewXSGZSource(schoolauth.XSGZOptions{
		Doer:      infra.doer,
		Exchanger: exchangerOrNil(d.jwSrc),
		// 取到完整档案后落库（4 张表）。落库失败不阻断登录。
		OnDetail: func(ctx context.Context, detail schoolauth.XSGZStudentDetail) error {
			if identityStore == nil {
				return nil
			}
			pd := detail.ToProfileData()
			// 身份证号加密后落库（ADR-0002：明文绝不入库）。
			// 复用 TicketKey 作密钥（同样是「必须加密保存的敏感数据」一类），
			// 用不同派生后缀隔离用途，避免与 tgt 共用同一把实际密钥。
			var sealed []byte
			suffix := ""
			if pd.IDCardNo != "" {
				if cfg.TicketKey != "" {
					var serr error
					sealed, serr = platform.NewAESCipher(cfg.TicketKey + ":idcard").Seal([]byte(pd.IDCardNo))
					if serr != nil {
						logger.Warn("身份证加密失败，只保存后四位", "err", serr)
						sealed = nil
					}
				}
				suffix = schoolauth.Suffix4FromMasked(pd.IDCardNo)
			}
			return identityStore.SaveXSGZProfile(ctx, platform.XSGZProfileInput{
				StudentNo:     pd.StudentNo,
				Fields:        pd.Fields,
				Groups:        pd.Groups,
				FamilyMembers: pd.FamilyMembers,
				Resumes:       pd.Resumes,
				IDCardSealed:  sealed,
				IDCardSuffix4: suffix,
				Age:           pd.Age,
			})
		},
		// 取数链路各步失败都打 WARN：取档案失败不阻断登录，但绝不能静默。
		// 2026-11-13 实测踩到：Redis 里存的「tgt」其实是 302 重定向的 HTML
		// （`<a href="https://webvpn.sicau.edu.cn/login">Found</a>.`），
		// 换票必然 500 未登录，而旧代码把错误丢掉，画像空了也无从排查。
		OnError: func(err error) {
			logger.Warn("学工取数链路失败", "err", err)
		},
	}); err == nil {
		// Academic 传 nil：jiaowu 的 GBK 页面取数已 disable（#28 后续）。
		if a, err := schoolauth.New(schoolauth.Options{Auth: authSrc}); err == nil {
			schoolAuthAdapter = bridge.NewSchoolAuth(a)
		} else {
			logger.Warn("学校认证适配器构造失败，学校认证登录将不可用", "err", err)
		}
	} else {
		logger.Warn("ywtb 取数源构造失败，学校认证登录将不可用", "err", err)
	}

	// 身份域持久层：与审计共用同一个 Postgres（库 nongyu），但连接池独立。
	if cfg.DatabaseURL == "" {
		logger.Warn("身份域未启用：未配置 DATABASE_URL，用户与会话能力不可用")
		// 认证中间件仍要挂（用不可吊销的自包含令牌版本）。
		httpStack.installAuthAndRateLimit(cfg, nil, infra.clock, logger)
		return d, nil
	}

	st, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: cfg.DatabaseURL})
	if err != nil {
		return d, fmt.Errorf("身份域存储初始化失败：%w", err)
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	err = st.Ping(pingCtx)
	cancel()
	if err != nil {
		return d, fmt.Errorf("身份域存储（Postgres）不可达：%w", err)
	}
	identityStore = st
	d.store = st

	d.userSvc = identityuser.New(st, st)
	d.sessionSvc = identitysession.New(st, st, infra.clock)

	// 公告的读者画像解析器：userID → 学号 → 学院/年级。
	// 没有它，「按学院/年级定向」的公告对所有人不可见（fail-closed）。
	installProfileResolver(newProfileResolver(st, d.userSvc))

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
				d.sessionCache = rc
				d.sessionSvc.WithCache(rc)
			} else {
				logger.Warn("会话 Redis 副本不可用，鉴权将直读 Postgres", "err", err)
				_ = rc.Close()
			}
			cancel()
		}
	}

	// 登录子域：密码镜像与身份证必须用**两把独立密钥**（ADR-0002 第 2 条二修）。
	if cfg.TicketKey != "" {
		mailer, mailCodes, err := buildMailDeps(cfg, logger)
		if err != nil {
			return d, err
		}

		// schoolForTest 仅供本包测试注入假的学校认证源（默认 nil，生产行为不变）。
		// 有了它，端到端 HTTP 链路测试才能在不打真实学校网络的前提下跑通
		// 「取验证码 → 提交 → 拿令牌 → 访问受保护端点」这条链（#26 门槛 4）。
		schoolAdapter := schoolAuthAdapter
		if schoolForTest != nil {
			schoolAdapter = schoolForTest
		}
		d.loginSvc = identitylogin.New(identitylogin.Options{
			Users:       st,
			Profiles:    st,
			Methods:     st,
			MailCodes:   mailCodes,
			Mailer:      mailer,
			Clock:       infra.clock,
			UserService: d.userSvc,
			PasswordBox: platform.NewAESCipher(cfg.TicketKey + ":password"),
			IDCardBox:   platform.NewAESCipher(cfg.TicketKey + ":idcard"),
			// 学校认证：用客户端交上来的 Bearer token + cookie 走 ywtb 取数。
			School: schoolAdapter,
			// 登录成功后按用户保管 tgt；后续取学籍/续期不再需要客户端交凭据。
			TGTSaver: bridge.NewTGTSaver(d.tgtMgr),
			// 登录失败计数的跨实例存储：配了 Redis 就用外置原子计数，
			// 未配则留空退回进程内 map（单实例足够；多实例时攻击者可在
			// 实例间分摊尝试次数，见 login/lockout.go 的说明）。
			FailCounter: bridge.LoginFailCounter(infra.cache),
		})
	}

	// 认证与限流中间件必须在这里挂：它要 identityStore，而路由注册在更后面。
	httpStack.installAuthAndRateLimit(cfg, identityStore, infra.clock, logger)

	return d, nil
}

// buildMailDeps 构造邮件投递与验证码存储。
//
// 邮件投递：配了 SMTP_HOST 就用真实发信，否则回落到日志实现（仅本地调试）。
// 邮箱验证码存储需要 Redis；没有 Redis 时该登录方式不可用。
func buildMailDeps(cfg config.Config, logger *slog.Logger) (mail.Mailer, *mail.RedisMailCodeStore, error) {
	var mailer mail.Mailer
	if cfg.SMTPHost != "" {
		smtpMailer, err := mail.NewSMTPMailer(mail.MailOptions{
			Host: cfg.SMTPHost, Port: cfg.SMTPPort,
			Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
			From: cfg.SMTPFrom, Insecure: cfg.SMTPInsecure,
		}, logger)
		if err != nil {
			return nil, nil, fmt.Errorf("SMTP 发信初始化失败：%w", err)
		}
		mailer = smtpMailer
		logger.Info("邮件验证码使用 SMTP 发信", "host", cfg.SMTPHost, "port", cfg.SMTPPort)
	} else {
		mailer = &mail.LogMailer{Logger: logger}
		logger.Warn("未配置 SMTP_HOST：验证码只写日志，仅限本地开发")
	}

	var mailCodes *mail.RedisMailCodeStore
	if cfg.RedisAddr != "" {
		if mc, err := mail.NewRedisMailCodeStore(platform.RedisOptions{
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
	return mailer, mailCodes, nil
}
