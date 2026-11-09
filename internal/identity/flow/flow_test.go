package flow_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/identity"
	"nongyu-backend-plus/internal/identity/flow"
	"nongyu-backend-plus/internal/identity/login"
	"nongyu-backend-plus/internal/identity/session"
	identityuser "nongyu-backend-plus/internal/identity/user"
	"nongyu-backend-plus/internal/platform"
)

const testKey = "flow-test-ticket-key-32bytes-min!!"

// 每个测试用**独立学号**，避免共用学号时某测试的清理把另一测试的数据删掉
// （实测踩过：单独跑通过、连跑失败）。
const (
	noLoginIssues  = "888100001"
	noIncomplete   = "888100002"
	noPasswordMirr = "888100003"
	noUnknown      = "888999999" // 不应存在
	noLogout       = "888100004"
	noAdminLogin   = "888100005"

	noAdminNoMirror = "888100006"
)

type fakeSchool struct {
	ident login.SchoolIdentity
	fail  bool
}

func (f *fakeSchool) Authenticate(_ context.Context, in login.SchoolCredential) (login.SchoolIdentity, error) {
	if f.fail {
		return login.SchoolIdentity{}, errors.New("学校认证失败")
	}
	// 只有账密方式才校验密码；微信/短信是另外的学校认证通道。
	if in.Method == "password" && in.Password != "school-pw" {
		return login.SchoolIdentity{}, errors.New("密码错误")
	}
	return f.ident, nil
}

type fixture struct {
	flow     *flow.Service
	sessions *session.Service
	store    *platform.PGIdentityStore
}

// setupFlow 为指定学号建一套完整依赖（真连 Postgres）。
func setupFlow(t *testing.T, ident login.SchoolIdentity) fixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过 Postgres 集成测试")
	}
	store, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsn})
	if err != nil {
		t.Fatalf("NewPGIdentityStore: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		t.Skipf("Postgres 不可达，跳过：%v", err)
	}
	// 只清理本测试自己的学号，不碰别人的。
	cleanupOne(t, dsn, ident.StudentNo)
	t.Cleanup(func() {
		cleanupOne(t, dsn, ident.StudentNo)
		_ = store.Close()
	})

	clock := platform.NewMockClock(time.Now())
	userSvc := identityuser.New(store, store)
	sessionSvc := session.New(store, store, clock)
	loginSvc := login.New(login.Options{
		Users: store, Profiles: store, Methods: store,
		School:      &fakeSchool{ident: ident},
		PasswordBox: platform.NewMockCipher(testKey),
		Clock:       clock, UserService: userSvc,
		Initials: login.DefaultInitializer{},
	})
	return fixture{
		flow:     flow.New(loginSvc, sessionSvc, userSvc, clock),
		sessions: sessionSvc,
		store:    store,
	}
}

func cleanupOne(t *testing.T, dsn, studentNo string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return
	}
	defer pool.Close()
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE student_no = $1`, studentNo)
}

// fullIdent 返回一个字段齐全的学校身份。
func fullIdent(no string) login.SchoolIdentity {
	return login.SchoolIdentity{
		StudentNo: no, Name: "Yao Yifei",
		College: "信息工程学院", Major: "数据科学",
		ClassName: "大数据202503", Grade: "2025",
		IDCardSuffix4: "2614",
	}
}

// 编排层的核心职责：登录成功后**自动签发**一条可用的会话。
func TestFlow_LoginIssuesUsableSession(t *testing.T) {
	f := setupFlow(t, fullIdent(noLoginIssues))
	ctx := context.Background()

	signed, err := f.flow.Login(ctx, flow.Params{
		Method:   "school",
		DeviceID: "dev-A",
		SchoolCred: login.SchoolCredential{
			Method: "password", StudentNo: noLoginIssues, Password: "school-pw",
		},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if signed.SessionID == "" {
		t.Fatal("登录成功必须签发会话")
	}
	if signed.User.StudentNo != noLoginIssues {
		t.Fatalf("用户不符：%+v", signed.User)
	}
	if signed.Profile.Major != "数据科学" {
		t.Fatalf("画像未同步：%+v", signed.Profile)
	}

	p, err := f.sessions.Authenticate(ctx, signed.SessionID, platform.ScopeApp, "dev-A")
	if err != nil {
		t.Fatalf("签发的会话应可鉴权：%v", err)
	}
	if p.StudentNo != noLoginIssues || p.Scope != platform.ScopeApp {
		t.Fatalf("主体不符：%+v", p)
	}
}

// 画像不完整（教务没抓到）时，登录仍成功并带回标记。
func TestFlow_ProfileIncompleteStillLogsIn(t *testing.T) {
	// 模拟教务源抓取失败：只有认证能给的字段。
	f := setupFlow(t, login.SchoolIdentity{
		StudentNo: noIncomplete, Name: "Yao Yifei",
		College:           "信息工程学院", // 认证的「所属单位」有值
		IDCardSuffix4:     "2614",
		ProfileIncomplete: true,
	})
	ctx := context.Background()

	signed, err := f.flow.Login(ctx, flow.Params{
		Method:   "school",
		DeviceID: "dev-B",
		SchoolCred: login.SchoolCredential{
			Method: "password", StudentNo: noIncomplete, Password: "school-pw",
		},
	})
	if err != nil {
		t.Fatalf("教务字段缺失时登录仍应成功，实际 %v", err)
	}
	if !signed.ProfileIncomplete {
		t.Fatal("应标记画像不完整")
	}
	if signed.SessionID == "" {
		t.Fatal("画像不完整也必须签发会话")
	}
	if signed.Profile.College != "信息工程学院" {
		t.Fatalf("学院应保留，实际 %q", signed.Profile.College)
	}
	if signed.Profile.Major != "" {
		t.Fatalf("专业应为空，实际 %q", signed.Profile.Major)
	}
}

// 用学校认证建号后，可以用农屿账密登录（密码镜像生效）。
func TestFlow_PasswordLoginAfterMirror(t *testing.T) {
	f := setupFlow(t, fullIdent(noPasswordMirr))
	ctx := context.Background()

	if _, err := f.flow.Login(ctx, flow.Params{
		Method: "school", DeviceID: "dev-C",
		SchoolCred: login.SchoolCredential{Method: "password", StudentNo: noPasswordMirr, Password: "school-pw"},
	}); err != nil {
		t.Fatalf("首次学校认证登录: %v", err)
	}

	// 前置条件必须成立：学校账密登录后镜像应已写入。
	// 先断言，再测后续登录——否则失败信息会误导成「登录逻辑有问题」，
	// 实际可能是镜像没写上（或测试数据被别人删了）。
	u, err := f.store.ByStudentNo(ctx, noPasswordMirr)
	if err != nil {
		t.Fatalf("前置：用户应已建号：%v", err)
	}
	m, err := f.store.ByUserAndKind(ctx, u.ID, platform.LoginPassword)
	if err != nil {
		t.Fatalf("前置：学校账密登录后密码镜像应已写入，实际 %v（用户 id=%d）", err, u.ID)
	}
	if len(m.SecretSealed) == 0 {
		t.Fatalf("前置：密码镜像密文不得为空（用户 id=%d）", u.ID)
	}

	signed, err := f.flow.Login(ctx, flow.Params{
		Method: "password", DeviceID: "dev-C",
		StudentNo: noPasswordMirr, Password: "school-pw",
	})
	if err != nil {
		t.Fatalf("密码镜像后应能用同一密码登录：%v", err)
	}
	if signed.SessionID == "" {
		t.Fatal("账密登录也应签发会话")
	}
}

// 未注册的账号走账密登录 → need_school_auth。
func TestFlow_UnknownUserNeedsSchoolAuth(t *testing.T) {
	f := setupFlow(t, fullIdent(noLoginIssues))
	_, err := f.flow.Login(context.Background(), flow.Params{
		Method: "password", DeviceID: "dev-D",
		StudentNo: noUnknown, Password: "whatever",
	})
	if identity.CodeOf(err) != identity.CodeNeedSchoolAuth {
		t.Fatalf("应返回 need_school_auth，实际 %v", err)
	}
}

// 登出后会话立即失效。
func TestFlow_LogoutRevokes(t *testing.T) {
	f := setupFlow(t, fullIdent(noLogout))
	ctx := context.Background()
	signed, err := f.flow.Login(ctx, flow.Params{
		Method: "school", DeviceID: "dev-E",
		SchoolCred: login.SchoolCredential{Method: "password", StudentNo: noLogout, Password: "school-pw"},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := f.flow.Logout(ctx, signed.SessionID); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := f.sessions.Authenticate(ctx, signed.SessionID, platform.ScopeApp, "dev-E"); identity.CodeOf(err) != identity.CodeSessionExpired {
		t.Fatalf("登出后应失效，实际 %v", err)
	}
}

// 管理台登录必须 role >= 1；普通用户被拒（fail-closed）。
//
// 前提：管理员必须先在 App 用学校账密登录过一次（密码镜像写入），
// 之后才能用农屿账密进管理台——这是维护者确认的口径。
func TestFlow_AdminLoginRequiresAdminRole(t *testing.T) {
	f := setupFlow(t, fullIdent(noAdminLogin))
	ctx := context.Background()
	// 用学校账密登录：既建号，也写入密码镜像。
	first, err := f.flow.Login(ctx, flow.Params{
		Method: "school", DeviceID: "dev-F",
		SchoolCred: login.SchoolCredential{Method: "password", StudentNo: noAdminLogin, Password: "school-pw"},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if first.MirrorError != nil {
		t.Fatalf("前置：密码镜像应写入成功，实际 %v", first.MirrorError)
	}

	// 普通用户即使密码镜像已写入，也不能进管理台。
	_, err = f.flow.LoginAdmin(ctx, noAdminLogin, "school-pw")
	if identity.CodeOf(err) != identity.CodeForbidden {
		t.Fatalf("普通用户进管理台应 forbidden，实际 %v", err)
	}

	u, _ := f.store.ByStudentNo(ctx, noAdminLogin)
	if err := f.store.SetRole(ctx, u.ID, platform.RoleAdmin); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	signed, err := f.flow.LoginAdmin(ctx, noAdminLogin, "school-pw")
	if err != nil {
		t.Fatalf("管理员应能登录管理台：%v", err)
	}
	if _, err := f.sessions.Authenticate(ctx, signed.SessionID, platform.ScopeAdmin, "admin-web"); err != nil {
		t.Fatalf("admin 会话应可鉴权：%v", err)
	}
}

// 从未用学校账密登录过（密码镜像为空）的管理员，进不了管理台。
// 这是维护者确认的口径：要求先在 App 登录过一次。
func TestFlow_AdminLoginRequiresPasswordMirror(t *testing.T) {
	f := setupFlow(t, fullIdent(noAdminNoMirror))
	ctx := context.Background()
	// 用微信方式完成学校认证：建号，但**不写**密码镜像。
	if _, err := f.flow.Login(ctx, flow.Params{
		Method: "school", DeviceID: "dev-G",
		SchoolCred: login.SchoolCredential{Method: "wechat", StudentNo: noAdminNoMirror, WechatCode: "wx"},
	}); err != nil {
		t.Fatalf("Login(wechat): %v", err)
	}
	u, _ := f.store.ByStudentNo(ctx, noAdminNoMirror)
	if err := f.store.SetRole(ctx, u.ID, platform.RoleAdmin); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	_, err := f.flow.LoginAdmin(ctx, noAdminNoMirror, "school-pw")
	if identity.CodeOf(err) != identity.CodeLoginMethodUnbound {
		t.Fatalf("未设置农屿密码时管理台登录应提示未绑定，实际 %v", err)
	}
}
