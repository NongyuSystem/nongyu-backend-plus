// Package flow 是身份域的编排层：把「登录」与「签发会话」串成一个用例。
//
// 为什么单独一层（维护者选择「独立编排层」）：
//   - login 子域只负责「确认你是谁」，不该知道会话怎么签；
//   - session 子域只负责「发一张票」，不该知道是谁以什么方式登进来的；
//   - **HTTP handler 只做翻译**，不承担业务编排；
//   - 编排放在这里，可被 HTTP、未来的 CLI/定时任务复用，也可单独测。
//
// 依赖方向：flow → login、flow → session、flow → user。三者都不反向依赖 flow。
package flow

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"nongyu-backend-plus/internal/identity"
	"nongyu-backend-plus/internal/identity/login"
	"nongyu-backend-plus/internal/identity/session"
	identityuser "nongyu-backend-plus/internal/identity/user"
	"nongyu-backend-plus/internal/platform"
)

// Service 编排登录与签发。
type Service struct {
	login    *login.Service
	sessions *session.Service
	users    *identityuser.Service
	clock    platform.Clock
	// userRepo 用于登录审计（TouchLogin，issue #29）。可为 nil（单测注入时可省）。
	userRepo platform.UserRepo
	// credentialCleaner 在登出时清理该用户的学校凭据（tgt）。
	// 可为 nil：未注入时跳过，不影响登出本身。
	credentialCleaner CredentialCleaner
}

// CredentialCleaner 清理某个用户已保管的学校凭据。
//
// 为什么需要它：go-admin-tgt 是等效长期凭据（学校侧 10 天），后端加密保管 9 天。
// 用户登出后如果不清掉，凭据会一直留到自然过期——这与「退出登录即清除」的
// 约定（ADR-0001 第 3 条）不一致。由装配层注入，flow 不 import schoolauth。
type CredentialCleaner interface {
	ClearCredential(ctx context.Context, userID string) error
}

// WithCredentialCleaner 注入登出时的凭据清理能力。
func (s *Service) WithCredentialCleaner(c CredentialCleaner) *Service {
	s.credentialCleaner = c
	return s
}

// New 构造编排层。
func New(loginSvc *login.Service, sessionSvc *session.Service, users *identityuser.Service, clock platform.Clock) *Service {
	return &Service{login: loginSvc, sessions: sessionSvc, users: users, clock: clock}
}

// WithUserRepo 注入用户仓储，用于记录登录审计（IP / 设备 / 时间）。
//
// 单独一个 setter 而不是改 New 的签名：既有调用点（含测试）不必全改，
// 未注入时 TouchLogin 静默跳过（审计缺失不应阻断登录）。
func (s *Service) WithUserRepo(r platform.UserRepo) *Service {
	s.userRepo = r
	return s
}

// touchLogin 记录一次成功登录的审计三元组；失败只记日志，不影响登录结果。
func (s *Service) touchLogin(ctx context.Context, userID int64, ip, device string) {
	if s.userRepo == nil {
		return
	}
	if err := s.userRepo.TouchLogin(ctx, userID, ip, device); err != nil {
		slog.Warn("记录登录审计失败（不影响登录）", "userId", userID, "err", err.Error())
	}
}

// SignedIn 是一次成功登录的完整产物。
type SignedIn struct {
	User    platform.User
	Profile platform.Profile
	IsNew   bool
	// SessionID 由 server 层据此签发令牌。
	SessionID string
	ExpiresAt time.Time
	// ProfileIncomplete 为真表示教务源没抓到，画像缺专业/班级/年级。
	ProfileIncomplete bool
	// MirrorError 非空表示登录成功但密码镜像未写入（不阻断登录）。
	MirrorError error
	// TGTStoreError 非空表示登录成功但学校会话凭据保管失败（不阻断登录）。
	// 由调用方决定是否记日志/告警，不静默吞掉（评审 M3）。
	TGTStoreError error
}

// Params 是一次登录的全部输入。
type Params struct {
	// Method 是登录方式：school / password / mailcode / idcard
	Method string
	// DeviceID 是 App 侧设备标识（单设备登录的依据）。
	DeviceID string
	// ClientIP 是客户端 IP（来自请求头），用于登录审计。可为空。
	ClientIP string

	// school
	SchoolCred login.SchoolCredential
	// password
	StudentNo string
	Password  string
	// mailcode
	Email string
	Code  string
	// idcard
	IDCardSuffix4 string
	NameInitials  string
}

// Login 执行一次登录并在成功后签发 App 会话。
//
// 所有失败路径都返回带统一错误码的 *identity.Error，由 handle 层映射成 HTTP 状态。
func (s *Service) Login(ctx context.Context, p Params) (SignedIn, error) {
	var res login.Result
	var err error

	switch p.Method {
	case "school":
		res, err = s.login.LoginBySchool(ctx, p.SchoolCred)
	case "password":
		res, err = s.login.LoginByPassword(ctx, p.StudentNo, p.Password)
	case "mailcode":
		res, err = s.login.LoginByMailCode(ctx, p.Email, p.Code)
	case "idcard":
		res, err = s.login.LoginByIDCard(ctx, p.StudentNo, p.IDCardSuffix4, p.NameInitials)
	default:
		return SignedIn{}, identity.NewError(identity.CodeBadCredentials, "不支持的登录方式")
	}
	if err != nil {
		return SignedIn{}, err
	}
	// 非学校方式找不到用户时，login 会带回 NeedSchoolAuth 并同时返回错误；
	// 这里兜底再判一次，防止将来某个分支忘了返回错误。
	if res.NeedSchoolAuth {
		return SignedIn{}, identity.NewError(identity.CodeNeedSchoolAuth, "请先完成学校认证登录")
	}

	issued, err := s.sessions.Issue(ctx, res.User.ID, platform.ScopeApp, p.DeviceID)
	if err != nil {
		return SignedIn{}, err
	}

	// 登录审计（issue #29）：只在成功路径写一次，不挂到每个请求。
	s.touchLogin(ctx, res.User.ID, p.ClientIP, p.DeviceID)

	return SignedIn{
		User:              res.User,
		Profile:           res.Profile,
		IsNew:             res.IsNew,
		SessionID:         issued.SessionID,
		ExpiresAt:         issued.ExpiresAt,
		ProfileIncomplete: res.ProfileIncomplete,
		MirrorError:       res.MirrorError,
		TGTStoreError:     res.TGTStoreError,
	}, nil
}

// LoginAdmin 走同一套凭据校验，但签发**管理台**会话（scope=admin）。
//
// 与 App 会话互不影响：这里是维护者确认的「复用同一条农屿账密登录」，
// 只是 scope 不同，所以 App 不会被顶下线。
func (s *Service) LoginAdmin(ctx context.Context, studentNo, password string) (SignedIn, error) {
	res, err := s.login.LoginByPassword(ctx, studentNo, password)
	if err != nil {
		return SignedIn{}, err
	}
	if res.NeedSchoolAuth {
		return SignedIn{}, identity.NewError(identity.CodeNeedSchoolAuth, "请先在 App 完成学校认证登录")
	}
	// 管理台权限三档：只有 role >= 1 才能进管理台。
	if err := session.RequireRole(session.Principal{
		UserID: res.User.ID, Role: res.User.Role,
	}, identity.RoleAdmin); err != nil {
		return SignedIn{}, err
	}
	// 管理台不绑定单设备（允许换浏览器），deviceID 传固定值。
	issued, err := s.sessions.Issue(ctx, res.User.ID, platform.ScopeAdmin, "admin-web")
	if err != nil {
		return SignedIn{}, err
	}
	return SignedIn{
		User: res.User, Profile: res.Profile,
		SessionID: issued.SessionID, ExpiresAt: issued.ExpiresAt,
	}, nil
}

// Logout 吊销当前会话。
func (s *Service) Logout(ctx context.Context, sessionID string) error {
	return s.sessions.Revoke(ctx, sessionID)
}

// LogoutAll 吊销某用户在某 scope 下的全部会话（「退掉全部设备」）。
//
// 与 Logout 的区别：Logout 只按 sessionID 吊销当前这一台；LogoutAll 按
// userID + scope 清场，用于用户主动「退出所有设备」或改密后强制下线。
//
// 两者都会顺手清掉该用户保管的学校凭据（tgt）——「退出登录即清除」是 ADR-0001
// 第 3 条的硬约定，而 tgt 是等效长期凭据，留着会一直有效到自然过期。
// 清理失败只记日志，不让登出本身失败（会话已经吊销，凭据问题不应挡住用户）。
func (s *Service) LogoutAll(ctx context.Context, userID int64, scope platform.SessionScope) (int, error) {
	n, err := s.sessions.RevokeAll(ctx, userID, scope)
	if err != nil {
		return n, err
	}
	s.clearCredential(ctx, userID)
	return n, nil
}

// LogoutByUser 是「只清凭据」的那一半：供 Logout 在知道 userID 时调用。
// 单独的 Logout 只拿到 sessionID，由调用方（handler）决定是否连带清凭据。
func (s *Service) LogoutByUser(ctx context.Context, userID int64) {
	s.clearCredential(ctx, userID)
}

// clearCredential 清理用户的学校凭据。失败只记日志。
func (s *Service) clearCredential(ctx context.Context, userID int64) {
	if s.credentialCleaner == nil || userID <= 0 {
		return
	}
	if err := s.credentialCleaner.ClearCredential(ctx, strconv.FormatInt(userID, 10)); err != nil {
		slog.Warn("清理学校凭据失败（不影响登出）", "userId", userID, "err", err.Error())
	}
}

// SendMailCode 发送邮箱验证码。
func (s *Service) SendMailCode(ctx context.Context, email string) error {
	return s.login.SendMailCode(ctx, email)
}
