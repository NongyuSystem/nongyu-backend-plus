// Package apperr 是全仓业务域共用的「带错误码的领域错误」。
//
// 为什么单独一个包（而不是每个域各写一份）：
//   - Spec 固定了全 effort 的错误码字符串（not_found / validation_failed / locked …），
//     各域若各自定义，迟早出现同一个码在不同 handler 映射成不同 HTTP 状态；
//   - handler 的「码 → 状态」映射也要只有一份判据，否则新增端点时容易漏；
//   - 这里是**纯类型**（只 import 标准库），业务域与框架层都能引，不产生反向依赖。
//
// 它刻意不含 gin / net/http：错误码到 HTTP 状态的映射是纯函数（HTTPStatus），
// 由各 handler 两三行调用即可，避免把框架依赖塞进领域层。
package apperr

import (
	"errors"
	"fmt"
)

// Code 是稳定错误码。取值固定自 docs/specs/spec-六类常见功能.md 的「错误码」节，
// 不允许在 handler 里临时拼字符串——否则客户端无法稳定分支。
type Code string

const (
	CodeUnauthorized       Code = "unauthorized"
	CodeForbidden          Code = "forbidden"
	CodeNotFound           Code = "not_found"
	CodeValidationFailed   Code = "validation_failed"
	CodeInvalidCursor      Code = "invalid_cursor"
	CodeLocked             Code = "locked"
	CodeTooManyAttachments Code = "too_many_attachments"
	CodeAttachmentTooLarge Code = "attachment_too_large"
	CodeUnsupportedMedia   Code = "unsupported_media_type"
	CodePayloadTooLarge    Code = "payload_too_large"
	CodeRecipientNotFound  Code = "recipient_not_found"
	CodeSelfSendForbidden  Code = "self_send_forbidden"
	CodeRateLimited        Code = "rate_limited"
	CodeSchemaRejected     Code = "schema_rejected"
)

// Error 是带错误码的领域错误。
type Error struct {
	Code    Code
	Message string
}

// Error 实现 error。
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return string(e.Code) + ": " + e.Message
}

// Errorf 构造一个带错误码的领域错误。
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf 从错误链里取出领域错误码。
//
// 只认 *Error；其它错误（数据库故障、上游超时等）一律返回 false，
// 由调用方映射成 5xx，绝不把内部错误包成 4xx 骗客户端。
func CodeOf(err error) (Code, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, true
	}
	return "", false
}

// HTTPStatus 把领域错误码映射为 HTTP 状态码。
//
// 收敛成一张表，避免各域 handler 各自 switch 导致同一码状态不一致。
// 未知码归 400（保守），非领域错误由调用方走 500。
func HTTPStatus(code Code) int {
	switch code {
	case CodeUnauthorized:
		return 401
	case CodeForbidden:
		return 403
	case CodeNotFound, CodeRecipientNotFound:
		return 404
	case CodeLocked:
		return 409
	case CodeAttachmentTooLarge, CodePayloadTooLarge:
		return 413
	case CodeUnsupportedMedia:
		return 415
	case CodeRateLimited:
		return 429
	case CodeValidationFailed, CodeInvalidCursor, CodeTooManyAttachments,
		CodeSelfSendForbidden, CodeSchemaRejected:
		return 400
	default:
		return 400
	}
}
