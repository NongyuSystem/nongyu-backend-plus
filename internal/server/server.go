// Package server 负责 HTTP 框架的装配：路由注册、中间件、健康检查。
// 这里是「框架适配层」——业务逻辑不应写在本包，而应放进各自的 internal/<业务域>。
package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// New 构造并返回一个已注册好基础路由的 *gin.Engine。
// 骨架阶段只挂 /healthz；业务路由由 app 装配层在拿到各自 handler 后注册。
//
// trustedProxies 是「可信反向代理」的 IP / CIDR 白名单。它决定 gin 的
// ClientIP() 是否采信 X-Forwarded-For / X-Real-IP：
//
//   - 传 nil / 空：SetTrustedProxies(nil) —— 不信任任何转发头，ClientIP()
//     回落到 TCP 直连地址 RemoteAddr。这是**安全默认**。
//   - 非空：只有来自这些地址段的请求才解析 XFF。绝不能传 0.0.0.0/0 / ::/0。
//
// 为什么必须在这里处理：gin.New() 默认 trustedProxies=0.0.0.0/0 + ::/0
// 且 ForwardedByClientIP=true，也就是**默认信任任意客户端伪造的 XFF**。
// 未认证限流（RateLimitMiddleware）以 ClientIP() 为 key、登录审计 IP 也取自
// 它，默认行为等于「限流可被无限绕过 + 审计 IP 可伪造」。此函数把它们收口成
// 一处显式配置，避免每个中间件各自重复处理。
//
// 变长参数是为兼容既有调用（server.New()）：不传即安全默认。
func New(trustedProxies ...string) *gin.Engine {
	r := gin.New()
	applyTrustedProxies(r, trustedProxies)
	// 恢复 panic + 结构化日志，是一切后续中间件的基础。
	r.Use(gin.Recovery(), gin.Logger())

	r.GET("/healthz", healthz)
	return r
}

// applyTrustedProxies 把白名单写进 gin 引擎。
//
// 单独成函数是为了让「默认不信任转发头」这件事可被单测直接断言，
// 而不必依赖 ClientIP() 的间接行为。
func applyTrustedProxies(r *gin.Engine, trustedProxies []string) {
	// 空的白名单 → nil：gin 的 isTrustedProxy 对 nil trustedCIDRs 恒 false，
	// ClientIP() 因此直接返回 RemoteAddr，XFF 完全不生效。
	if len(trustedProxies) == 0 {
		_ = r.SetTrustedProxies(nil)
		return
	}
	_ = r.SetTrustedProxies(trustedProxies)
}

// healthz 是存活探针：只报告进程可达，不检查下游依赖。
// 就绪探针（readyz）等接入数据库后再单独添加，避免下游故障导致进程被误杀。
func healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
	})
}
