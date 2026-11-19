package app

// 本文件装配 **HTTP 骨架**：gin 引擎、可信代理、请求追踪号、请求签名、认证与限流。
//
// 中间件的挂载顺序在这里定死，且不可随意交换——每一层的注释都写了为什么必须是这个位置。
// 注意「挂载顺序」与「构造顺序」是两回事：认证中间件依赖 identityStore，
// 而 gin 的 Use 又必须先于路由注册，所以认证那一步由 assemble_identity.go 回填。

import (
	"fmt"
	"log/slog"
	"net/http"
	"nongyu-backend-plus/internal/app/httpserver"
	identitystore "nongyu-backend-plus/internal/identity/store"
	"time"

	"nongyu-backend-plus/internal/config"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/server"

	"github.com/gin-gonic/gin"
)

// httpStack 是装配第二阶段产出的 HTTP 骨架。
type httpStack struct {
	engine  *gin.Engine
	server  *http.Server
	caps    Capabilities
	tokenV  *server.TokenVerifier
	limiter *server.RateLimiter // 未启用认证时为 nil；消息域内部信限频复用同一实例
}

// buildHTTPStack 构造 gin 引擎，并挂上「不依赖身份域」的中间件。
//
// 挂载顺序（自上而下，后续中间件在后续阶段追加）：
//
//	① RequestID  —— 必须最先，后续 handler 与审计都要能拿到请求号；
//	② AppSign    —— 先于用户鉴权：登录、发验证码这些公开路径也要证明来自客户端；
//	③ Auth       —— 认证（见 installAuthAndRateLimit，需身份域就绪后调用）；
//	④ RateLimit  —— 限流紧跟在认证后，未认证请求不消耗业务配额。
//
// 注意 gin 的 Use 必须先于路由注册，所以本函数必须在 buildDomains 之前调用。
func buildHTTPStack(cfg config.Config, infra infraDeps, logger *slog.Logger) (*httpStack, error) {
	// 可信代理白名单：默认不信任任何 X-Forwarded-For（见 server.New 的长注释）。
	// 只有部署在会改写 XFF 的反向代理之后，才由 TRUSTED_PROXIES 显式给出该代理的地址段。
	trustedProxies, err := config.ParseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("装配失败：%w", err)
	}
	engine := server.New(trustedProxies...)

	// 请求追踪号：每个请求一个审计流水号，写入 context 与 X-Request-Id 响应头。
	// 必须最先挂，后续所有 handler 与审计都能拿到它。
	engine.Use(server.RequestIDMiddleware())

	// 令牌签发/验签器：登录成功后用它把 session_id 包成客户端可用的令牌。
	// AuthKey 为空时（仅本地开发）不签发令牌，登录接口会因此不可用。
	var tokenIssuer *server.TokenVerifier
	if cfg.AuthKey != "" {
		tokenIssuer = server.NewTokenVerifier(cfg.AuthKey, infra.clock)
	}

	s := &httpStack{
		engine: engine,
		server: httpserver.NewServer(cfg.Addr, engine,
			time.Duration(cfg.HTTPReadTimeoutSec)*time.Second,
			time.Duration(cfg.HTTPWriteTimeoutSec)*time.Second,
			time.Duration(cfg.HTTPIdleTimeoutSec)*time.Second),
		caps: Capabilities{
			Tickets: infra.tickets,
			Cipher:  infra.cipher,
			Doer:    infra.doer,
			Clock:   infra.clock,
			Cache:   infra.cache,
		},
		tokenV: tokenIssuer,
	}

	// 请求签名先于用户鉴权：登录、发验证码这些公开路径也要证明来自客户端。
	// 唯一免签探针是 /healthz（只报进程存活）；/status 含内部依赖状态，
	// 必须带签名 + 有效会话才能访问（见 server/appsign.go 的 IsAppSignExempt）。
	// 密钥为空时不挂（本地开发 / 既有单测）；prod 已由 httpserver.CheckAppSigningConfig 拦住。
	httpserver.InstallAppSigning(engine, cfg, infra.cache, infra.clock, logger)

	return s, nil
}

// installAuthAndRateLimit 挂认证与限流中间件。
//
// 依赖身份域：身份域可用时用**可吊销**中间件——令牌只带会话标识，验签后还要查会话，
// 因此能踢人（单设备顶替、登出、账号禁用即时生效）。
// 身份域不可用时退化为旧的自包含令牌中间件，并在日志里显式告警——
// 那种模式下吊销不生效，只适合本地开发。
//
// 由 buildIdentityDeps 在 identityStore 就绪后调用：既满足「Use 先于路由注册」，
// 又满足「认证要用到 identityStore」。
func (s *httpStack) installAuthAndRateLimit(cfg config.Config, identityStore *identitystore.PGIdentityStore, clock platform.Clock, logger *slog.Logger) {
	if cfg.AuthKey == "" {
		return
	}
	verifier := s.tokenV
	s.limiter = server.NewRateLimiter(clock, cfg.RateLimitPerMinute, time.Minute)
	if identityStore != nil {
		resolver := server.NewSessionStoreResolver(identityStore, identityStore, clock)
		s.engine.Use(server.RevocableAuthMiddleware(verifier, resolver, server.ScopeApp),
			server.RateLimitMiddleware(s.limiter))
		return
	}
	logger.Warn("使用不可吊销的自包含令牌鉴权：登出与踢人不会生效，仅限本地开发")
	s.engine.Use(server.AuthMiddleware(verifier), server.RateLimitMiddleware(s.limiter))
}
