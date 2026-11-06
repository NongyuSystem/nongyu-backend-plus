// 命令入口：nongyu-backend-plus 的唯一可执行程序。
//
// 只做三件事：读配置 → 装配 App → 启动并优雅关停。
// 业务逻辑一律不写在这里；新增业务域时只需在 internal/app 装配层接线。
package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nongyu-backend-plus/internal/app"
	"nongyu-backend-plus/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("配置加载失败", "err", err)
		os.Exit(1)
	}

	application, err := app.New(cfg, logger)
	if err != nil {
		logger.Error("装配失败", "err", err)
		os.Exit(1)
	}

	// 在独立 goroutine 启动 HTTP；主 goroutine 只等退出信号。
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("服务启动", "addr", cfg.Addr)
		if err := application.Run(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	// 等待 SIGINT / SIGTERM，或服务器自身出错。
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		logger.Info("收到退出信号，开始优雅关停", "signal", sig.String())
	case err := <-serverErr:
		logger.Error("服务异常退出", "err", err)
		os.Exit(1)
	}

	// 优雅关停：给在途请求一个结束窗口，超时则强制退出。
	shutdownTimeout := time.Duration(cfg.ShutdownTimeoutSec) * time.Second
	if err := application.Shutdown(shutdownTimeout); err != nil {
		logger.Error("优雅关停失败", "err", err)
		os.Exit(1)
	}
	logger.Info("已安全退出")
}
