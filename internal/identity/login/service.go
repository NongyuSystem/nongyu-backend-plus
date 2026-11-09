// Package login 是身份域的「登录」子域。
//
// 四种登录方式（见 docs/tech/领域模型-用户与登录-消息.md 第三节）：
//  1. school   学校统一身份认证（账密 / 微信 / 电话）—— **唯一能直接建号的方式**
//  2. password 农屿账密（密码镜像；未设置时不可用）
//  3. mailcode 农屿邮箱验证码
//  4. idcard   学号 + 身份证后四位 + 姓名拼音首字母
//
// 无感注册的硬约束（维护者原话）：
//
//	「学校统一身份认证是唯一注册所需的，其他方式注册时也要再走这一步。」
//
// 所以除 school 外的三种方式，在用户不存在时一律返回 CodeNeedSchoolAuth，
// 由客户端引导用户先完成一次学校认证。
package login

import (
	"context"
	"strings"
	"time"

	"nongyu-backend-plus/internal/identity"
	identityuser "nongyu-backend-plus/internal/identity/user"
	"nongyu-backend-plus/internal/platform"
)

// SchoolAuthenticator 让学校确认「这个学号是真的」。
//
// 由 school/jiaowu 实现、在装配层注入；login 子域不 import school，保持业务域隔离。
type SchoolAuthenticator interface {
	// Authenticate 用某种学校凭据确认身份，返回学号与学校档案。
	Authenticate(ctx context.Context, in SchoolCredential) (SchoolIdentity, error)
}

// SchoolCredential 是学校认证的入参。
//
// 两种形态，由 Method 区分：
//   - "cookie"：客户端已完成学校登录，把整套会话 cookie 交上来（**当前主路径**）。
//     后端不接触学生密码，只用 cookie 去取身份与学籍。
//   - "password"/"wechat"/"phone"：直接给学校凭据，由后端发起认证。
type SchoolCredential struct {
	// Method 是学校侧的认证方式：cookie / password / wechat / phone
	Method string

	// ── 形态一：客户端会话 ──
	// CookieHeader 是原始 Cookie 头（"a=1; b=2"）。
	CookieHeader string
	// Cookies 是结构化的一组 cookie；与 CookieHeader 二者给其一即可。
	Cookies []SchoolCookie
	// GoAdminTgt 是统一身份认证登录后的全局登录态 cookie。
	//
	// 这是**唯一必需**的学校凭据（实测 2026-11-09）：后端拿它换票即可取到身份与
	// 教务数据。曾有一个 ywtb `BearerToken` 字段，已按维护者指示删除——
	// 真实链路上客户端不会有它，换票响应本身就带身份（#28）。
	// GoAdminTgt 是统一身份认证的全局登录态 cookie（10 天有效）。
	// 教务换票需要它：后端用它换一张教务 service ticket，再抓 bjiben.asp 取学籍。
	// 由客户端在 auth 域登录后交来。
	GoAdminTgt string
	// UserID 是这份凭据归属的农屿用户；供服务端按用户保管 tgt。
	// 首次登录时还不知道（为零），此时只能用本次交来的凭据做一次性取数。
	UserID int64

	// ── 形态二：直接凭据 ──
	StudentNo  string
	Password   string
	Phone      string
	Code       string
	WechatCode string
}

// SchoolCookie 是一条学校会话 cookie。
type SchoolCookie struct {
	Name  string
	Value string
}

// String 实现 fmt.Stringer，**故意不输出 cookie 值**，避免被日志打印。
func (c SchoolCredential) String() string {
	return "SchoolCredential{method=" + c.Method + ", cookies=" +
		itoa(len(c.Cookies)) + ", hasHeader=" + boolStr(c.CookieHeader != "") + "}"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// SchoolIdentity 是学校确认后的结果。
//
// 数据来自**两个源**（实测确认，见知识库「川农统一身份认证的身份证号是掩码」）：
//   - 统一身份认证：学号、姓名、所属单位（= 学院）、掩码证件号（只可取后四位）
//   - 教务 bjiben.asp：专业、班级、年级（认证不下发这三项）
type SchoolIdentity struct {
	StudentNo string
	Name      string
	// College 取自认证结果的「所属单位」；没有时才回落教务抓取值。
	College string
	// IDCardSuffix4 是从证件号取出的后四位（掩码或完整号均可）。
	IDCardSuffix4 string
	// IDCardFull 是完整身份证号（ywtb API 会返回）。
	// 由本层立即加密后写入画像，明文不留存、不进日志。
	IDCardFull string

	// 以下三项只有教务源提供，抓取失败时为空。
	Major     string
	ClassName string
	Grade     string

	// ProfileIncomplete 为真表示教务源没抓到，画像缺专业/班级/年级。
	// 此时**登录照常成功**（维护者决定），由客户端提示用户，下次登录重试。
	ProfileIncomplete bool
	// MirrorError 非空表示登录成功但密码镜像没写进去（不阻断登录）。
	// 由调用方决定是否记日志/告警——静默失败会让问题无从排查。
	MirrorError error
}

// Secrets 是加解密能力（密码镜像与身份证各用一把独立密钥，由装配层注入两个实例）。
type Secrets interface {
	// Seal 加密；Open 解密。
	Seal(plaintext []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

// Service 编排登录。
type Service struct {
	users       platform.UserRepo
	profiles    platform.ProfileRepo
	methods     platform.LoginMethodRepo
	school      SchoolAuthenticator
	mailCodes   *platform.RedisMailCodeStore
	passwordBox Secrets // 密码镜像密钥
	idcardBox   Secrets // 身份证密钥（与密码密钥独立）
	clock       platform.Clock
	userSvc     *identityuser.Service
	mailer      platform.Mailer
	initials    PinyinInitializer
	// tgtSaver 可选：登录成功后把学校会话凭据按用户保管，供后续免密取学籍。
	tgtSaver TGTSaver
}

// Options 是构造参数。
type Options struct {
	Users     platform.UserRepo
	Profiles  platform.ProfileRepo
	Methods   platform.LoginMethodRepo
	School    SchoolAuthenticator
	MailCodes *platform.RedisMailCodeStore
	// PasswordBox 与 IDCardBox 必须是**两把不同密钥**（ADR-0002 第 2 条二修）。
	PasswordBox Secrets
	// IDCardBox 是身份证的加密能力；必须与 PasswordBox 用不同密钥。
	IDCardBox   Secrets
	Clock       platform.Clock
	UserService *identityuser.Service
	// Mailer 投递邮箱验证码；SMTP 实现在配置齐全后注入。
	Mailer platform.Mailer
	// Initials 计算姓名拼音首字母；nil 时用 DefaultInitializer（仅支持拉丁字母）。
	Initials PinyinInitializer
	// TGTSaver 可选：登录成功后保管学校会话凭据（tgt）。
	TGTSaver TGTSaver
}

// New 构造登录子域服务。
func New(o Options) *Service {
	initials := o.Initials
	if initials == nil {
		initials = DefaultInitializer{}
	}
	return &Service{
		users:       o.Users,
		profiles:    o.Profiles,
		methods:     o.Methods,
		school:      o.School,
		mailCodes:   o.MailCodes,
		passwordBox: o.PasswordBox,
		idcardBox:   o.IDCardBox,
		clock:       o.Clock,
		userSvc:     o.UserService,
		mailer:      o.Mailer,
		initials:    initials,
		tgtSaver:    o.TGTSaver,
	}
}

// TGTSaver 是「登录成功后把学校会话凭据保管起来」的能力。
//
// 为什么放在登录子域：只有这里同时知道「是谁」（userID）与「用了哪份学校凭据」。
// 由装配层把 schoolauth.TGTManager 适配成这个接口注入，业务域不 import school。
type TGTSaver interface {
	// Save 按用户保管整套登录态（tgt + 配套 cookie）。
	// cookieHeader 是客户端交来的整套同域 cookie：实测换票必须带全套，
	// 只存单枚 tgt 会导致续期失败（见 Spec #18 M5）。
	Save(ctx context.Context, userID int64, tgt, cookieHeader string) error
}

// Result 是一次登录的结果。
type Result struct {
	User    platform.User
	Profile platform.Profile
	IsNew   bool
	// NeedSchoolAuth 为真时表示：该登录方式找不到用户，必须先完成学校认证。
	NeedSchoolAuth bool
	// ProfileIncomplete 为真表示教务源没抓到，画像缺专业/班级/年级（登录仍成功）。
	ProfileIncomplete bool
	// MirrorError 非空表示登录成功但密码镜像没写进去（不阻断登录）。
	// 由调用方决定是否记日志/告警——静默失败会让问题无从排查。
	MirrorError error
	// TGTStoreError 非空表示登录成功但学校会话凭据保管失败（不阻断登录）。
	// 与 MirrorError 同理：不能静默吞掉，否则后续取学籍会莫名要求重交凭据。
	TGTStoreError error
}

// LoginBySchool 走学校统一身份认证登录。
//
// 这是**唯一**能创建用户的方式；成功后：
//   - 同步学校档案到画像；
//   - 把学校密码加密后写入「农屿账密」的 secret_ref（维护者决定的密码镜像）。
func (s *Service) LoginBySchool(ctx context.Context, cred SchoolCredential) (Result, error) {
	if s.school == nil {
		return Result{}, identity.NewError(identity.CodeInternal, "学校认证能力未配置")
	}
	ident, err := s.school.Authenticate(ctx, cred)
	if err != nil {
		return Result{}, identity.WrapError(identity.CodeBadCredentials, "学校认证失败", err)
	}
	if strings.TrimSpace(ident.StudentNo) == "" {
		return Result{}, identity.NewError(identity.CodeBadCredentials, "学校未返回学号")
	}

	u, isNew, err := s.userSvc.Ensure(ctx, ident.StudentNo, identity.RoleUser)
	if err != nil {
		return Result{}, err
	}
	if u.Status != platform.UserActive {
		return Result{}, identity.NewError(identity.CodeUserDisabled, "账号已被禁用")
	}

	// 身份证号：ywtb API 返回**完整号**（页面显示才是掩码）。
	// 按 ADR-0002 二修：加密存储完整号 + 仍存后四位供离线校验；
	// 明文只在内存里存在这一次，用完即弃。
	var sealedID []byte
	if ident.IDCardFull != "" && s.idcardBox != nil {
		sealedID, err = s.idcardBox.Seal([]byte(ident.IDCardFull))
		if err != nil {
			return Result{}, identity.WrapError(identity.CodeInternal, "加密身份证失败", err)
		}
	}
	prof, err := s.userSvc.SyncSchoolProfile(ctx, u.ID, identityuser.SchoolProfile{
		Name:          ident.Name,
		College:       ident.College,
		Major:         ident.Major,
		ClassName:     ident.ClassName,
		Grade:         ident.Grade,
		IDCardSealed:  sealedID,
		IDCardSuffix4: ident.IDCardSuffix4,
	})
	if err != nil {
		return Result{}, err
	}

	// 密码镜像：只在「确实用了学校账密」时写入。其它学校方式（微信/短信）不写。
	var mirrorErr error
	if cred.Method == "password" && cred.Password != "" && s.passwordBox != nil {
		// 镜像失败不阻断登录：学生仍可用学校认证登录。
		// 但错误要带出去（Result.MirrorError）——静默失败会让
		// 「管理员进不了管理台」这类问题无从排查。
		mirrorErr = s.mirrorPassword(ctx, u.ID, cred.Password)
	}

	// 绑定/刷新 school 登录方式（标识用学号）。
	_ = s.bindOrIgnore(ctx, u.ID, platform.LoginSchool, u.StudentNo, nil)

	// 保管学校会话凭据：现在才知道 userID，正好按用户存下来。
	// 失败不阻断登录，但**必须透传错误**（评审 M3）：静默吞掉会让后续取学籍
	// 莫名要求重交凭据，且日志/审计里零痕迹。
	var tgtStoreErr error
	if s.tgtSaver != nil && cred.GoAdminTgt != "" {
		tgtStoreErr = s.tgtSaver.Save(ctx, u.ID, cred.GoAdminTgt, cred.CookieHeader)
	}

	return Result{User: u, Profile: prof, IsNew: isNew, ProfileIncomplete: ident.ProfileIncomplete, MirrorError: mirrorErr, TGTStoreError: tgtStoreErr}, nil
}

// LoginByPassword 走农屿账密登录。
// 该方式**不建号**：用户不存在时返回 NeedSchoolAuth。
func (s *Service) LoginByPassword(ctx context.Context, studentNo, password string) (Result, error) {
	u, err := s.users.ByStudentNo(ctx, studentNo)
	if platform.IsNotFound(err) {
		return Result{NeedSchoolAuth: true}, identity.NewError(identity.CodeNeedSchoolAuth, "该账号尚未注册，请先完成学校认证登录")
	}
	if err != nil {
		return Result{}, identity.WrapError(identity.CodeInternal, "查询用户失败", err)
	}
	if u.Status != platform.UserActive {
		return Result{}, identity.NewError(identity.CodeUserDisabled, "账号已被禁用")
	}

	m, err := s.methods.ByUserAndKind(ctx, u.ID, platform.LoginPassword)
	if platform.IsNotFound(err) || (err == nil && len(m.SecretSealed) == 0) {
		return Result{}, identity.NewError(identity.CodeLoginMethodUnbound, "尚未设置农屿密码，请使用学校认证登录")
	}
	if err != nil {
		return Result{}, identity.WrapError(identity.CodeInternal, "查询登录方式失败", err)
	}
	plain, err := s.passwordBox.Open(m.SecretSealed)
	if err != nil {
		return Result{}, identity.WrapError(identity.CodeInternal, "解密密码失败", err)
	}
	if !constantTimeEqual(string(plain), password) {
		return Result{}, identity.NewError(identity.CodeBadCredentials, "学号或密码错误")
	}

	prof, _ := s.profiles.ByUserID(ctx, u.ID)
	return Result{User: u, Profile: prof}, nil
}

// LoginByMailCode 走邮箱验证码登录。不建号。
func (s *Service) LoginByMailCode(ctx context.Context, email, code string) (Result, error) {
	m, err := s.methods.ByIdentifier(ctx, platform.LoginMailCode, normalizeEmail(email))
	if platform.IsNotFound(err) {
		return Result{NeedSchoolAuth: true}, identity.NewError(identity.CodeNeedSchoolAuth, "该邮箱尚未绑定账号，请先完成学校认证登录")
	}
	if err != nil {
		return Result{}, identity.WrapError(identity.CodeInternal, "查询登录方式失败", err)
	}

	ok, verr := s.mailCodes.Verify(ctx, normalizeEmail(email), code)
	if verr != nil {
		return Result{}, identity.WrapError(identity.CodeInternal, "校验验证码失败", verr)
	}
	if !ok {
		return Result{}, identity.NewError(identity.CodeBadCredentials, "验证码错误或已过期")
	}

	u, err := s.users.ByID(ctx, m.UserID)
	if platform.IsNotFound(err) {
		return Result{NeedSchoolAuth: true}, identity.NewError(identity.CodeNeedSchoolAuth, "账号不存在，请先完成学校认证登录")
	}
	if err != nil {
		return Result{}, identity.WrapError(identity.CodeInternal, "查询用户失败", err)
	}
	if u.Status != platform.UserActive {
		return Result{}, identity.NewError(identity.CodeUserDisabled, "账号已被禁用")
	}
	prof, _ := s.profiles.ByUserID(ctx, u.ID)
	return Result{User: u, Profile: prof}, nil
}

// LoginByIDCard 走「学号 + 身份证后四位 + 姓名拼音首字母」登录。不建号。
func (s *Service) LoginByIDCard(ctx context.Context, studentNo, suffix4, nameInitials string) (Result, error) {
	u, err := s.users.ByStudentNo(ctx, studentNo)
	if platform.IsNotFound(err) {
		return Result{NeedSchoolAuth: true}, identity.NewError(identity.CodeNeedSchoolAuth, "该学号尚未注册，请先完成学校认证登录")
	}
	if err != nil {
		return Result{}, identity.WrapError(identity.CodeInternal, "查询用户失败", err)
	}
	if u.Status != platform.UserActive {
		return Result{}, identity.NewError(identity.CodeUserDisabled, "账号已被禁用")
	}

	p, err := s.profiles.ByUserID(ctx, u.ID)
	if platform.IsNotFound(err) {
		return Result{}, identity.NewError(identity.CodeLoginMethodUnbound, "该账号尚无可用的身份证信息，请先完成学校认证登录")
	}
	if err != nil {
		return Result{}, identity.WrapError(identity.CodeInternal, "读取画像失败", err)
	}
	// 两个因子都必须匹配；任一为空都视为不可用（fail-closed）。
	if p.IDCardSuffix4 == "" || p.Name == "" {
		return Result{}, identity.NewError(identity.CodeLoginMethodUnbound, "该账号缺少身份证信息，请先完成学校认证登录")
	}
	if !constantTimeEqual(p.IDCardSuffix4, strings.TrimSpace(suffix4)) {
		return Result{}, identity.NewError(identity.CodeBadCredentials, "学号、身份证后四位或姓名首字母错误")
	}
	// 姓名首字母：算不出来就拒绝（fail-closed），绝不用猜测值比对。
	if s.initials == nil {
		return Result{}, identity.NewError(identity.CodeLoginMethodUnbound, "该登录方式暂不可用（未配置拼音转换）")
	}
	wantInitials, err := s.initials.Initials(p.Name)
	if err != nil || wantInitials == "" {
		return Result{}, identity.NewError(identity.CodeLoginMethodUnbound, "无法为该姓名计算拼音首字母，请使用其它登录方式")
	}
	if !constantTimeEqual(wantInitials, NormalizeInitials(nameInitials)) {
		return Result{}, identity.NewError(identity.CodeBadCredentials, "学号、身份证后四位或姓名首字母错误")
	}

	_ = s.bindOrIgnore(ctx, u.ID, platform.LoginIDCard, u.StudentNo, nil)
	return Result{User: u, Profile: p}, nil
}

// SendMailCode 为一个**已绑定**的邮箱发送验证码，返回是否真的发出。
//
// 未绑定时不发送（避免匿名者用本接口当邮件轰炸器），直接返回 NeedSchoolAuth。
// 处于冷却期时拒绝发送并返回剩余时间，由调用方转成 429 语义。
func (s *Service) SendMailCode(ctx context.Context, email string) error {
	email = normalizeEmail(email)
	if _, err := s.methods.ByIdentifier(ctx, platform.LoginMailCode, email); platform.IsNotFound(err) {
		return identity.NewError(identity.CodeNeedSchoolAuth, "该邮箱尚未绑定账号")
	}

	if remain, err := s.mailCodes.Cooldown(ctx, email); err == nil && remain > 0 {
		return identity.NewError(identity.CodeTooManyAttempts, "验证码仍在有效期内，请稍后再试")
	}

	code, err := platform.GenerateMailCode(6)
	if err != nil {
		return identity.WrapError(identity.CodeInternal, "生成验证码失败", err)
	}
	if err := s.mailCodes.Issue(ctx, email, code, mailCodeTTL); err != nil {
		if platform.IsCooldown(err) {
			return identity.NewError(identity.CodeTooManyAttempts, "验证码仍在有效期内，请稍后再试")
		}
		return identity.WrapError(identity.CodeInternal, "写入验证码失败", err)
	}

	// 投递失败则把验证码作废，避免出现「用户没收到但已进入冷却期」的尴尬。
	if s.mailer == nil {
		return identity.NewError(identity.CodeInternal, "邮件投递能力未配置")
	}
	if err := s.mailer.SendMailCode(ctx, email, code); err != nil {
		_ = s.mailCodes.Clear(ctx, email)
		return identity.WrapError(identity.CodeInternal, "发送验证码失败", err)
	}
	return nil
}

// mailCodeTTL 是验证码有效期，同时也是发送冷却窗口。
const mailCodeTTL = 5 * time.Minute

// mirrorPassword 把学校密码加密后写入农屿账密的 secret_ref。
func (s *Service) mirrorPassword(ctx context.Context, userID int64, plain string) error {
	sealed, err := s.passwordBox.Seal([]byte(plain))
	if err != nil {
		return err
	}
	if err := s.methods.UpdateSecret(ctx, userID, platform.LoginPassword, sealed); err == nil {
		return nil
	}
	// 还没有 password 这条登录方式：先绑定再写。
	if _, berr := s.methods.Bind(ctx, platform.LoginMethod{
		UserID:       userID,
		Kind:         platform.LoginPassword,
		Identifier:   "",
		SecretSealed: sealed,
	}); berr != nil {
		if platform.IsAlreadyExists(berr) {
			return s.methods.UpdateSecret(ctx, userID, platform.LoginPassword, sealed)
		}
		return berr
	}
	return nil
}

func (s *Service) bindOrIgnore(ctx context.Context, userID int64, kind platform.LoginMethodKind, identifier string, sealed []byte) error {
	_, err := s.methods.Bind(ctx, platform.LoginMethod{
		UserID: userID, Kind: kind, Identifier: identifier, SecretSealed: sealed,
	})
	if err != nil && platform.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// last4 取最后四个字符。
func last4(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}

func normalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// constantTimeEqual 常量时间比较，避免计时侧信道。
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
