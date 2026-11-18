package platform

// 身份域数据模型与仓储契约：转发到零依赖的叶子包 model/。
//
// 为什么下沉：pg / redis / cache 等子包需要 User / Session 等值类型。若这些类型
// 留在 platform 根，子包 import 根、根又 import 子包就会成环。放进 model/ 后环断开，
// 而外部调用方 `platform.User` / `platform.Session` / `platform.RoleUser` 等完全不用改。
//
// 这里只有**类型别名与常量转发**，不含任何实现；实现在 model/ 与各子包里。

import "nongyu-backend-plus/internal/platform/model"

// ── 类型别名 ──
type (
	Role               = model.Role
	UserStatus         = model.UserStatus
	User               = model.User
	Profile            = model.Profile
	LoginMethodKind    = model.LoginMethodKind
	LoginMethod        = model.LoginMethod
	SessionScope       = model.SessionScope
	Session            = model.Session
	UserRepo           = model.UserRepo
	ProfileRepo        = model.ProfileRepo
	LoginMethodRepo    = model.LoginMethodRepo
	SessionStore       = model.SessionStore
	StudentBrief       = model.StudentBrief
	StudentBriefReader = model.StudentBriefReader
)

// ── 常量转发 ──
const (
	RoleUser      = model.RoleUser
	RoleAdmin     = model.RoleAdmin
	RoleSuperUser = model.RoleSuperUser

	UserDisabled = model.UserDisabled
	UserActive   = model.UserActive

	LoginSchool   = model.LoginSchool
	LoginPassword = model.LoginPassword
	LoginMailCode = model.LoginMailCode
	LoginIDCard   = model.LoginIDCard

	ScopeApp   = model.ScopeApp
	ScopeAdmin = model.ScopeAdmin
)
