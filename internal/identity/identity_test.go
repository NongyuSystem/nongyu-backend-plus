package identity_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/identity"
	"nongyu-backend-plus/internal/identity/login"
	"nongyu-backend-plus/internal/identity/session"
	identityuser "nongyu-backend-plus/internal/identity/user"
	"nongyu-backend-plus/internal/platform"
)

// 身份域的集成测试：真连 Postgres（与 Redis 不参与，邮箱码另有测试）。
// 未配置 DATABASE_URL 时跳过。

const fullIDCard = "110101200001011234"

const pwdKey = "password-box-key-for-tests-only-32b"
const idKey = "idcard-box-key-for-tests-only-32byts"

type fixtures struct {
	users      *platform.PGIdentityStore
	profiles   *platform.PGIdentityStore
	methods    *platform.PGIdentityStore
	sessions   *platform.PGIdentityStore
	clock      *platform.MockClock
	userSvc    *identityuser.Service
	sessionSvc *session.Service
	loginSvc   *login.Service
	studentNo  string
}

// 学校认证的假实现：按预置表返回身份。
type fakeSchool struct {
	byStudent map[string]login.SchoolIdentity
	fail      bool
}

func (f *fakeSchool) Authenticate(_ context.Context, in login.SchoolCredential) (login.SchoolIdentity, error) {
	if f.fail {
		return login.SchoolIdentity{}, errors.New("学校认证失败")
	}
	ident, ok := f.byStudent[in.StudentNo]
	if !ok {
		return login.SchoolIdentity{}, errors.New("学号或密码错误")
	}
	// 账密方式才校验密码，模拟真实行为。
	if in.Method == "password" && in.Password != "school-pw" {
		return login.SchoolIdentity{}, errors.New("学号或密码错误")
	}
	return ident, nil
}

// studentNoFor 由测试名派生一个 9 位学号：
// 同一测试每次运行相同（可复现），不同测试互不相同
// （-count=N 或跨包并发时不会互相删数据）。
func studentNoFor(name string) string {
	sum := sha256.Sum256([]byte("identity-test:" + name))
	n := binary.BigEndian.Uint32(sum[:4]) % 100000000
	return fmt.Sprintf("7%08d", n)
}

var _ = pgxpool.New

func setup(t *testing.T) fixtures {
	t.Helper()
	// 学号按当前测试名派生：同一测试每次运行相同（可复现），
	// 不同测试互不相同（-count=N 或跨包并发时不会互相删数据）。
	studentNo := studentNoFor(t.Name())
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

	cleanup(t, dsn, studentNo)
	t.Cleanup(func() {
		cleanup(t, dsn, studentNo)
		_ = store.Close()
	})

	clock := platform.NewMockClock(time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC))
	userSvc := identityuser.New(store, store)
	// 两把独立密钥（ADR-0002 要求）。
	pwBox := platform.NewMockCipher(pwdKey)

	school := &fakeSchool{byStudent: map[string]login.SchoolIdentity{
		studentNo: {
			StudentNo: studentNo, Name: "Yao Yifei",
			// 认证只给掩码证件号，故直接给后四位（实测：650****2614）
			IDCardSuffix4: "2614",
			IDCardFull:    fullIDCard,
			College:       "信息工程学院", Major: "数据科学与大数据技术",
			ClassName: "大数据202503", Grade: "2025",
		},
	}}

	loginSvc := login.New(login.Options{
		Users: store, Profiles: store, Methods: store,
		School:      school,
		PasswordBox: pwBox,
		IDCardBox:   platform.NewMockCipher(idKey),
		Clock:       clock, UserService: userSvc,
		Initials: login.DefaultInitializer{},
	})
	sessionSvc := session.New(store, store, clock)

	return fixtures{
		users: store, profiles: store, methods: store, sessions: store,
		clock: clock, userSvc: userSvc, sessionSvc: sessionSvc, loginSvc: loginSvc,
		studentNo: studentNo,
	}
}

func cleanup(t *testing.T, dsn, studentNo string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return
	}
	defer pool.Close()
	// 只删本测试自己的学号：按前缀删会误伤并发运行的其他测试。
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE student_no = $1`, studentNo)
}

// ── 无感注册的硬约束 ──────────────────────────────────────────

func TestLogin_SchoolCreatesUserOthersDoNot(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	// 非学校方式在用户不存在时：返回 NeedSchoolAuth，且**不建号**。
	_, err := f.loginSvc.LoginByPassword(ctx, f.studentNo, "whatever")
	if identity.CodeOf(err) != identity.CodeNeedSchoolAuth {
		t.Fatalf("农屿账密找不到用户应返回 need_school_auth，实际 %v", err)
	}
	if _, err := f.users.ByStudentNo(ctx, f.studentNo); !platform.IsNotFound(err) {
		t.Fatalf("非学校方式不得建号，实际查到了用户")
	}

	// 学校认证可以建号。
	res, err := f.loginSvc.LoginBySchool(ctx, login.SchoolCredential{
		Method: "password", StudentNo: f.studentNo, Password: "school-pw",
	})
	if err != nil {
		t.Fatalf("LoginBySchool: %v", err)
	}
	if !res.IsNew {
		t.Fatal("首次学校认证应为新建用户")
	}
	if res.Profile.Name != "Yao Yifei" || res.Profile.College != "信息工程学院" {
		t.Fatalf("画像未同步：%+v", res.Profile)
	}
	if res.Profile.IDCardSuffix4 != "2614" {
		t.Fatalf("身份证后四位应派生为 2614，实际 %q", res.Profile.IDCardSuffix4)
	}
	// ADR-0002 二修：ywtb API 返回完整号，因此**加密存储完整号**。
	// 断言两件事：密文存在，且不是明文。
	if len(res.Profile.IDCardSealed) == 0 {
		t.Fatal("完整身份证号应被加密存储（ywtb API 会返回完整号）")
	}
	if string(res.Profile.IDCardSealed) == fullIDCard {
		t.Fatal("身份证号不得明文存储")
	}
}

// ── 密码镜像 ──────────────────────────────────────────────────

func TestLogin_PasswordMirrorWrittenOnSchoolPasswordLogin(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	_, err := f.loginSvc.LoginBySchool(ctx, login.SchoolCredential{
		Method: "password", StudentNo: f.studentNo, Password: "school-pw",
	})
	if err != nil {
		t.Fatalf("LoginBySchool: %v", err)
	}

	u, _ := f.users.ByStudentNo(ctx, f.studentNo)
	m, err := f.methods.ByUserAndKind(ctx, u.ID, platform.LoginPassword)
	if err != nil {
		t.Fatalf("密码镜像应写入 login_methods：%v", err)
	}
	if len(m.SecretSealed) == 0 {
		t.Fatal("密码镜像密文不得为空")
	}
	if string(m.SecretSealed) == "school-pw" {
		t.Fatal("密码不得明文存储")
	}

	// 用镜像后的密码可以登录农屿账密。
	res, err := f.loginSvc.LoginByPassword(ctx, f.studentNo, "school-pw")
	if err != nil {
		t.Fatalf("镜像后应能用学校密码登录农屿账密：%v", err)
	}
	if res.User.StudentNo != f.studentNo {
		t.Fatalf("登录到的用户不符：%+v", res.User)
	}
	// 错误密码必须被拒。
	if _, err := f.loginSvc.LoginByPassword(ctx, f.studentNo, "wrong-pw"); identity.CodeOf(err) != identity.CodeBadCredentials {
		t.Fatalf("错误密码应 bad_credentials，实际 %v", err)
	}
}

// 未走过学校账密登录的用户，农屿账密为「未设置」，不可用。
func TestLogin_PasswordUnsetWhenNoSchoolPasswordLogin(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	// 用微信方式完成学校认证（不写密码镜像）。
	if _, err := f.loginSvc.LoginBySchool(ctx, login.SchoolCredential{
		Method: "wechat", StudentNo: f.studentNo, WechatCode: "wx-code",
	}); err != nil {
		t.Fatalf("LoginBySchool(wechat): %v", err)
	}
	// 此时农屿账密应不可用。
	_, err := f.loginSvc.LoginByPassword(ctx, f.studentNo, "school-pw")
	if identity.CodeOf(err) != identity.CodeLoginMethodUnbound {
		t.Fatalf("未设置密码时农屿账密应不可用，实际 %v", err)
	}
}

// ── 身份证号登录 ──────────────────────────────────────────────

func TestLogin_ByIDCard(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// 先把用户建出来（学校认证）。
	if _, err := f.loginSvc.LoginBySchool(ctx, login.SchoolCredential{
		Method: "password", StudentNo: f.studentNo, Password: "school-pw",
	}); err != nil {
		t.Fatalf("LoginBySchool: %v", err)
	}

	// 拉丁名 "Yao Yifei" 按词取首字母 → "YY"
	res, err := f.loginSvc.LoginByIDCard(ctx, f.studentNo, "2614", "yy")
	if err != nil {
		t.Fatalf("LoginByIDCard: %v", err)
	}
	if res.User.StudentNo != f.studentNo {
		t.Fatalf("登录用户不符：%+v", res.User)
	}

	// 后四位错 → bad_credentials
	if _, err := f.loginSvc.LoginByIDCard(ctx, f.studentNo, "9999", "yy"); identity.CodeOf(err) != identity.CodeBadCredentials {
		t.Fatalf("后四位错应 bad_credentials，实际 %v", err)
	}
	// 首字母错 → bad_credentials
	if _, err := f.loginSvc.LoginByIDCard(ctx, f.studentNo, "2614", "abc"); identity.CodeOf(err) != identity.CodeBadCredentials {
		t.Fatalf("首字母错应 bad_credentials，实际 %v", err)
	}
	// 用户不存在 → need_school_auth
	if _, err := f.loginSvc.LoginByIDCard(ctx, f.studentNo+"9", "2614", "yy"); identity.CodeOf(err) != identity.CodeNeedSchoolAuth {
		t.Fatalf("未注册学号应 need_school_auth，实际 %v", err)
	}
}

// 拼音转换器处理不了汉字时必须 fail-closed，而不是拿猜测值比对。
func TestLogin_ByIDCard_FailsClosedWhenInitialsUnavailable(t *testing.T) {
	// 直接验证转换器本身的行为：处理不了汉字时必须返回错误而不是猜。
	if _, err := (login.DefaultInitializer{}).Initials("张三"); !errors.Is(err, login.ErrNoHanSupport) {
		t.Fatalf("默认转换器遇汉字应返回 ErrNoHanSupport，实际 %v", err)
	}
	// 拉丁名按词取首字母："Yao Yifei" → "YY"
	got, err := (login.DefaultInitializer{}).Initials("Yao Yifei")
	if err != nil || got != "YY" {
		t.Fatalf("应为 YY，实际 %q err=%v", got, err)
	}

	// 注入一个不含该字的表 → 仍应拒绝（不得跳过字符）。
	mi := login.MapInitializer{Table: map[rune]string{'姚': "Y"}}
	if _, err := mi.Initials("张三"); !errors.Is(err, login.ErrNoHanSupport) {
		t.Fatalf("表外汉字应拒绝，实际 %v", err)
	}
	// 表内全覆盖 → 正常返回
	if got, err := mi.Initials("姚"); err != nil || got != "Y" {
		t.Fatalf("表内字符应得 Y，实际 %q err=%v", got, err)
	}
}

// ── 会话 ──────────────────────────────────────────────────────

func TestSession_SingleDeviceAndRevoke(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	res, err := f.loginSvc.LoginBySchool(ctx, login.SchoolCredential{
		Method: "password", StudentNo: f.studentNo, Password: "school-pw",
	})
	if err != nil {
		t.Fatalf("LoginBySchool: %v", err)
	}

	first, err := f.sessionSvc.Issue(ctx, res.User.ID, platform.ScopeApp, "device-A")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// 同设备可鉴权
	p, err := f.sessionSvc.Authenticate(ctx, first.SessionID, platform.ScopeApp, "device-A")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.StudentNo != f.studentNo {
		t.Fatalf("主体不符：%+v", p)
	}

	// 换设备登录 → 旧会话应失效（单设备顶替）
	second, err := f.sessionSvc.Issue(ctx, res.User.ID, platform.ScopeApp, "device-B")
	if err != nil {
		t.Fatalf("Issue #2: %v", err)
	}
	if _, err := f.sessionSvc.Authenticate(ctx, first.SessionID, platform.ScopeApp, "device-A"); identity.CodeOf(err) != identity.CodeSessionExpired {
		t.Fatalf("旧设备会话应失效，实际 %v", err)
	}
	// 新会话可用
	if _, err := f.sessionSvc.Authenticate(ctx, second.SessionID, platform.ScopeApp, "device-B"); err != nil {
		t.Fatalf("新会话应可用：%v", err)
	}

	// device 不匹配 → 失效
	if _, err := f.sessionSvc.Authenticate(ctx, second.SessionID, platform.ScopeApp, "device-C"); identity.CodeOf(err) != identity.CodeSessionExpired {
		t.Fatalf("device 不匹配应失效，实际 %v", err)
	}

	// scope 不匹配 → 失效
	if _, err := f.sessionSvc.Authenticate(ctx, second.SessionID, platform.ScopeAdmin, "device-B"); identity.CodeOf(err) != identity.CodeSessionExpired {
		t.Fatalf("scope 不匹配应失效，实际 %v", err)
	}

	// 登出后失效
	if err := f.sessionSvc.Revoke(ctx, second.SessionID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := f.sessionSvc.Authenticate(ctx, second.SessionID, platform.ScopeApp, "device-B"); identity.CodeOf(err) != identity.CodeSessionExpired {
		t.Fatalf("登出后应失效，实际 %v", err)
	}
}

// 账号被禁用后，已签发的会话必须立刻失效。
func TestSession_DisabledUserInvalidatesExistingSession(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	res, err := f.loginSvc.LoginBySchool(ctx, login.SchoolCredential{
		Method: "password", StudentNo: f.studentNo, Password: "school-pw",
	})
	if err != nil {
		t.Fatalf("LoginBySchool: %v", err)
	}
	issued, err := f.sessionSvc.Issue(ctx, res.User.ID, platform.ScopeApp, "device-A")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := f.sessionSvc.Authenticate(ctx, issued.SessionID, platform.ScopeApp, "device-A"); err != nil {
		t.Fatalf("前置：会话应可用：%v", err)
	}

	if err := f.users.SetStatus(ctx, res.User.ID, platform.UserDisabled); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if _, err := f.sessionSvc.Authenticate(ctx, issued.SessionID, platform.ScopeApp, "device-A"); identity.CodeOf(err) != identity.CodeUserDisabled {
		t.Fatalf("禁用后会话应失效为 user_disabled，实际 %v", err)
	}
	// 也不能再签发新会话
	if _, err := f.sessionSvc.Issue(ctx, res.User.ID, platform.ScopeApp, "device-A"); identity.CodeOf(err) != identity.CodeUserDisabled {
		t.Fatalf("禁用用户不得再签发会话，实际 %v", err)
	}
}

// ── 权限三档：fail-closed ────────────────────────────────────

func TestRequireRole_FailClosed(t *testing.T) {
	cases := []struct {
		name string
		p    session.Principal
		min  identity.Role
		ok   bool
	}{
		{"普通用户要管理员", session.Principal{UserID: 1, Role: identity.RoleUser}, identity.RoleAdmin, false},
		{"管理员要管理员", session.Principal{UserID: 1, Role: identity.RoleAdmin}, identity.RoleAdmin, true},
		{"超管要管理员", session.Principal{UserID: 1, Role: identity.RoleSuperUser}, identity.RoleAdmin, true},
		{"普通用户要普通", session.Principal{UserID: 1, Role: identity.RoleUser}, identity.RoleUser, true},
		// fail-closed：角色非法一律拒绝
		{"角色非法", session.Principal{UserID: 1, Role: identity.Role(99)}, identity.RoleUser, false},
		{"要求角色非法", session.Principal{UserID: 1, Role: identity.RoleAdmin}, identity.Role(99), false},
		// fail-closed：身份未知一律拒绝（即使角色看起来够）
		{"用户ID为0", session.Principal{UserID: 0, Role: identity.RoleSuperUser}, identity.RoleUser, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := session.RequireRole(tc.p, tc.min)
			if tc.ok && err != nil {
				t.Fatalf("应通过，实际 %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("应拒绝，实际放行（fail-open 是安全隐患）")
			}
		})
	}
}

// ── 画像字段分权 ──────────────────────────────────────────────

func TestProfileExtrasPreservedAcrossSchoolSync(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	res, err := f.loginSvc.LoginBySchool(ctx, login.SchoolCredential{
		Method: "password", StudentNo: f.studentNo, Password: "school-pw",
	})
	if err != nil {
		t.Fatalf("LoginBySchool: %v", err)
	}

	// 本人改扩展字段
	updated, err := f.userSvc.UpdateExtras(ctx, res.User.ID, identityuser.Extras{QQ: "10001", AvatarURL: "http://a/b.png"})
	if err != nil {
		t.Fatalf("UpdateExtras: %v", err)
	}
	if updated.QQ != "10001" {
		t.Fatalf("扩展字段未写入：%+v", updated)
	}

	// 再走一次学校认证（同步学校档案）→ 扩展字段必须保留
	if _, err := f.loginSvc.LoginBySchool(ctx, login.SchoolCredential{
		Method: "password", StudentNo: f.studentNo, Password: "school-pw",
	}); err != nil {
		t.Fatalf("LoginBySchool #2: %v", err)
	}
	p, err := f.userSvc.Profile(ctx, res.User.ID)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if p.QQ != "10001" || p.AvatarURL != "http://a/b.png" {
		t.Fatalf("同步学校档案不得清空扩展字段，实际 qq=%q avatar=%q", p.QQ, p.AvatarURL)
	}
}

// 窄接口只暴露最小字段集。
func TestStudentBriefReader_MinimalFields(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.loginSvc.LoginBySchool(ctx, login.SchoolCredential{
		Method: "password", StudentNo: f.studentNo, Password: "school-pw",
	}); err != nil {
		t.Fatalf("LoginBySchool: %v", err)
	}

	var reader platform.StudentBriefReader = f.userSvc
	brief, err := reader.Brief(ctx, f.studentNo)
	if err != nil {
		t.Fatalf("Brief: %v", err)
	}
	if brief.StudentNo != f.studentNo || brief.Name != "Yao Yifei" || brief.College != "信息工程学院" {
		t.Fatalf("最小字段集不符：%+v", brief)
	}
	// 不存在的学号 → ErrNotFound（而不是零值）
	if _, err := reader.Brief(ctx, f.studentNo+"9"); !platform.IsNotFound(err) {
		t.Fatalf("不存在应 ErrNotFound，实际 %v", err)
	}
}
