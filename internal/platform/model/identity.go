package model

// 本文件是**身份域数据模型与仓储契约**（原 identity.go），不含任何实现。
//
// 放在 platform 的原因：这是跨业务域共享的最小契约集合——identity 域与下游域
// （content / message 等）都要读 User / StudentBrief，而按架构约束业务域之间不互相
// import，所以模型与仓储接口下沉到 platform，实现（Postgres / Redis）在上层装配。
//
// 判读要点：
//   - 这里**只有类型与接口**，没有 SQL、没有连接池、没有业务规则；
//   - 想改「谁能登录」「能改哪个字段」，去 identity 域，不要改本文件。

import (
	"context"
	"time"
)

// Role 是三档权限，沿用旧农屿语义。
type Role int16

const (
	RoleUser      Role = 0 // 普通用户
	RoleAdmin     Role = 1 // 管理员
	RoleSuperUser Role = 2 // 超级管理员
)

// Valid 判断 role 是否是已知取值。
func (r Role) Valid() bool { return r >= RoleUser && r <= RoleSuperUser }

// UserStatus 是用户状态。
type UserStatus int16

const (
	UserDisabled UserStatus = 0
	UserActive   UserStatus = 1
)

// User 是身份域的用户实体（不含画像）。
type User struct {
	ID        int64
	StudentNo string
	Role      Role
	Status    UserStatus
	CreatedAt time.Time

	// ── 登录审计（来自客户端请求头 + 登录时刻，issue #29）──────────
	// 存在 users 而不是 profile：它们是「账号」属性，不是「画像」。
	LastLoginIP     string
	LastLoginDevice string
	// LastLoginAt 零值表示从未登录。
	// 用指针而不是 time.Time：last_login_at 在从未登录时是 SQL NULL，
	// 直接扫进 time.Time 会报 "cannot scan NULL into *time.Time"。
	LastLoginAt *time.Time
}

// Profile 是用户画像。学校档案字段与农屿扩展字段混在一行，
// 但**写入权限分开**：前者只允许服务端同步，后者只允许本人修改。
type Profile struct {
	UserID int64
	Name   string
	// IDCardSealed 是身份证号的密文；平台层不理解内容。
	IDCardSealed []byte
	// IDCardSuffix4 是派生的后四位，供登录方式校验（非明文全号）。
	IDCardSuffix4 string

	College   string
	Major     string
	ClassName string
	Grade     string

	QQ        string
	AvatarURL string

	// ── 以下字段来自教务档案页 bjiben.asp（issue #29）──────────────
	// 实测表头：考生号 | 学号 | 姓名 | 性别 | 系别 | 专业 | 学制 | 培养层次 |
	//          年级 | 班级 | 新专业 | 新班级 | 学籍状态 | 入学日期 | 民族 |
	//          政治面貌 | 家庭通讯地址 | 火车站点 | 个人电话 | 家庭电话 | 特长
	ExamNo           string // 考生号
	Gender           string // 性别
	StudyDuration    string // 学制（四年/三年…）
	EducationLevel   string // 培养层次（本科/研究生）
	NewMajor         string // 新专业
	NewClassName     string // 新班级
	EnrollmentStatus string // 学籍状态（在读/休学…）
	EnrollmentDate   string // 入学日期
	Nation           string // 民族
	PoliticalStatus  string // 政治面貌
	HomeAddress      string // 家庭通讯地址
	TrainStation     string // 火车站点
	PersonalPhone    string // 个人电话
	HomePhone        string // 家庭电话
	Specialty        string // 特长

	// ── 农屿扩展字段（本人可改，不被学校数据覆盖）──────────────────
	Nickname string // 昵称
	Email    string // 邮箱

	// ── 以下字段来自 ywtb getUserInfo（它需要 ywtb 自己的 Bearer）────
	// 生日：用于推算年龄；身份证本身加密存在 IDCardSealed。
	Birthday        string
	AvatarSourceURL string // ywtb 头像地址

	UpdatedAt time.Time
}

// LoginMethodKind 是登录方式类型。
type LoginMethodKind string

const (
	// LoginSchool 学校统一身份认证（账密 / 微信 / 电话）。唯一能直接建号的方式。
	LoginSchool LoginMethodKind = "school"
	// LoginPassword 农屿账密（密码镜像）。
	LoginPassword LoginMethodKind = "password"
	// LoginMailCode 农屿邮箱验证码。
	LoginMailCode LoginMethodKind = "mailcode"
	// LoginIDCard 学号 + 身份证后四位 + 姓名拼音首字母。
	LoginIDCard LoginMethodKind = "idcard"
)

// LoginMethod 是一条「这个用户可以用这种方式登录」的记录。
type LoginMethod struct {
	ID         int64
	UserID     int64
	Kind       LoginMethodKind
	Identifier string
	// SecretSealed 是凭据密文；为 nil 表示该方式尚未设置凭据（如农屿账密未设置）。
	SecretSealed []byte
	BoundAt      time.Time
}

// SessionScope 区分 App 与管理台两套会话。
type SessionScope string

const (
	ScopeApp   SessionScope = "app"
	ScopeAdmin SessionScope = "admin"
)

// Session 是一次登录的产物。ID 即令牌里携带的 session_id。
type Session struct {
	ID        string
	UserID    int64
	Scope     SessionScope
	DeviceID  string
	ExpiresAt time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}

// Active 判断会话当前是否可用于鉴权。
// 判定要点：未过期、未吊销。scope 与 device 的匹配由调用方按场景补充。
func (s Session) Active(now time.Time) bool {
	if s.RevokedAt != nil {
		return false
	}
	return now.Before(s.ExpiresAt)
}

// UserRepo 是用户的持久化能力。
type UserRepo interface {
	// Create 创建用户；学号已存在时返回 ErrAlreadyExists。
	Create(ctx context.Context, studentNo string, role Role) (User, error)
	// ByStudentNo 按学号查用户；不存在返回 ErrNotFound。
	ByStudentNo(ctx context.Context, studentNo string) (User, error)
	// ByID 按主键查用户；不存在返回 ErrNotFound。
	ByID(ctx context.Context, id int64) (User, error)
	// SetStatus 改用户状态。
	SetStatus(ctx context.Context, id int64, status UserStatus) error
	// TouchLogin 记录一次成功登录的审计三元组（IP / 设备 / 时间）。
	//
	// 只在**登录成功**时调用一次，不要在每个请求里写（避免写放大）。
	TouchLogin(ctx context.Context, id int64, ip, device string) error
}

// ProfileRepo 是画像的持久化能力。
type ProfileRepo interface {
	// Upsert 写入画像（整体）。业务域负责按字段分权后再调用。
	Upsert(ctx context.Context, p Profile) error
	// ByUserID 读画像；不存在返回 ErrNotFound。
	ByUserID(ctx context.Context, userID int64) (Profile, error)
}

// LoginMethodRepo 是登录方式的持久化能力。
type LoginMethodRepo interface {
	// Bind 绑定一条登录方式；(kind, identifier) 或 (user_id, kind) 冲突返回 ErrAlreadyExists。
	Bind(ctx context.Context, m LoginMethod) (LoginMethod, error)
	// ByIdentifier 按 (kind, identifier) 反查；不存在返回 ErrNotFound。
	ByIdentifier(ctx context.Context, kind LoginMethodKind, identifier string) (LoginMethod, error)
	// ByUserAndKind 按 (userID, kind) 查；不存在返回 ErrNotFound。
	ByUserAndKind(ctx context.Context, userID int64, kind LoginMethodKind) (LoginMethod, error)
	// UpdateSecret 更新凭据密文（密码镜像写入 / 清除时传 nil）。
	UpdateSecret(ctx context.Context, userID int64, kind LoginMethodKind, sealed []byte) error
	// Unbind 解绑。
	Unbind(ctx context.Context, userID int64, kind LoginMethodKind) error
}

// SessionStore 是会话的持久化能力。Postgres 为权威，实现可另加 Redis 副本。
type SessionStore interface {
	// CreateSession 写入一条会话。
	CreateSession(ctx context.Context, s Session) error
	// SessionByID 读会话；不存在返回 ErrNotFound。
	SessionByID(ctx context.Context, id string) (Session, error)
	// Revoke 吊销一条会话（幂等）。
	Revoke(ctx context.Context, id string) error
	// RevokeByUserScope 吊销某用户在某 scope 下的全部有效会话，返回受影响条数。
	// 用于「App 单设备登录顶替」与「管理台强制下线」。
	RevokeByUserScope(ctx context.Context, userID int64, scope SessionScope) (int, error)
}

// StudentBrief 是给其它业务域读取画像的**最小字段集**。
// 刻意不含身份证、QQ、头像等——最小暴露由接口形状保证，而非调用方自觉。
type StudentBrief struct {
	StudentNo string
	Name      string
	College   string
	Major     string
	ClassName string
	Grade     string
}

// StudentBriefReader 是 identity 域对外提供的窄读接口，
// 由 identity/user 实现、在装配层注册，其它业务域只 import 本包。
type StudentBriefReader interface {
	Brief(ctx context.Context, studentNo string) (StudentBrief, error)
}
