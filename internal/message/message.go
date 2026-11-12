// Package message 是「站内消息」域。
//
// 单表 per-user 投递行：一行 = 「一个收件人的一条消息」，同时承载两类形态：
//   - notification   通知：由服务端内部路径（app/notify 编排层）经 Emit 写入，
//     **不接受外部直接创建**；同幂等键重复触发走 upsert 合并。
//   - internal_mail  内部信：用户主动发给另一名用户，实名可见（SenderID）。
//
// 领域语义依据：
//   - docs/specs/spec-六类常见功能.md 的「站内消息」「错误码」「Testing Decisions」节；
//   - docs/tech/决策-消息域模型.md（Q1~Q6、D1 快照语义、D2 模型层拒自发自收）；
//   - docs/tech/决策-通知派生规则.md（Q4 幂等键与 upsert 合并、Q5 双层过滤）。
//
// 本包只依赖 internal/apperr（共享错误码）、internal/platform（Clock）与 gin，
// 不 import server / app / identity / config；存储实现放 internal/platform/pgstore。
package message

import (
	"strconv"
	"time"
)

// Kind 是站内消息的形态。
type Kind string

const (
	// KindNotification 通知：服务端派生，外部不可直接创建。
	KindNotification Kind = "notification"
	// KindInternalMail 内部信：用户 → 用户，实名可见。
	KindInternalMail Kind = "internal_mail"
)

// ParseKind 解析形态取值；空串与未知值都返回 false（fail-closed）。
func ParseKind(s string) (Kind, bool) {
	switch Kind(s) {
	case KindNotification:
		return KindNotification, true
	case KindInternalMail:
		return KindInternalMail, true
	default:
		return "", false
	}
}

// Valid 判断形态取值是否合法。
func (k Kind) Valid() bool {
	_, ok := ParseKind(string(k))
	return ok
}

// 字段长度与分页上限。
//
// 上限值属 Spec 的「阻塞开工」层：Spec 只写「body 有长度上限」未给确切数值，
// 这里沿用同构范本 internal/content/announcement 的取值（见回报「存疑点」）。
const (
	// MaxTitleLen 是标题长度上限。
	MaxTitleLen = 200
	// MaxBodyLen 是正文长度上限（通知快照同样受此约束）。
	MaxBodyLen = 20000
	// DefaultPageSize 是未指定 limit 时的页大小。
	DefaultPageSize = 20
	// MaxPageSize 是单页上限，防止巨大 limit 拖垮一次查询。
	MaxPageSize = 100
)

// Message 是一条 per-user 投递行。
//
// Title / Body 是**触发方写入时落库的快照**（D1）：读路径不按 source 实时渲染，
// 来源被软删后消息仍可读、读路径无跨域 join。代价是来源改文案后旧通知不跟随。
type Message struct {
	ID          int64
	RecipientID int64
	Kind        Kind

	// SourceDomain / SourceObjectID 对通知必填；内部信为空。
	// SourceObjectID 含「动作与靶子」（如 like:post:88、reply:1234），
	// 由编排层算好传入（决策-通知派生规则.md Q4）。
	SourceDomain   string
	SourceObjectID string

	Title string
	Body  string

	// Hits 是合并次数：通知首次写入为 1，同键重复触发累加；内部信固定为 1。
	Hits int

	// ReadAt 为空表示未读；DeletedAt 非空表示软删（不计未读）。
	ReadAt    *time.Time
	DeletedAt *time.Time
	CreatedAt time.Time

	// SenderID 仅内部信有意义（实名可见）；通知恒为 0。
	SenderID int64
}

// Unread 判断这条消息当前是否计入未读数。
//
// 口径固定自 决策-消息域模型.md Q4：count(read_at is null and deleted_at is null)。
// 软删优先：删掉的未读消息也不再计数。
func (m *Message) Unread() bool {
	return m.ReadAt == nil && m.DeletedAt == nil
}

// EmitInput 是 app/notify 编排层调用 Emit 的入参（服务端内部写入面）。
//
// 它不是 HTTP 入参：通知不接受外部直接创建，只能由编排层在「主业务提交成功后」
// 以异步旁路方式调用（决策-通知派生规则.md Q3）。
type EmitInput struct {
	// RecipientID 是收件人。必须 > 0。
	RecipientID int64
	// ActorID 是触发动作的人；与 RecipientID 相等时写入口 fail-closed 过滤（D2 双保险）。
	ActorID int64
	// SourceDomain / SourceObjectID 组成通知幂等键的一部分，均不可为空。
	SourceDomain   string
	SourceObjectID string
	// Title / Body 是触发方渲染好的快照。
	Title string
	Body  string
}

// NotificationKey 是通知的部分唯一索引四元组
// (recipientId, kind=notification, sourceDomain, sourceObjectId) 的领域表示。
//
// kind 恒为 notification（内部信 source 为空、不参与唯一索引），故不单列字段。
// 它同时是 Emit 算出的幂等键与内存仓储 notifIdx 的键。
type NotificationKey struct {
	RecipientID    int64
	SourceDomain   string
	SourceObjectID string
}

// String 把幂等键编成仓储索引用的不透明串。
//
// 用 \x00 分隔而不是 ':'：SourceObjectID 本身含冒号（like:post:88），
// 用可见字符拼接会让 (a, "b:c") 与 (a, "b", "c") 之类组合碰撞。
func (k NotificationKey) String() string {
	return strconv.FormatInt(k.RecipientID, 10) + "\x00" + k.SourceDomain + "\x00" + k.SourceObjectID
}

// NotificationKeyOf 从 EmitInput 组装通知幂等键。
//
// 纯函数、无 IO：幂等键的组装规则（哪些字段参与、是否含动作与靶子）是
// 本 effort 逻辑最密的一层，必须由纯函数单测直接锁定（Testing Decisions ①）。
func NotificationKeyOf(in EmitInput) NotificationKey {
	return NotificationKey{
		RecipientID:    in.RecipientID,
		SourceDomain:   in.SourceDomain,
		SourceObjectID: in.SourceObjectID,
	}
}

// IsSelfEmit 判断这次触发是否「发给自己」，写入口应据此过滤（D2 模型层）。
//
// ActorID 为 0 表示系统触发（无明确动作人），不与任何收件人相等——否则
// 0 == 0 会把所有系统通知误杀。编排层本就应在算收件人时过滤一次，
// 这里是模型层的第二道保险（决策-通知派生规则.md Q5）。
func IsSelfEmit(in EmitInput) bool {
	return in.ActorID > 0 && in.ActorID == in.RecipientID
}

// CreateMailInput 是创建内部信的入参（service 层）。
//
// SenderID 由 handler 用注入的 userIDOf 填当前会话用户，忽略客户端自报，
// 避免伪造发送者。
type CreateMailInput struct {
	RecipientID int64
	SenderID    int64
	Title       string
	Body        string
}
