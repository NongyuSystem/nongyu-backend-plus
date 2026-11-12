// Package post 是帖子子域。
//
// 与 announcement 同属 content 父包下的兄弟子域，两者互不 import。
// 本包只依赖 platform（ObjectStore / Clock）、父包 content（Board 枚举）
// 与全仓共享的 apperr（错误码）；不 import server / app / identity / config，
// 也不 import 任何存储实现（实现放 internal/platform/pgstore）。
//
// 领域语义依据 docs/specs/spec-六类常见功能.md 的「帖子」节、「附件上传」节
// 与「错误码」「Testing Decisions」节。
package post

import (
	"time"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/content"
)

// Board 是帖子板块。继续复用父包枚举（type alias，不另立一套，
// 否则两个子域的板块判定会漂移）。
type Board = content.Board

const (
	BoardFeedback  = content.BoardFeedback
	BoardCourtyard = content.BoardCourtyard
)

// ParseBoard 解析板块取值。空串表示「未指定」，返回 false，由调用方决定语义
// （列表接口用零值 Board 表达「不过滤」）。
func ParseBoard(s string) (Board, bool) { return content.ParseBoard(s) }

// TargetType 是点赞/收藏的靶子类型。
type TargetType string

const (
	// TargetPost 靶子是一条帖子。
	TargetPost TargetType = "post"
	// TargetComment 靶子是一条留言。Spec 的「点赞/收藏」统一表里叫 comment，
	// 与回复端点路径里的 replies 不是同一个词，但指的是同一类靶子。
	TargetComment TargetType = "comment"
)

// ParseTargetType 解析靶子类型。
func ParseTargetType(s string) (TargetType, bool) {
	switch TargetType(s) {
	case TargetPost:
		return TargetPost, true
	case TargetComment:
		return TargetComment, true
	default:
		return "", false
	}
}

// ReactionKind 是一种反应。同一用户 + 同一靶子 + 同一种反应唯一（取消即删行）。
type ReactionKind string

const (
	ReactionLike     ReactionKind = "like"
	ReactionFavorite ReactionKind = "favorite"
)

// ParseReactionKind 解析反应类型。
func ParseReactionKind(s string) (ReactionKind, bool) {
	switch ReactionKind(s) {
	case ReactionLike:
		return ReactionLike, true
	case ReactionFavorite:
		return ReactionFavorite, true
	default:
		return "", false
	}
}

// 分页、字段与业务窗口上限。
const (
	DefaultPageSize = 20
	MaxPageSize     = 100

	MaxTitleLen     = 200
	MaxBodyLen      = 20000
	MaxReplyBodyLen = 5000

	// MaxAttachments 是单条帖子允许携带的附件对象 key 数量上限。
	MaxAttachments = 9
	// MaxAttachmentBytes 是单个附件 / 整个上传请求体的体积上限（5 MiB）。
	MaxAttachmentBytes int64 = 5 << 20

	// EditWindow 是发帖后可编辑的窗口。恰好 24h 时窗口已关闭（见 WithinEditWindow）。
	EditWindow = 24 * time.Hour

	// DefaultPresignTTL 是附件预签名 URL 的短 TTL。Spec 只写「短 TTL」未给
	// 具体值，Options.PresignTTL 可覆盖；此处取 5 分钟作为施工期默认。
	DefaultPresignTTL = 5 * time.Minute

	// DefaultCacheTTL 是列表/详情缓存的短 TTL。
	DefaultCacheTTL = 30 * time.Second

	// objectKeyPrefix 是帖子附件的对象 key 前缀。Spec 预留的
	// announcements/ 前缀本期不实现（ValidateAttachments 会拒掉）。
	objectKeyPrefix = "posts"
)

// Post 是一条帖子。
//
// AuthorID 只在服务端与 isMine 判定中使用：读者侧列表/详情**不下发**它，
// 作者本人靠响应里的 isMine 认出自己的帖子；只有 /admin/posts 才带真实作者。
type Post struct {
	ID          int64
	Board       Board
	Title       string
	Body        string
	Attachments []string // 只存对象 key，不存 URL
	PublishedAt time.Time
	EditedAt    *time.Time
	DeletedAt   *time.Time
	AuthorID    int64

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Deleted 判断是否已软删。
func (p *Post) Deleted() bool { return p != nil && p.DeletedAt != nil }

// Reply 是一条留言。
//
// 没有 parentReplyId（不嵌套）。作者标识**存在但不下发**：
// 对读者匿名（Q2/Q12），服务端仍必须知道是谁，否则
//   - 「评论被点赞 / 评论被回复」两个通知触发点算不出收件人；
//   - 将来的「我的留言」也无法实现。
//
// 区分「存不存」与「发不发」是这层的核心：AuthorID 在 handler 里
// 一律不进响应体，只在服务端与 isMine 判定中使用。
type Reply struct {
	ID     int64
	PostID int64
	Body   string
	// AuthorID 仅服务端使用，绝不下发给其它调用方。
	AuthorID    int64
	PublishedAt time.Time
	DeletedAt   *time.Time
}

// Deleted 判断是否已软删。
func (r *Reply) Deleted() bool { return r != nil && r.DeletedAt != nil }

// Reaction 是一次点赞/收藏。
//
// 唯一键是 (TargetType, TargetID, Kind, UserID)：同一用户对同一靶子的
// 同一种反应只算一次，取消即删除该行。
type Reaction struct {
	TargetType TargetType
	TargetID   int64
	Kind       ReactionKind
	UserID     int64
}

// ---------- 纯函数校验（Testing Decisions ①：无 IO，可单测） ----------

// ValidateBoard 校验并解析板块。空串非法（列表接口的「不过滤」另用零值
// Board 表达，不经过这里）。
func ValidateBoard(s string) (Board, error) {
	b, ok := ParseBoard(s)
	if !ok {
		return "", apperr.Errorf(apperr.CodeValidationFailed, "板块取值非法")
	}
	return b, nil
}

// ValidateAttachmentCount 校验附件数量上限。
func ValidateAttachmentCount(n int) error {
	if n > MaxAttachments {
		return apperr.Errorf(apperr.CodeTooManyAttachments, "附件最多 %d 张，得到 %d 张", MaxAttachments, n)
	}
	return nil
}

// ValidateAttachmentSize 校验单个体积上限。
func ValidateAttachmentSize(n int64) error {
	if n > MaxAttachmentBytes {
		return apperr.Errorf(apperr.CodeAttachmentTooLarge, "附件超过 %d 字节", MaxAttachmentBytes)
	}
	return nil
}

// ValidateText 校验标题/正文/留言的长度与空值。
func ValidateText(label, s string, max int) error {
	if s == "" {
		return apperr.Errorf(apperr.CodeValidationFailed, "%s不能为空", label)
	}
	if len(s) > max {
		return apperr.Errorf(apperr.CodeValidationFailed, "%s超长（上限 %d）", label, max)
	}
	return nil
}

// allowedMedia 是「嗅探出的 MIME → 规范扩展名」白名单。
//
// jpg 与 jpeg 是同一种编码，统一落 jpg；白名单按内容嗅探判定，
// 扩展名只是 key 的后缀，不作为准入依据。
var allowedMedia = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/webp": "webp",
	"image/gif":  "gif",
}

// ValidateMediaType 把嗅探出的 MIME 映射为白名单扩展名；不在白名单返回
// unsupported_media_type。
func ValidateMediaType(contentType string) (string, error) {
	ext, ok := allowedMedia[contentType]
	if !ok {
		return "", apperr.Errorf(apperr.CodeUnsupportedMedia, "不支持的图片格式：%s", contentType)
	}
	return ext, nil
}

// ValidateAttachments 校验 POST/PATCH 传入的附件 key 数组：
// 数量上限、非空、以及只允许本期实现的 posts/ 前缀。
//
// 前缀校验是刻意的：上传端点只签发 posts/ 前缀的 key，announcements/
// 前缀本期不实现；若允许任意 key，客户端就能把帖子挂到别人的对象上。
func ValidateAttachments(keys []string) error {
	if err := ValidateAttachmentCount(len(keys)); err != nil {
		return err
	}
	for _, k := range keys {
		if k == "" {
			return apperr.Errorf(apperr.CodeValidationFailed, "附件 key 不能为空")
		}
		if len(k) < len(objectKeyPrefix)+1 || k[:len(objectKeyPrefix)+1] != objectKeyPrefix+"/" {
			return apperr.Errorf(apperr.CodeValidationFailed, "附件 key 前缀非法")
		}
	}
	return nil
}

// WithinEditWindow 判断 publishedAt 发出的帖子在 now 是否仍可编辑。
//
// 边界按「24h 内」的严格口径：耗时 < 24h 可编辑，恰好 24h 或更久即锁定。
// 另外 fail-closed：若 now 早于 publishedAt（时钟回拨/数据异常）也不放行，
// 避免出现「未来时刻反而能编辑」的窗口。
func WithinEditWindow(publishedAt, now time.Time) bool {
	if now.Before(publishedAt) {
		return false
	}
	return now.Sub(publishedAt) < EditWindow
}

// ---------- 上传路径用的薄封装 ----------

// errValidation 是「入参不合法」的薄封装，少写一层 apperr 前缀。
func errValidation(format string, args ...any) error {
	return apperr.Errorf(apperr.CodeValidationFailed, format, args...)
}

// errUnsupportedMedia 构造「格式不在白名单」的错误，带上嗅探出的真实类型。
//
// 带上真实类型是有意的：客户端说传了 png、实际是 zip 时，
// 把 "application/zip" 回给调用方能直接定位问题。
func errUnsupportedMedia(actual string) error {
	return apperr.Errorf(apperr.CodeUnsupportedMedia,
		"只支持 jpg/png/webp/gif，实际是 %s", actual)
}
