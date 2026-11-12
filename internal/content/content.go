// Package content 是「内容域」的父包。
//
// 它只放 post（帖子）与 announcement（公告）两个子域**共享**的东西：
// 统一错误码、系统错误类型、板块枚举、以及「这条内容对当前用户可见吗」
// 这类两边都要回答的判定。
//
// 为什么要有父包而不是两个平级顶层域（map #43 子票 #54 的决议）：
// 帖子与公告的可见性/受众过滤是同一套语义，若各写一套，迟早出现
// 「公告按角色过滤、帖子按另一套规则」这种漂移。父包放共享契约、
// 子域之间仍然互不 import（README 架构约束第 1 条）。
package content

import (
	"errors"
	"fmt"
)

// Code 是内容域的统一错误码。
//
// 取值固定自 docs/specs/spec-六类常见功能.md 的「错误码」节，
// 不允许在 handler 里临时拼字符串——否则客户端无法稳定分支。
type Code string

const (
	// CodeNotFound 目标不存在（或对当前调用方不可见，两者刻意不区分，
	// 避免用 404/403 的差异探测资源是否存在）。
	CodeNotFound Code = "not_found"
	// CodeForbidden 已认证但无权进行该操作（如非作者编辑他人帖子）。
	CodeForbidden Code = "forbidden"
	// CodeValidationFailed 入参形状或取值非法。
	CodeValidationFailed Code = "validation_failed"
	// CodeInvalidCursor 分页游标无法解析。
	CodeInvalidCursor Code = "invalid_cursor"
	// CodeLocked 操作因业务窗口关闭而被拒（如超过 24 小时编辑窗口）。
	CodeLocked Code = "locked"
	// CodeTooManyAttachments 附件数量超上限。
	CodeTooManyAttachments Code = "too_many_attachments"
	// CodeAttachmentTooLarge 单个附件体积超上限。
	CodeAttachmentTooLarge Code = "attachment_too_large"
	// CodeUnsupportedMediaType 附件格式不在白名单内。
	CodeUnsupportedMediaType Code = "unsupported_media_type"
	// CodePayloadTooLarge 请求体整体超上限。
	CodePayloadTooLarge Code = "payload_too_large"
)

// Error 是带错误码的领域错误。
//
// 与 school 包的 Code 做法保持一致：handler 只做码 → HTTP 状态的映射，
// 业务层不感知 HTTP。
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

// CodeOf 从错误链里取出内容域错误码。
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

// Board 是帖子的板块。
//
// 两个取值来自 map #43 子票 #49：`feedback`（反馈）与 `courtyard`（留言/院子）。
// 「广场」不是正名（见 GLOSSARY.md），代码里也不使用该词。
type Board string

const (
	// BoardFeedback 反馈板块：面向学校/后勤的问题反馈。
	BoardFeedback Board = "feedback"
	// BoardCourtyard 留言板块：轻量讨论。
	BoardCourtyard Board = "courtyard"
)

// ParseBoard 解析板块取值；空串视为未指定（返回 false），由调用方决定默认值。
func ParseBoard(s string) (Board, bool) {
	switch Board(s) {
	case BoardFeedback:
		return BoardFeedback, true
	case BoardCourtyard:
		return BoardCourtyard, true
	default:
		return "", false
	}
}

// Valid 判断板块取值是否合法。
func (b Board) Valid() bool {
	_, ok := ParseBoard(string(b))
	return ok
}
