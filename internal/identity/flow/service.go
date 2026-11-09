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
}

// New 构造编排层。
func New(loginSvc *login.Service, sessionSvc *session.Service, users *identityuser.Service, clock platform.Clock) *Service {
	return &Service{login: loginSvc, sessions: sessionSvc, users: users, clock: clock}
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
}

// Params 是一次登录的全部输入。
type Params struct {
	// Method 是登录方式：school / password / mailcode / idcard
	Method string
	// DeviceID 是 App 侧设备标识（单设备登录的依据）。
	DeviceID string

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

	return SignedIn{
		User:              res.User,
		Profile:           res.Profile,
		IsNew:             res.IsNew,
		SessionID:         issued.SessionID,
		ExpiresAt:         issued.ExpiresAt,
		ProfileIncomplete: res.ProfileIncomplete,
		MirrorError:       res.MirrorError,
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
func (s *Service) LogoutAll(ctx context.Context, userID int64, scope platform.SessionScope) (int, error) {
	return s.sessions.RevokeAll(ctx, userID, scope)
}

// SendMailCode 发送邮箱验证码。
func (s *Service) SendMailCode(ctx context.Context, email string) error {
	return s.login.SendMailCode(ctx, email)
}
