// Package announcement 是公告子域。
//
// 公告在本 effort 里是**独立内容**，不是帖子的一个类型取值（旧仓用
// `post_type='announcement'` 承载，那是被明确否决的形状，见 map #43 子票 #44）。
//
// 领域语义的完整依据在 docs/tech/决策-公告域模型与作用域.md；
// 本包的字段与状态机取值来自 docs/specs/spec-六类常见功能.md 的「数据契约」节。
package announcement

import (
	"time"
)

// Status 是公告状态。
type Status string

const (
	// StatusDraft 草稿：只有管理端可见，永不进入读者侧列表。
	StatusDraft Status = "draft"
	// StatusPublished 已发布：读者侧可见（还需过有效期与受众判定）。
	StatusPublished Status = "published"
	// StatusRevoked 已撤回：读者侧任何入口都不可见，但行仍在（不物理删除）。
	StatusRevoked Status = "revoked"
)

// ParseStatus 解析状态取值。
func ParseStatus(s string) (Status, bool) {
	switch Status(s) {
	case StatusDraft:
		return StatusDraft, true
	case StatusPublished:
		return StatusPublished, true
	case StatusRevoked:
		return StatusRevoked, true
	default:
		return "", false
	}
}

// Valid 判断状态取值是否合法。
func (s Status) Valid() bool {
	_, ok := ParseStatus(string(s))
	return ok
}

// AudienceScope 是公告的受众范围。
type AudienceScope string

const (
	// AudienceAll 全员可见（含未登录读者——这是「公开读」能读到公告的前提）。
	AudienceAll AudienceScope = "all"
	// AudienceRole 按角色定向（读者必须已登录且角色命中）。
	AudienceRole AudienceScope = "role"
	// AudienceCollegeGrade 按学院 + 年级定向（读者必须已登录且命中）。
	AudienceCollegeGrade AudienceScope = "college_grade"
)

// ParseAudienceScope 解析受众范围取值。
func ParseAudienceScope(s string) (AudienceScope, bool) {
	switch AudienceScope(s) {
	case AudienceAll:
		return AudienceAll, true
	case AudienceRole:
		return AudienceRole, true
	case AudienceCollegeGrade:
		return AudienceCollegeGrade, true
	default:
		return "", false
	}
}

// Audience 是公告的受众条件。
//
// 条件**存在公告自身上**（而不是发布时展开成一人一行的投递清单）：
// 公告是「拉取型」内容，读者来看时用自己当时的画像判定即可，
// 不需要为每个用户预生成一行。这一取舍让「改了学院的学生」能立刻
// 看到新学院范围的公告，代价是每次列表都要逐条做一次画像匹配。
type Audience struct {
	Scope AudienceScope
	// Roles 仅在 Scope=AudienceRole 时使用。
	Roles []int
	// College / Grade 仅在 Scope=AudienceCollegeGrade 时使用；
	// 留空表示该维度不限制（只按另一个维度过滤）。
	College string
	Grade   string
}

// Viewer 是「正在看公告的人」。
//
// 用值类型而不是取出整份用户实体：公告判定只需要四个事实，
// 传得更少，测试也更容易构造。
type Viewer struct {
	// Authenticated 表示是否携带有效农屿会话。未登录读者只能看到 AudienceAll 的公告。
	Authenticated bool
	// Role 是读者的角色（0/1/2）。仅 Authenticated 时有意义。
	Role int
	// College / Grade 来自学籍画像，可能为空（画像缺失时按「不命中」处理）。
	College string
	Grade   string
}

// Announcement 是一条公告。
type Announcement struct {
	ID    int64
	Title string
	Body  string

	Status Status

	// PublishedAt 是最近一次「发布/重发」的时间；草稿为 nil。
	PublishedAt *time.Time
	// EffectiveFrom 是生效起点（nil 表示发布即生效）。
	EffectiveFrom *time.Time
	// ExpiresAt 是失效时间（nil 表示永不过期）。
	ExpiresAt *time.Time
	// Pinned 为真时在列表中排在非置顶之前。
	Pinned bool
	// PublishRevision 每次「发布/重发」自增；撤回不增。
	// 它与公告 id 一起构成通知幂等键，使「编辑重发」能再通知一次，
	// 而「同一次发布」不会重复通知。
	PublishRevision int

	Audience Audience

	CreatedAt time.Time
	UpdatedAt time.Time
	// DeletedAt 非空表示软删（管理端删除）。
	DeletedAt *time.Time
}

// Deleted 判断是否已软删。
func (a *Announcement) Deleted() bool { return a.DeletedAt != nil }

// audienceMatch 判断这条公告的受众条件是否命中该读者。
//
// 三个分支都是 fail-closed 取向：范围需要身份而读者未登录 → 不命中；
// 画像缺失而范围要求该维度 → 不命中（宁可看不到，也不误发给范围外的人）。
func audienceMatch(a Audience, v Viewer) bool {
	switch a.Scope {
	case AudienceAll:
		return true
	case AudienceRole:
		if !v.Authenticated {
			return false
		}
		for _, r := range a.Roles {
			if r == v.Role {
				return true
			}
		}
		return false
	case AudienceCollegeGrade:
		if !v.Authenticated {
			return false
		}
		if a.College != "" && a.College != v.College {
			return false
		}
		if a.Grade != "" && a.Grade != v.Grade {
			return false
		}
		// 两个维度都空时不应走到这里（入参校验会拒），但真出现了
		// 也按「命中」处理会让它变成全员公告——所以这里要求至少有一个维度非空。
		return a.College != "" || a.Grade != ""
	default:
		// 未知范围一律不命中。
		return false
	}
}

// active 判断公告在 now 时刻是否处于有效期。
func active(a *Announcement, now time.Time) bool {
	if a.EffectiveFrom != nil && now.Before(*a.EffectiveFrom) {
		return false
	}
	if a.ExpiresAt != nil && !now.Before(*a.ExpiresAt) {
		return false
	}
	return true
}

// Listable 判断这条公告是否应出现在**读者侧列表**（含 latest）里。
//
// 四个条件缺一不可：未软删、已发布、在有效期内、受众命中。
func Listable(a *Announcement, v Viewer, now time.Time) bool {
	if a == nil || a.Deleted() {
		return false
	}
	if a.Status != StatusPublished {
		return false
	}
	if !active(a, now) {
		return false
	}
	return audienceMatch(a.Audience, v)
}

// Readable 判断这条公告是否可按 id 打开**详情**。
//
// 与 Listable 的差别只有一条：**不过滤有效期**。已过期的公告移出列表，
// 但详情仍可查——这样读者能核对「我之前看到的那条通知说的是什么」，
// 而不会因为过期就变成 404（map #43 子题 #44 Q2 的语义）。
//
// 已撤回的公告**不可读**：撤回的语义就是「当作没发过」。
func Readable(a *Announcement, v Viewer) bool {
	if a == nil || a.Deleted() {
		return false
	}
	if a.Status != StatusPublished {
		return false
	}
	return audienceMatch(a.Audience, v)
}
