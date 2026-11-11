package app

// 安卓请求签名（NY1）在装配层的接线。
//
// 为什么单独成文件：签名有「启动前强校验 / 中间件装配 / 启动后告警」三处，
// 原先散落在 app.go 的开头、中段、结尾共 7 处，和身份/缓存/邮件装配混在一起，
// 想确认「签名到底开没开」要跳读整个 769 行的主文件。抽到这里后 app.go
// 只留两次调用：开头 checkAppSigningConfig、中段 installAppSigning。
//
// 协议本身在 internal/server/appsign.go，密钥解析在 internal/config/config.go；
// 本文件只负责把三者接起来，不重复实现任何验签逻辑。
//
// 设计要点（详见 docs/adr/0003-安卓请求签名.md）：
//   - 这层只证明「请求来自持有当前密钥的客户端」，不证明用户是谁；
//     用户身份仍由既有 HMAC 会话令牌负责，因此**不另加 JWT**。
//   - 生产/预发布缺密钥必须拒绝启动，不能静默以「接口可仿造」的形态上线。
//   - nonce 存储优先 Redis（多实例共享重放防护），无 Redis 时退化进程内存并告警。

import (
	"fmt"
	"log/slog"

	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/server"

	"github.com/gin-gonic/gin"
)

// checkAppSigningConfig 是启动前的签名配置强校验。
//
// 生产/预发布且未开 ALLOW_INSECURE_DEV 时，缺 APP_SIGNING_KEYS 直接拒绝启动：
// 此时不会挂签名中间件，所有接口可被任意客户端仿造，是「失败开放」形态。
// 开了 ALLOW_INSECURE_DEV 只放本地开发，并显式告警。
func checkAppSigningConfig(cfg config.Config, logger *slog.Logger) error {
	if !config.IsProductionLike(cfg.AppEnv) || len(cfg.AppSigningKeys) > 0 {
		return nil
	}
	if !cfg.AllowInsecureDev {
		return fmt.Errorf("app: APP_SIGNING_KEYS 为空且未开启 ALLOW_INSECURE_DEV：请求签名中间件不会挂载，接口可被任意客户端仿造；生产/预发布必须配置 APP_SIGNING_KEYS")
	}
	logger.Warn("ALLOW_INSECURE_DEV 已开启：APP_SIGNING_KEYS 为空，请求签名中间件未挂载；仅限本地开发")
	return nil
}

// installAppSigning 把签名中间件挂到路由上，返回是否真的启用。
//
// 调用方必须把它排在用户鉴权之前——登录、发验证码这些公开路径也要证明来自
// 客户端。/healthz、/status 由中间件内部豁免，给负载均衡探针留门。
//
// 密钥为空时不挂（本地开发 / 既有单测能继续用 curl 打接口）；prod 已由
// checkAppSigningConfig 拦住，走到这里说明是显式允许的本地开发。
func installAppSigning(engine *gin.Engine, cfg config.Config, cache *platform.RedisCache,
	clock platform.Clock, logger *slog.Logger) bool {

	if len(cfg.AppSigningKeys) == 0 {
		return false
	}

	var nonces server.NonceStore
	if cache != nil {
		nonces = server.NewRedisNonceStore(cache)
	} else {
		nonces = server.NewMemoryNonceStore(clock)
		logger.Warn("请求签名 nonce 使用进程内存：多实例无法共享重放防护，仅限本地开发")
	}

	engine.Use(server.AppSignMiddleware(cfg.AppSigningKeys, nonces, clock, logger))
	return true
}

// warnAppSigningDisabled 在装配结束时补一条告警：签名没启用时，接口不校验
// 客户端签名。和「装配完成」日志分开，避免和同一次 Info 里的其它能力状态混淆。
func warnAppSigningDisabled(cfg config.Config, logger *slog.Logger) {
	if len(cfg.AppSigningKeys) == 0 {
		logger.Warn("请求签名中间件未启用：未配置 APP_SIGNING_KEYS，接口不校验客户端签名，仅限本地开发/测试")
	}
}
