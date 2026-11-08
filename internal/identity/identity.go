// Package identity 是身份域：登录、会话、用户。
//
// 三个子域：
//   - login：四种登录方式的解析与编排（学校认证 / 农屿账密 / 邮箱验证码 / 身份证号）
//   - session：会话签发、校验、吊销，以及 role 三档判定
//   - user：用户实体与画像（维护者口中的「用户消息」）
//
// 本父包只放跨子域共享的契约：统一错误码、Role 与状态的再导出、上下文键。
// 业务语义见 docs/tech/领域模型-用户与登录-消息.md，数据边界见 docs/adr/0002-身份仓数据边界.md。
//
// 依赖方向（硬约束）：子域之间 login→user、session→user 允许；user 不反向依赖；
// 任何子域都不得 import internal/school、internal/app、internal/server。
package identity

import (
	"errors"

	"nongyu-backend-plus/internal/platform"
)

// Role 三档权限，沿用旧农屿语义（0 普通 / 1 管理员 / 2 超级管理员）。
type Role = platform.Role

const (
	RoleUser      = platform.RoleUser
	RoleAdmin     = platform.RoleAdmin
	RoleSuperUser = platform.RoleSuperUser
)

// 统一错误码。跨子域共用一套，避免各子域自造。
type Code string

const (
	// CodeUnauthorized 未认证或凭据无效。
	CodeUnauthorized Code = "unauthorized"
	// CodeForbidden 已认证但权限不足。
	CodeForbidden Code = "forbidden"
	// CodeUserDisabled 用户被禁用。
	CodeUserDisabled Code = "user_disabled"
	// CodeSessionExpired 会话过期或已被吊销。
	CodeSessionExpired Code = "session_expired"
	// CodeBadCredentials 凭据错误（账号密码不对、验证码不对等）。
	CodeBadCredentials Code = "bad_credentials"
	// CodeNeedSchoolAuth 非学校方式的登录找不到用户，必须先完成一次学校认证。
	CodeNeedSchoolAuth Code = "need_school_auth"
	// CodeLoginMethodUnbound 该登录方式尚未绑定到任何用户。
	CodeLoginMethodUnbound Code = "login_method_unbound"
	// CodeTooManyAttempts 冷却中或尝试过多。
	CodeTooManyAttempts Code = "too_many_attempts"
	// CodeConflict 唯一约束冲突（学号已存在、登录方式已绑定）。
	CodeConflict Code = "conflict"
	// CodeInternal 内部错误。
	CodeInternal Code = "internal_error"
)

// Error 是带统一错误码的错误。
type Error struct {
	Code Code
	Msg  string
	// Cause 是底层原因，用于日志排查；绝不直接返回给调用方。
	Cause error
}

func (e *Error) Error() string {
	if e.Msg != "" {
		return string(e.Code) + ": " + e.Msg
	}
	return string(e.Code)
}

func (e *Error) Unwrap() error { return e.Cause }

// NewError 构造一个统一错误。
func NewError(code Code, msg string) *Error { return &Error{Code: code, Msg: msg} }

// WrapError 用统一错误包装底层原因。
func WrapError(code Code, msg string, cause error) *Error {
	return &Error{Code: code, Msg: msg, Cause: cause}
}

// CodeOf 从错误链里取出统一错误码；非 *Error 一律视为内部错误。
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}
