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

// Board 是帖子的**类型**（也叫板块）。
//
// 语义澄清（2026-11-23 维护者确认）：board 就是「帖子类型」，
// 不存在独立的 type 维度。`teacher_review` / `course_review` 与既有的
// `feedback` / `courtyard` 是同一维度上的并列取值。
//
// 「广场」不是正名（见 GLOSSARY.md），代码里也不使用该词。
type Board string

const (
	// BoardFeedback 反馈：面向学校/后勤的问题反馈。
	BoardFeedback Board = "feedback"
	// BoardCourtyard 院子：轻量讨论。
	BoardCourtyard Board = "courtyard"

	// BoardTeacherReview 教师评价贴。
	//
	// 与下面两个是**带固定格式的类型**：发这类帖子必须填
	// TeacherName 与 CourseName（见 post.RequiresTargetFields）。
	BoardTeacherReview Board = "teacher_review"
	// BoardCourseReview 课程评价贴。同样必须填 TeacherName 与 CourseName。
	BoardCourseReview Board = "course_review"
)

// ParseBoard 解析类型取值；空串视为未指定（返回 false），由调用方决定默认值。
//
// **只做形状校验（非空、长度、字符集），不校验「是不是已知类型」。**
//
// 为什么不再枚举：类型是可以由管理员**新增**的（教师评价 → 课程评价 →
// 宿舍评价 → …）。若这里写死四个值，每加一个类型都要改代码 + 发版，
// 「数据驱动」就名不副实了。真正的合法性判据在数据库 post_types 表里
// （发帖时由外键/规则表把关），这里只挡住明显非法的输入。
func ParseBoard(s string) (Board, bool) {
	if s == "" {
		return "", false
	}
	// 类型码是「小写字母/数字/下划线」的短标识符，长度上限 32。
	// 这个约束同时保证了它不会被当成任意字符串塞进数据库。
	if len(s) > 32 {
		return "", false
	}
	for _, c := range s {
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_'
		if !ok {
			return "", false
		}
	}
	return Board(s), true
}

// Valid 判断类型取值的形状是否合法（不判断是否已登记）。
func (b Board) Valid() bool {
	_, ok := ParseBoard(string(b))
	return ok
}

// RequiresTarget 判断该类型是否**必须**带结构化字段。
//
// 这里**刻意不写死**「教师评价/课程评价要哪两个字段」：
// 那种固定写法会让每新增一个类型都要改代码。真正的校验规则在
// 数据库的 post_type_rule 表里（见 migrations 与 post/detail.go），
// 本方法只回答「这类帖子有没有专属字段」这一个问题，
// 供列表/详情决定是否要带上 detail。
func (b Board) RequiresTarget() bool {
	return b == BoardTeacherReview || b == BoardCourseReview
}
