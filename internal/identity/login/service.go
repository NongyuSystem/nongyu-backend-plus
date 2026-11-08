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
type SchoolCredential struct {
	// Method 是学校侧的认证方式：password / wechat / phone
	Method string
	// 账密方式
	StudentNo string
	Password  string
	// 短信方式
	Phone string
	Code  string
	// 微信方式
	WechatCode string
}

// SchoolIdentity 是学校确认后的结果。
// 注意：学院/专业/班级/年级**不在**这里——统一身份认证不下发这些，
// 需要另取（见 knowledge: 从川农教务 bjiben.asp 抓学籍）。
type SchoolIdentity struct {
	StudentNo string
	Name      string
	// IDCardNo 是身份证号明文，由调用方立即加密，不得留存原文。
	IDCardNo string
	// College/Major/ClassName/Grade 若认证链路顺带抓到了就填，抓不到留空。
	College   string
	Major     string
	ClassName string
	Grade     string
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
	idcardBox   Secrets // 身份证密钥
	clock       platform.Clock
	userSvc     *identityuser.Service
	mailer      Mailer
	initials    PinyinInitializer
}

// Options 是构造参数。
type Options struct {
	Users     platform.UserRepo
	Profiles  platform.ProfileRepo
	Methods   platform.LoginMethodRepo
	School    SchoolAuthenticator
	MailCodes *platform.RedisMailCodeStore
	// PasswordBox 与 IDCardBox 必须是两个**不同的** Secrets 实例（独立密钥）。
	PasswordBox Secrets
	IDCardBox   Secrets
	Clock       platform.Clock
	UserService *identityuser.Service
	// Mailer 投递邮箱验证码；SMTP 实现在配置齐全后注入。
	Mailer Mailer
	// Initials 计算姓名拼音首字母；nil 时用 DefaultInitializer（仅支持拉丁字母）。
	Initials PinyinInitializer
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
	}
}

// Result 是一次登录的结果。
type Result struct {
	User    platform.User
	Profile platform.Profile
	IsNew   bool
	// NeedSchoolAuth 为真时表示：该登录方式找不到用户，必须先完成学校认证。
	NeedSchoolAuth bool
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

	// 身份证号：立即加密，明文不留存。
	var sealedID []byte
	suffix4 := ""
	if ident.IDCardNo != "" {
		suffix4 = last4(ident.IDCardNo)
		if s.idcardBox != nil {
			sealedID, err = s.idcardBox.Seal([]byte(ident.IDCardNo))
			if err != nil {
				return Result{}, identity.WrapError(identity.CodeInternal, "加密身份证失败", err)
			}
		}
	}

	prof, err := s.userSvc.SyncSchoolProfile(ctx, u.ID, identityuser.SchoolProfile{
		Name:          ident.Name,
		College:       ident.College,
		Major:         ident.Major,
		ClassName:     ident.ClassName,
		Grade:         ident.Grade,
		IDCardSealed:  sealedID,
		IDCardSuffix4: suffix4,
	})
	if err != nil {
		return Result{}, err
	}

	// 密码镜像：只在「确实用了学校账密」时写入。其它学校方式（微信/短信）不写。
	if cred.Method == "password" && cred.Password != "" && s.passwordBox != nil {
		if err := s.mirrorPassword(ctx, u.ID, cred.Password); err != nil {
			// 镜像失败不阻断登录：学生仍可用学校认证登录。
			// 但必须记下来（由调用方写审计），所以不作为错误返回。
			_ = err
		}
	}

	// 绑定/刷新 school 登录方式（标识用学号）。
	_ = s.bindOrIgnore(ctx, u.ID, platform.LoginSchool, u.StudentNo, nil)

	return Result{User: u, Profile: prof, IsNew: isNew}, nil
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

// Mailer 是验证码的投递能力（SMTP 实现，维护者已定；发信账号上服务器时再配）。
type Mailer interface {
	SendMailCode(ctx context.Context, to, code string) error
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
