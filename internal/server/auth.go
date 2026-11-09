package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
)

// 认证与限流：框架适配层把「已验证身份」写进 gin.Context，业务域只从 context 取，
// 绝不再信任请求体/query 里的 userID（ADR-0001 第 1/4 条）。
//
// 骨架期用 HMAC-SHA256 签名的自包含令牌（userID + 过期时刻），密钥来自 config；
// 生产可替换为 JWT/OIDC 验签，只要继续往 CtxUserID 写同一个 key，业务域零改动。

// CtxUserID 是认证中间件写入 gin.Context 的身份键（与业务域共享同一契约）。
const CtxUserID = school.CtxUserID

// TokenVerifier 签发并验证自包含令牌。
type TokenVerifier struct {
	key   []byte
	clock platform.Clock
}

// NewTokenVerifier 用配置密钥构造验签器。
func NewTokenVerifier(key string, clock platform.Clock) *TokenVerifier {
	return &TokenVerifier{key: []byte(key), clock: clock}
}

type tokenClaims struct {
	UserID    string `json:"sub"`
	ExpiresAt int64  `json:"exp"`
}

// Issue 签发一枚令牌（供登录成功后的鉴权服务或测试使用）。
func (v *TokenVerifier) Issue(userID string, expiresAt time.Time) string {
	claims, _ := json.Marshal(tokenClaims{UserID: userID, ExpiresAt: expiresAt.Unix()})
	payload := base64.RawURLEncoding.EncodeToString(claims)
	mac := v.sign(payload)
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac)
}

func (v *TokenVerifier) sign(payload string) []byte {
	m := hmac.New(sha256.New, v.key)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

// Verify 校验签名与过期时刻，返回 userID；任何异常都返回错误。
func (v *TokenVerifier) Verify(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", errInvalidToken
	}
	wantMAC, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(v.sign(parts[0]), wantMAC) {
		return "", errInvalidToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", errInvalidToken
	}
	var claims tokenClaims
	if err := json.Unmarshal(raw, &claims); err != nil || claims.UserID == "" {
		return "", errInvalidToken
	}
	if claims.ExpiresAt <= v.clock.Now().Unix() {
		return "", errInvalidToken
	}
	return claims.UserID, nil
}

var errInvalidToken = &authError{"invalid or expired token"}

type authError struct{ msg string }

func (e *authError) Error() string { return e.msg }

// AuthMiddleware 拒绝缺少/无效凭证的请求，并把已验证身份写入 context。
// 请求体或 query 里的 userID 一律不读取、不生效。
func AuthMiddleware(verifier *TokenVerifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 公开路径（存活探针、观测出口、登录、发验证码）不需要认证。
		// 与 RevocableAuthMiddleware 共用同一个 IsPublicPath，避免两份清单漂移（评审 M5）。
		if IsPublicPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		hdr := c.GetHeader("Authorization")
		if !strings.HasPrefix(hdr, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": "unauthorized"})
			return
		}
		userID, err := verifier.Verify(strings.TrimPrefix(hdr, "Bearer "))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": "unauthorized"})
			return
		}
		c.Set(CtxUserID, userID)
		c.Next()
	}
}

// RateLimiter 是按用户滑动窗口限流器。
type RateLimiter struct {
	mu     sync.Mutex
	clock  platform.Clock
	limit  int
	window time.Duration
	hits   map[string][]time.Time
}

// NewRateLimiter 构造限流器：每 window 内最多 limit 次（按用户独立计数）。
func NewRateLimiter(clock platform.Clock, limit int, window time.Duration) *RateLimiter {
	if limit <= 0 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	return &RateLimiter{clock: clock, limit: limit, window: window, hits: make(map[string][]time.Time)}
}

// Allow 判断该用户本次请求是否放行，并记录一次命中。
func (l *RateLimiter) Allow(userID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	cutoff := now.Add(-l.window)
	kept := l.hits[userID][:0]
	for _, t := range l.hits[userID] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[userID] = kept
		return false
	}
	l.hits[userID] = append(kept, now)
	return true
}

// RateLimitMiddleware 限流：已认证按身份计数，未认证按客户端 IP 计数。
//
// 必须挂在鉴权中间件之后。早期实现在 userID 为空时直接返回 429——
// 那会把**登录请求本身**也挡掉（登录时当然还没有身份），等于谁也进不来。
// 现在未认证请求按 IP 单独计数，既能防登录爆破，又不至于把人挡在门外。
func RateLimitMiddleware(limiter *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.URL.Path == "/healthz" {
			// 健康探针不计入限流，避免探活把配额打满。
			//
			// 这里**刻意不用 IsPublicPath**：限流豁免与「是否需要认证」是两件事。
			// 若照搬公开路径，"登录/发验证码"也会绕过限流，反而削弱防刷（评审 M5 的反面）。
			c.Next()
			return
		}
		key := c.GetString(CtxUserID)
		if key == "" {
			// 未认证：按 IP 计数，并加前缀与已认证身份区分开。
			key = "ip:" + c.ClientIP()
		}
		if !limiter.Allow(key) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"code": "rate_limited"})
			return
		}
		c.Next()
	}
}
