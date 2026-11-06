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
	"time"

	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/server"

	"github.com/gin-gonic/gin"
)

// App 持有一个已装配完成的进程。
type App struct {
	cfg    config.Config
	engine *gin.Engine
	store  platform.Store
	server *http.Server
}

// New 完成全部装配，返回可直接 Run 的 App。
// 骨架期注入 MockStore；数据库层就绪后，只改这一行注入即可。
func New(cfg config.Config, logger *slog.Logger) (*App, error) {
	store := platform.NewMockStore()
	engine := server.New()

	a := &App{
		cfg:    cfg,
		engine: engine,
		store:  store,
		server: &http.Server{
			Addr:              cfg.Addr,
			Handler:           engine,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
	logger.Info("装配完成", "addr", cfg.Addr, "env", cfg.AppEnv, "store", "mock")
	return a, nil
}

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
