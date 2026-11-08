// Package session 是身份域的「会话与鉴权」子域。
//
// 职责边界：
//   - 签发会话（app / admin 两套 scope）
//   - 校验会话（未过期、未吊销、scope 匹配、App 侧 device 匹配）
//   - 吊销会话（登出、单设备顶替、管理台强制下线）
//   - role 三档判定，且**兜底必须 fail-closed**
//
// 不负责：用户是否存在、凭据是否正确、令牌怎么加密。前两者在 login / user，后者在 platform。
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"nongyu-backend-plus/internal/identity"
	"nongyu-backend-plus/internal/platform"
)

// DefaultAppTTL / DefaultAdminTTL 是两套会话的默认有效期。
// App 侧较长（学生不常重登），管理台较短（权限敏感）。
const (
	DefaultAppTTL   = 30 * 24 * time.Hour
	DefaultAdminTTL = 12 * time.Hour
)

// Service 编排会话生命周期。
type Service struct {
	sessions platform.SessionStore
	users    platform.UserRepo
	clock    platform.Clock
	appTTL   time.Duration
	adminTTL time.Duration
}

// New 构造会话子域服务。
func New(sessions platform.SessionStore, users platform.UserRepo, clock platform.Clock) *Service {
	return &Service{
		sessions: sessions,
		users:    users,
		clock:    clock,
		appTTL:   DefaultAppTTL,
		adminTTL: DefaultAdminTTL,
	}
}

// Issued 是一次签发的产物。
type Issued struct {
	SessionID string
	ExpiresAt time.Time
}

// Issue 为某用户签发一条会话。
//
// App 侧是**单设备**：签发前先吊销该用户在同一 scope 下的全部旧会话（换设备即顶替）。
// 管理台同样按 scope 独立，互不影响。
func (s *Service) Issue(ctx context.Context, userID int64, scope platform.SessionScope, deviceID string) (Issued, error) {
	u, err := s.users.ByID(ctx, userID)
	if platform.IsNotFound(err) {
		return Issued{}, identity.NewError(identity.CodeUnauthorized, "用户不存在")
	}
	if err != nil {
		return Issued{}, identity.WrapError(identity.CodeInternal, "查询用户失败", err)
	}
	// 禁用状态独立于登录方式：任何路径签发前都要过这一关。
	if u.Status != platform.UserActive {
		return Issued{}, identity.NewError(identity.CodeUserDisabled, "账号已被禁用")
	}

	// 单设备：先踢掉同 scope 的旧会话。
	if _, err := s.sessions.RevokeByUserScope(ctx, userID, scope); err != nil {
		return Issued{}, identity.WrapError(identity.CodeInternal, "吊销旧会话失败", err)
	}

	ttl := s.appTTL
	if scope == platform.ScopeAdmin {
		ttl = s.adminTTL
	}
	id, err := newSessionID()
	if err != nil {
		return Issued{}, identity.WrapError(identity.CodeInternal, "生成会话标识失败", err)
	}
	expires := s.clock.Now().Add(ttl)
	sess := platform.Session{
		ID:        id,
		UserID:    userID,
		Scope:     scope,
		DeviceID:  deviceID,
		ExpiresAt: expires,
	}
	if err := s.sessions.CreateSession(ctx, sess); err != nil {
		return Issued{}, identity.WrapError(identity.CodeInternal, "写入会话失败", err)
	}
	return Issued{SessionID: id, ExpiresAt: expires}, nil
}

// Principal 是通过校验后得到的调用者身份。
type Principal struct {
	UserID    int64
	StudentNo string
	Role      identity.Role
	Scope     platform.SessionScope
	SessionID string
}

// Authenticate 校验会话并返回调用者身份。
//
// 四项校验（缺一不可）：未过期、未吊销、scope 匹配、App 侧 device 匹配。
// 任何一项不满足都返回 CodeSessionExpired，不区分具体原因（避免给攻击者做区分探测）。
func (s *Service) Authenticate(ctx context.Context, sessionID string, scope platform.SessionScope, deviceID string) (Principal, error) {
	sess, err := s.sessions.SessionByID(ctx, sessionID)
	if platform.IsNotFound(err) {
		return Principal{}, identity.NewError(identity.CodeSessionExpired, "会话不存在")
	}
	if err != nil {
		return Principal{}, identity.WrapError(identity.CodeInternal, "读取会话失败", err)
	}
	if !sess.Active(s.clock.Now()) {
		return Principal{}, identity.NewError(identity.CodeSessionExpired, "会话已过期或已吊销")
	}
	if sess.Scope != scope {
		return Principal{}, identity.NewError(identity.CodeSessionExpired, "会话 scope 不匹配")
	}
	// App 侧单设备：device 必须与签发时一致（管理台不校验，允许换浏览器）。
	if scope == platform.ScopeApp && sess.DeviceID != deviceID {
		return Principal{}, identity.NewError(identity.CodeSessionExpired, "会话已在其他设备登录")
	}

	u, err := s.users.ByID(ctx, sess.UserID)
	if platform.IsNotFound(err) {
		return Principal{}, identity.NewError(identity.CodeSessionExpired, "会话对应用户已不存在")
	}
	if err != nil {
		return Principal{}, identity.WrapError(identity.CodeInternal, "查询用户失败", err)
	}
	// 账号被禁用后，已签发的会话也要立刻失效。
	if u.Status != platform.UserActive {
		return Principal{}, identity.NewError(identity.CodeUserDisabled, "账号已被禁用")
	}

	return Principal{
		UserID:    u.ID,
		StudentNo: u.StudentNo,
		Role:      u.Role,
		Scope:     scope,
		SessionID: sess.ID,
	}, nil
}

// Revoke 吊销一条会话（登出）。
func (s *Service) Revoke(ctx context.Context, sessionID string) error {
	if err := s.sessions.Revoke(ctx, sessionID); err != nil {
		return identity.WrapError(identity.CodeInternal, "吊销会话失败", err)
	}
	return nil
}

// RevokeAll 吊销某用户在某 scope 下的全部会话（管理台踢人、改密后清场）。
func (s *Service) RevokeAll(ctx context.Context, userID int64, scope platform.SessionScope) (int, error) {
	n, err := s.sessions.RevokeByUserScope(ctx, userID, scope)
	if err != nil {
		return 0, identity.WrapError(identity.CodeInternal, "吊销会话失败", err)
	}
	return n, nil
}

// RequireRole 判定调用者是否满足最低角色要求。
//
// **fail-closed**：角色未知、不合法、则不满足要求——绝不放行。
// 这条规则来自知识库《占位兜底实现会让权限判定静默放行》里那次真实越权事故。
func RequireRole(p Principal, min identity.Role) error {
	if !p.Role.Valid() || !min.Valid() {
		return identity.NewError(identity.CodeForbidden, "权限判定失败（角色未知）")
	}
	if p.UserID == 0 {
		return identity.NewError(identity.CodeUnauthorized, "调用者身份未知")
	}
	if p.Role < min {
		return identity.NewError(identity.CodeForbidden, "权限不足")
	}
	return nil
}

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
