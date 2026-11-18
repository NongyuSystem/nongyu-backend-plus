// Package app 是装配层（composition root）。
//
// 唯一职责：把 config / platform / server / 业务域 拼起来。
// 依赖注入只发生在这里；业务域之间不互相 import，需要协作也由本层编排。
// 这样「换数据库实现」「换框架」「拆微服务」都只动这一层。
//
// ── 装配层的判据（2026-11 结构整改）────────────────────────────────
//
// 本包只允许出现两类语句：
//  1. 构造具体实现（new X(...)）；
//  2. 把实现注入接口（svc.BindY(x) / Register(engine, svc)）。
//
// 不允许出现：业务判断、SQL、HTTP 报文解析、领域规则。
// 若某段代码既不是构造也不是注入，它就不属于 app——那是本包被塞垃圾的信号。
//
// 文件分工（按装配阶段拆，便于定位，改哪一块就只开哪个文件）：
//
//	app.go              进程外壳：App 类型、Capabilities、Handler/Run/Shutdown、关停顺序
//	assemble.go         New 的总流程：按阶段调用下面各文件，自身只做顺序编排
//	assemble_infra.go   外部能力：迁移、票据存储、加解密、缓存、出站 Doer、对象存储
//	assemble_http.go    HTTP 骨架：引擎、请求号、请求签名、认证与限流
//	assemble_identity.go 身份域：认证适配器、PG 存储、登录/会话/用户子域
//	assemble_domains.go 业务域路由：教务、二课、内容、消息、皮肤、埋点、管理端、观测
//	status.go           /status 观测出口
//	*_bridge.go         业务域之间的窄适配器（跨域协作只在装配层发生）
//	*_wiring.go         单域的路由注册与钩子接线
//
// 历史遗留的、不属于装配层的文件（announcement_viewer.go、post_cache.go 等）已标
// TODO，见 _artifacts/structure-lead-diagnosis.md。
package app

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"nongyu-backend-plus/internal/app/notify"
	"nongyu-backend-plus/internal/config"
	identitylogin "nongyu-backend-plus/internal/identity/login"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/schoolauth"
	"nongyu-backend-plus/internal/track"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Capabilities 是装配层注入给业务域的全部外部能力快照。
// 业务域只依赖 platform 里的接口类型，不依赖具体实现；这里集中暴露便于测试与后续编排。
//
// 关于 Store（issue #23）的收尾：早先这里有 platform.Store 字段 + NewMockStore()
// 的进程内存实现。核查确认**零消费者**——业务域从不读它，真正承载票据的是 Tickets、
// 承载用户/会话/审计的是按 DATABASE_URL 装配的 Postgres 仓储。
// 该字段在 #23 时已移除；连带把「留作骨架期备用」的 platform.Store 接口与
// MockStore 实现一起删除——按仓库依赖策略「停用即删，不留着以后可能用」，
// 长期零引用的代码留着只会误导读代码的人（让人以为还有一条基于 key-value 的存储路径）。
type Capabilities struct {
	Tickets platform.TicketStore
	Cipher  platform.Cipher
	Doer    platform.HTTPDoer
	Clock   platform.Clock
	Cache   *platform.RedisCache // 未配置 Redis 时为 nil
}

// renewInterval 是定时续期扫描的间隔（保守值，避免高频打扰学校侧）。
const renewInterval = 5 * time.Minute

// schoolForTest 是**仅供本包测试**注入假学校认证源的钩子。
//
// 为什么需要它：#26 门槛 4 要求验证一条完整 HTTP 链路
// 「POST /identity/login → 拿令牌 → 用令牌访问受保护端点 200」。
// 真实链路要打学校统一身份认证（需实时验证码、账号，且离线网络不通），
// 无法在自动化测试里复现。用这个钩子注入假源，就能把**本服务自己的编排
// 与鉴权**测完整，同时不碰任何真实学校网络。
//
// 生产路径：默认 nil，装配时完全走 schoolAuthAdapter，行为零变化。
var schoolForTest identitylogin.SchoolAuthenticator

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
	// notifier 是通知派生编排层（未挂消息域时为 nil）。
	// 关停时要 flush，否则队列里剩下的通知会丢——这是 Spec「关停前 flush」的要求。
	notifier *notify.Notifier
	// trackSvc 是埋点域；关停同样要 flush（有界缓冲里的数据不能丢）。
	trackSvc *track.Service
	// pgPool 是业务仓储的连接池（未配 DATABASE_URL 时为 nil）。
	pgPool *pgxpool.Pool
	// storeMode 如实记录四个域用的哪套实现，供 /status 报告。
	storeMode storeMode
}

// Capabilities 返回装配层注入的全部外部能力（只读快照）。
func (a *App) Capabilities() Capabilities { return a.caps }

// Handler 返回已装配好的 HTTP handler，供测试与存活探针校验。
func (a *App) Handler() http.Handler { return a.engine }

// Run 阻塞监听 HTTP 请求。
func (a *App) Run() error {
	return a.server.ListenAndServe()
}

// Shutdown 在给定超时内优雅关停。
//
// 顺序是有意的，改动前先读一遍每一步的理由：
//
//	① 停定时循环（续期 / 缓存回灌）——它们会持续产生写入；
//	② 停 HTTP 收新请求并等在途请求结束；
//	③ flush 旁路队列（通知 / 埋点）——此时队列只减不增；
//	④ 关业务仓储连接池——必须在各域 flush 之后，否则写入会撞上已关的池；
//	⑤ 关外置存储连接（Redis）。
func (a *App) Shutdown(timeout time.Duration) error {
	a.stopRenew()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := a.server.Shutdown(ctx)

	// 通知派生编排层：把队列里剩余事件写完再退出（旁路，失败只记日志）。
	if a.notifier != nil {
		a.notifier.Flush()
	}
	// 埋点有界缓冲：同样在关停时写完（Spec「关停前 flush」）。
	if a.trackSvc != nil {
		a.trackSvc.Flush()
	}
	// 业务仓储连接池：必须在各域 flush 之后再关。
	if a.pgPool != nil {
		a.pgPool.Close()
	}
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

// exchangerOrNil 把 *JiaowuSource 转成 TicketExchanger，并在为 nil 时返回**真正的 nil 接口**。
//
// 直接写 `Exchanger: jwSrc` 会有 typed-nil 陷阱：*JiaowuSource(nil) 装进接口后
// 接口值 != nil，导致 YWTBSource 里 `s.exchanger == nil` 判断失效，运行时才崩。
func exchangerOrNil(jw *schoolauth.JiaowuSource) schoolauth.TicketExchanger {
	if jw == nil {
		return nil
	}
	return jw
}
