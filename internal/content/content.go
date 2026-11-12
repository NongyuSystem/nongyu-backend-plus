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
	"nongyu-backend-plus/internal/apperr"
)

// Code 是内容域使用的错误码。
//
// **它是 apperr.Code 的类型别名，不是另一套枚举**：早先 content 与 apperr
// 各定义了一份 Code / Error / Errorf / CodeOf，四者完全平行，导致
//   - 同一个业务错误在两处被判成不同类型（apperr.CodeOf 认不出 content.Error，
//     于是 400 被错报成 500）；
//   - 新增错误码要改两个地方。
//
// 现在 content 只保留「内容域会用到的码」这些常量，底层类型与构造/判定
// 一律复用 apperr，判定入口仍只有 apperr.CodeOf 一处。
type Code = apperr.Code

const (
	// CodeNotFound 目标不存在（或对当前调用方不可见，两者刻意不区分，
	// 避免用 404/403 的差异探测资源是否存在）。
	CodeNotFound = apperr.CodeNotFound
	// CodeForbidden 已认证但无权进行该操作（如非作者编辑他人帖子）。
	CodeForbidden = apperr.CodeForbidden
	// CodeValidationFailed 入参形状或取值非法。
	CodeValidationFailed = apperr.CodeValidationFailed
	// CodeInvalidCursor 分页游标无法解析。
	CodeInvalidCursor = apperr.CodeInvalidCursor
	// CodeLocked 操作因业务窗口关闭而被拒（如超过 24 小时编辑窗口）。
	CodeLocked = apperr.CodeLocked
	// CodeTooManyAttachments 附件数量超上限。
	CodeTooManyAttachments = apperr.CodeTooManyAttachments
	// CodeAttachmentTooLarge 单个附件体积超上限。
	CodeAttachmentTooLarge = apperr.CodeAttachmentTooLarge
	// CodeUnsupportedMediaType 附件格式不在白名单内。
	CodeUnsupportedMediaType = apperr.CodeUnsupportedMedia
	// CodePayloadTooLarge 请求体整体超上限。
	CodePayloadTooLarge = apperr.CodePayloadTooLarge
)

// Error / Errorf / CodeOf 直接复用 apperr，不再各写一份。
type Error = apperr.Error

// Errorf 构造一个带错误码的领域错误（复用 apperr）。
func Errorf(code Code, format string, args ...any) *Error {
	return apperr.Errorf(code, format, args...)
}

// CodeOf 从错误链里取出错误码（复用 apperr，全仓唯一判定入口）。
func CodeOf(err error) (Code, bool) {
	return apperr.CodeOf(err)
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
