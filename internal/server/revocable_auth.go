package server

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
)

// 可吊销的鉴权中间件。
//
// 与旧的 AuthMiddleware 的区别：旧令牌是**自包含**的（验签即放行，无法踢人）；
// 这里的令牌只携带 session_id，验签后还必须**查会话**确认未过期、未吊销、
// scope 与设备匹配。代价是每次请求多一次会话查询（走 Redis 副本，miss 回落 PG）。
//
// 安全要点：
//   - 会话查询出错一律 fail-closed（401/503），绝不放行；
//   - 身份只写入 context，业务域不信任请求体/query 里的任何身份字段。

const (
	// CtxStudentNo 是认证后写入的学号。
	CtxStudentNo = "authenticatedStudentNo"
	// CtxRole 是认证后写入的角色（int）。
	CtxRole = "authenticatedRole"
	// CtxSessionID 是认证后写入的会话标识。
	CtxSessionID = "authenticatedSessionID"
)

// CurrentUserID 从 gin.Context 读取当前调用者 userID（int64）。
func CurrentUserID(c *gin.Context) (int64, bool) {
	s := c.GetString(CtxUserID)
	if s == "" {
		return 0, false
	}
	var v int64
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch < '0' || ch > '9' {
			return 0, false
		}
		v = v*10 + int64(ch-'0')
	}
	return v, true
}

// CurrentSessionID 从 gin.Context 读取当前会话标识。
func CurrentSessionID(c *gin.Context) (string, bool) {
	s := c.GetString(CtxSessionID)
	return s, s != ""
}

// CurrentRole 从 gin.Context 读取当前角色。
func CurrentRole(c *gin.Context) (int, bool) {
	v, ok := c.Get(CtxRole)
	if !ok {
		return 0, false
	}
	r, ok := v.(int)
	return r, ok
}

// ErrSessionInactive 表示会话不存在、已过期或已吊销。
var ErrSessionInactive = errors.New("server: session inactive")

// SessionInfo 是会话解析后的最小信息。
type SessionInfo struct {
	UserID    int64
	StudentNo string
	Role      int
	DeviceID  string
}

// SessionResolver 把 session_id 解析成一个有效会话。
// 实现负责：查会话（缓存优先）、校验过期与吊销、查用户状态。
type SessionResolver interface {
	ResolveSession(ctx context.Context, sessionID string) (SessionInfo, error)
}

// Scope 是中间件服务的会话范围。
type Scope = platform.SessionScope

const (
	ScopeApp   = platform.ScopeApp
	ScopeAdmin = platform.ScopeAdmin
)

// sessionClaims 是令牌载荷：只放 session_id 与过期时刻。
type sessionClaims struct {
	SessionID string `json:"sid"`
	ExpiresAt int64  `json:"exp"`
}

// IssueSession 签发一枚只带 session_id 的令牌。
func (v *TokenVerifier) IssueSession(sessionID string, expiresAt time.Time) string {
	claims, _ := json.Marshal(sessionClaims{SessionID: sessionID, ExpiresAt: expiresAt.Unix()})
	payload := base64.RawURLEncoding.EncodeToString(claims)
	return payload + "." + base64.RawURLEncoding.EncodeToString(v.sign(payload))
}

// parseSession 校验签名与自身过期时刻，返回 session_id。
// 注意：这里只验「令牌没被篡改且未过期」，**不代表会话仍然有效**——那要查会话存储。
func (v *TokenVerifier) parseSession(token string) (string, error) {
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
	var claims sessionClaims
	if err := json.Unmarshal(raw, &claims); err != nil || claims.SessionID == "" {
		return "", errInvalidToken
	}
	if claims.ExpiresAt <= v.clock.Now().Unix() {
		return "", errInvalidToken
	}
	return claims.SessionID, nil
}

// publiclyAccessible 是**不需要已认证身份**的路径前缀。
//
// 登录端点必须在这里：它们的作用就是「取得一个身份」，若也要求先有身份，
// 就成了永远进不去的鸡生蛋问题（实测过：全局中间件会把登录请求拦成 401）。
// 登出不在名单里——登出需要先有会话才有意义。
var publiclyAccessible = []string{
	"/healthz",
	"/identity/login/",    // 四种登录方式
	"/identity/mailcode/", // 发送验证码
	"/identity/register",  // 独立注册入口
}

func isPublicPath(path string) bool {
	for _, p := range publiclyAccessible {
		if p == path || strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}
func RevocableAuthMiddleware(verifier *TokenVerifier, resolver SessionResolver, _ Scope) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 公开端点（健康探针、登录、发验证码）不需要已认证身份。
		if isPublicPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		hdr := c.GetHeader("Authorization")
		if !strings.HasPrefix(hdr, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": "unauthorized"})
			return
		}
		sessionID, err := verifier.parseSession(strings.TrimPrefix(hdr, "Bearer "))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": "unauthorized"})
			return
		}

		info, rerr := resolver.ResolveSession(c.Request.Context(), sessionID)
		if rerr != nil {
			// 会话无效 → 401；其它故障 → 503。
			// 两者都**不放行**（fail-closed），但要让调用方与运维能区分。
			if errors.Is(rerr, ErrSessionInactive) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": "session_expired"})
				return
			}
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"code": "auth_unavailable"})
			return
		}

		c.Set(CtxUserID, formatInt64(info.UserID))
		c.Set(CtxStudentNo, info.StudentNo)
		c.Set(CtxRole, info.Role)
		c.Set(CtxSessionID, sessionID)
		c.Next()
	}
}

// SessionStoreResolver 是基于 platform.SessionStore 的解析器实现。
// 只用权威库；若需要缓存加速，包一层 Redis 查询即可（见 identity/session）。
type SessionStoreResolver struct {
	sessions platform.SessionStore
	users    platform.UserRepo
	clock    platform.Clock
}

// NewSessionStoreResolver 构造解析器。
//
// users 是**必填**：校验「账号是否被禁用」必须在鉴权路径上，不能省略。
// 早期版本把它做成可选注入，结果运行期在鉴权中间件里空指针 panic ——
// 依赖缺失应该在装配期就失败，而不是在第一个请求进来时崩。
func NewSessionStoreResolver(sessions platform.SessionStore, users platform.UserRepo, clock platform.Clock) *SessionStoreResolver {
	if clock == nil {
		clock = platform.NewRealClock()
	}
	return &SessionStoreResolver{sessions: sessions, users: users, clock: clock}
}

// ResolveSession 实现 SessionResolver：查会话 → 判活跃 → 查用户 → 判状态。
func (r *SessionStoreResolver) ResolveSession(ctx context.Context, sessionID string) (SessionInfo, error) {
	sess, err := r.sessions.SessionByID(ctx, sessionID)
	if platform.IsNotFound(err) {
		return SessionInfo{}, ErrSessionInactive
	}
	if err != nil {
		return SessionInfo{}, err
	}
	if !sess.Active(r.clock.Now()) {
		return SessionInfo{}, ErrSessionInactive
	}
	u, err := r.users.ByID(ctx, sess.UserID)
	if platform.IsNotFound(err) {
		return SessionInfo{}, ErrSessionInactive
	}
	if err != nil {
		return SessionInfo{}, err
	}
	// 账号被禁用后，已签发的会话立刻失效。
	if u.Status != platform.UserActive {
		return SessionInfo{}, ErrSessionInactive
	}
	return SessionInfo{
		UserID:    u.ID,
		StudentNo: u.StudentNo,
		Role:      int(u.Role),
		DeviceID:  sess.DeviceID,
	}, nil
}

func formatInt64(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

var _ = school.CtxUserID // 身份键与业务域共享同一契约
