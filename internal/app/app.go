// Package app 是装配层（composition root）。
//
// 唯一职责：把 config / platform / server / 业务域 拼起来。
// 依赖注入只发生在这里；业务域之间不互相 import，需要协作也由本层编排。
// 这样「换数据库实现」「换框架」「拆微服务」都只动这一层。
package app

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/jiaowu"
	"nongyu-backend-plus/internal/school/presence"
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
}

// App 持有一个已装配完成的进程。
type App struct {
	cfg    config.Config
	engine *gin.Engine
	server *http.Server
	caps   Capabilities
}

// New 完成全部装配，返回可直接 Run 的 App。
// 骨架期注入内存/Mock 实现；数据库层 / 真实出站闸口就绪后，只改这里注入即可，业务域零改动。
func New(cfg config.Config, logger *slog.Logger) (*App, error) {
	store := platform.NewMockStore()
	tickets := platform.NewMockTicketStore()
	cipher := platform.NewMockCipher(cfg.TicketKey)
	doer := platform.NewMockHTTPDoer(platform.DoerPolicy{
		Enforce:        cfg.HTTPEnforce, // 出站闸口强制总开关（生产 Load() 默认 true）
		AllowedHosts:   outboundAllowlist(cfg.HTTPAllowlist),
		Timeout:        time.Duration(cfg.HTTPTimeoutSec) * time.Second,
		FollowRedirect: false, // ADR-0001：绝不跟随指向真站的 302
	})
	clock := platform.NewRealClock()
	engine := server.New()

	// 编排：把 platform 能力通过接口注入教务子域，再把子域路由注册到 gin.Engine。
	// 业务子域不 import app/server/config；装配是这一层唯一职责（架构落点草图 §8）。
	// #17 起用 NewWithAccel + RegisterAccel：加速开关从 cfg.AccelEnabled 读（默认 false），
	// 端点在生产装配里真正可达；续期调度器把观测周期回喂给 Service 的写票 TTL。
	scheduler := jiaowu.NewRenewScheduler(tickets, clock, doer)
	jiaowuSvc := jiaowu.NewWithAccel(doer, tickets, cipher, clock, logger, cfg.AccelEnabled)
	jiaowuSvc.BindRenewScheduler(scheduler)
	jiaowu.RegisterAccel(engine, jiaowuSvc)

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
		},
	}
	logger.Info("装配完成", "addr", cfg.Addr, "env", cfg.AppEnv, "store", "mock", "capabilities", 5,
		"outboundEnforce", cfg.HTTPEnforce, "accelEnabled", cfg.AccelEnabled)
	return a, nil
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

// Shutdown 在给定超时内优雅关停，等待在途请求结束。
func (a *App) Shutdown(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return a.server.Shutdown(ctx)
}
