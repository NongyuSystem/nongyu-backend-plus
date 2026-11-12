package announcement

import (
	"context"
	"time"

	"nongyu-backend-plus/internal/content"
	"nongyu-backend-plus/internal/platform"
)

// 分页与字段上限。
const (
	// DefaultPageSize 是未指定 limit 时的页大小。
	DefaultPageSize = 20
	// MaxPageSize 是单页上限，防止调用方用巨大 limit 拖垮一次查询。
	MaxPageSize = 100
	// MaxTitleLen / MaxBodyLen 是入参长度上限。
	// 上限值属 Spec 的「阻塞开工」层，改它们要同步改 Spec。
	MaxTitleLen = 200
	MaxBodyLen  = 20000
)

// Options 是构造 Service 的依赖。
type Options struct {
	// Repo 必填。
	Repo Repo
	// Clock 必填（失败即 panic，与仓储解析器的做法一致：
	// 依赖缺失应该在装配期暴露，而不是在第一个请求进来时空指针）。
	Clock platform.Clock
}

// Service 是公告的业务逻辑。
//
// 它只做三件事：校验入参、判定可见性、驱动状态机。
// 不感知 HTTP，也不直接碰 SQL（repo 接口在后面）。
type Service struct {
	repo  Repo
	clock platform.Clock
}

// New 构造 Service。
func New(opts Options) *Service {
	if opts.Repo == nil {
		panic("announcement: Options.Repo 必填")
	}
	if opts.Clock == nil {
		opts.Clock = platform.NewRealClock()
	}
	return &Service{repo: opts.Repo, clock: opts.Clock}
}

// ListResult 是读者侧列表的一次返回。
type ListResult struct {
	Items []*Announcement
	// Next 非空表示「还有更多」，调用方拿它作为下一次的 after。
	//
	// 注意它指向**本次扫描到的最后一行**，而不是本次返回的最后一条可见行：
	// 受众过滤会丢掉一部分行，若用可见行做游标，被丢掉的那批会在下一页
	// 重新扫到，造成重复翻页。代价是单页可见条数可能少于 limit。
	Next *PageCursor
}

// List 返回读者侧可见的公告。
func (s *Service) List(ctx context.Context, v Viewer, after PageCursor, limit int) (ListResult, error) {
	limit = normalizeLimit(limit)
	now := s.clock.Now()

	rows, err := s.repo.ListPublished(ctx, after, limit)
	if err != nil {
		return ListResult{}, err
	}

	out := make([]*Announcement, 0, len(rows))
	for _, a := range rows {
		if Listable(a, v, now) {
			out = append(out, a)
		}
	}

	var next *PageCursor
	// 只有「扫满一页」才可能存在更多；扫不满说明到底了。
	if len(rows) == limit && len(rows) > 0 {
		last := rows[len(rows)-1]
		next = &PageCursor{Pinned: last.Pinned, PublishedAt: PublishedOrZero(last), ID: last.ID}
	}
	return ListResult{Items: out, Next: next}, nil
}

// Latest 返回读者侧优先级最高的那一条（首页通知栏用）。没有则返回 nil。
//
// 为什么不能「取一页然后扫一遍就返回」：受众与有效期过滤只发生在服务层，
// 仓储返回的是**未过滤**的行。若窗口取固定 50 条，而前 50 条恰好都被
// 过滤掉（例如学校近期只发定向公告，突然发一条全员公告），第 51 条本应
// 可见的公告就会被静默漏掉 —— 首页通知栏会显示「没有公告」。
//
// 因此这里按游标真正翻页，直到找到第一条可见的或扫到底为止。翻页有上界，
// 避免异常数据（大量不可见行）把一次请求拖成无限循环。
func (s *Service) Latest(ctx context.Context, v Viewer) (*Announcement, error) {
	now := s.clock.Now()

	const pageSize = 50
	// 上界：最多扫 20 页（1000 行）。超过这个量说明公告表本身异常，
	// 宁可返回「没有」也不要让首页请求挂住。
	const maxPages = 20

	var cursor PageCursor
	for page := 0; page < maxPages; page++ {
		rows, err := s.repo.ListPublished(ctx, cursor, pageSize)
		if err != nil {
			return nil, err
		}
		for _, a := range rows {
			if Listable(a, v, now) {
				return a, nil
			}
		}
		// 没扫满一页说明到底了。
		if len(rows) < pageSize {
			return nil, nil
		}
		last := rows[len(rows)-1]
		cursor = PageCursor{Pinned: last.Pinned, PublishedAt: PublishedOrZero(last), ID: last.ID}
	}
	return nil, nil
}

// Get 按 id 打开公告详情。不可读时返回 content.CodeNotFound
// （与「不存在」不区分，避免用状态码差异探测资源存在性）。
//
// 用服务层持有的 clock 调 ReadableAt，而不是让 Readable 自己取 time.Now：
// 否则「未生效不可读」这条在测试里就无法固定时刻验证。
func (s *Service) Get(ctx context.Context, v Viewer, id int64) (*Announcement, error) {
	if id <= 0 {
		return nil, content.Errorf(content.CodeValidationFailed, "公告 id 非法")
	}
	a, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ReadableAt(a, v, s.clock.Now()) {
		return nil, content.Errorf(content.CodeNotFound, "公告不存在")
	}
	return a, nil
}

// CreateInput 是创建/编辑公告的入参（管理端）。
type CreateInput struct {
	Title         string
	Body          string
	EffectiveFrom *time.Time
	ExpiresAt     *time.Time
	Pinned        bool
	Audience      Audience
	// Publish 为真表示创建后立即发布，否则存草稿。
	Publish bool
}

// Create 创建一条公告（草稿或直接发布）。
func (s *Service) Create(ctx context.Context, in CreateInput) (*Announcement, error) {
	a := &Announcement{
		Title:         in.Title,
		Body:          in.Body,
		EffectiveFrom: in.EffectiveFrom,
		ExpiresAt:     in.ExpiresAt,
		Pinned:        in.Pinned,
		Audience:      in.Audience,
		Status:        StatusDraft,
	}
	if err := validate(a); err != nil {
		return nil, err
	}
	if in.Publish {
		now := s.clock.Now()
		a.Status = StatusPublished
		a.PublishedAt = &now
		a.PublishRevision = 1
	}
	if err := s.repo.Create(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// UpdateInput 是编辑公告的入参。指针字段为 nil 表示「不改这一项」。
type UpdateInput struct {
	Title              *string
	Body               *string
	EffectiveFrom      *time.Time
	ClearEffectiveFrom bool
	ExpiresAt          *time.Time
	ClearExpiresAt     bool
	Pinned             *bool
	Audience           *Audience
}

// Update 编辑一条公告。已软删的视为不存在。
func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (*Announcement, error) {
	a, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Title != nil {
		a.Title = *in.Title
	}
	if in.Body != nil {
		a.Body = *in.Body
	}
	if in.ClearEffectiveFrom {
		a.EffectiveFrom = nil
	} else if in.EffectiveFrom != nil {
		a.EffectiveFrom = in.EffectiveFrom
	}
	if in.ClearExpiresAt {
		a.ExpiresAt = nil
	} else if in.ExpiresAt != nil {
		a.ExpiresAt = in.ExpiresAt
	}
	if in.Pinned != nil {
		a.Pinned = *in.Pinned
	}
	if in.Audience != nil {
		a.Audience = *in.Audience
	}
	if err := validate(a); err != nil {
		return nil, err
	}
	if err := s.repo.Update(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// Publish 发布或重发。
//
// 状态机严格按 Spec 的三条转移：
//   - draft → published：首次发布，publishRevision 置 1；
//   - revoked → published：重发，publishRevision 自增；
//   - published → published：**不是合法转移**，直接拒绝。
//
// 为什么不允许 published → published：publishRevision 是通知幂等键的一部分
// （与公告 id 一起），每自增一次就会给受众再发一轮通知。若允许对已发布的
// 公告再「发布」，管理端误点两次就会让学生收到两条同样的通知。
// 想重新通知的正当路径是「撤回 → 重发」，那在语义上也确实是一次新的发布。
//
// 早期实现把 published 也纳入了允许分支并自增 revision —— 那是超出 Spec
// 的宽松，已按 Spec 收紧。
func (s *Service) Publish(ctx context.Context, id int64) (*Announcement, error) {
	a, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	switch a.Status {
	case StatusDraft:
		now := s.clock.Now()
		a.Status = StatusPublished
		a.PublishedAt = &now
		a.PublishRevision = 1
	case StatusRevoked:
		now := s.clock.Now()
		a.Status = StatusPublished
		a.PublishedAt = &now
		a.PublishRevision++
	case StatusPublished:
		return nil, content.Errorf(content.CodeValidationFailed, "公告已发布；如需重新通知请先撤回再重发")
	default:
		return nil, content.Errorf(content.CodeValidationFailed, "公告状态非法")
	}

	if err := s.repo.Update(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// Revoke 撤回公告。撤回后读者侧任何入口都不可见；PublishRevision 不变
// （撤回不是一次发布）。
func (s *Service) Revoke(ctx context.Context, id int64) (*Announcement, error) {
	a, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	// draft 不能直接撤回：它从来没发布过，「撤回」无从谈起。
	if a.Status == StatusDraft {
		return nil, content.Errorf(content.CodeValidationFailed, "草稿不能撤回")
	}
	if a.Status == StatusRevoked {
		return a, nil // 幂等
	}
	a.Status = StatusRevoked
	if err := s.repo.Update(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// Delete 软删一条公告。已删的视为成功（幂等）。
func (s *Service) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return content.Errorf(content.CodeValidationFailed, "公告 id 非法")
	}
	return s.repo.MarkDeleted(ctx, id, s.clock.Now())
}

// AdminListResult 是管理端列表的一次返回（含草稿与已撤回）。
type AdminListResult struct {
	Items []*Announcement
	Next  *PageCursor
}

// AdminList 返回管理端可见的全部公告（不过滤受众与有效期）。
func (s *Service) AdminList(ctx context.Context, after PageCursor, limit int) (AdminListResult, error) {
	limit = normalizeLimit(limit)
	rows, err := s.repo.ListAll(ctx, after, limit)
	if err != nil {
		return AdminListResult{}, err
	}
	var next *PageCursor
	if len(rows) == limit && len(rows) > 0 {
		last := rows[len(rows)-1]
		next = &PageCursor{Pinned: last.Pinned, PublishedAt: PublishedOrZero(last), ID: last.ID}
	}
	return AdminListResult{Items: rows, Next: next}, nil
}

// AdminGet 取一条公告供管理端使用：**不过滤可见性**，草稿与已撤回都返回。
func (s *Service) AdminGet(ctx context.Context, id int64) (*Announcement, error) {
	if id <= 0 {
		return nil, content.Errorf(content.CodeValidationFailed, "公告 id 非法")
	}
	return s.repo.FindByID(ctx, id)
}

// normalizeLimit 把 limit 收敛到 [1, MaxPageSize]。
func normalizeLimit(limit int) int {
	if limit <= 0 {
		return DefaultPageSize
	}
	if limit > MaxPageSize {
		return MaxPageSize
	}
	return limit
}

// validate 校验公告的可写字段。
//
// 这里刻意不校验「有效期是否已过」：允许创建一条已过期的公告是合法的
// （例如补录一条刚下线的通知），它会立刻不出现在读者列表里，但详情可查。
func validate(a *Announcement) error {
	if a.Title == "" {
		return content.Errorf(content.CodeValidationFailed, "标题不能为空")
	}
	if len(a.Title) > MaxTitleLen {
		return content.Errorf(content.CodeValidationFailed, "标题超长")
	}
	if a.Body == "" {
		return content.Errorf(content.CodeValidationFailed, "正文不能为空")
	}
	if len(a.Body) > MaxBodyLen {
		return content.Errorf(content.CodeValidationFailed, "正文超长")
	}
	if a.EffectiveFrom != nil && a.ExpiresAt != nil && !a.ExpiresAt.After(*a.EffectiveFrom) {
		return content.Errorf(content.CodeValidationFailed, "失效时间必须晚于生效时间")
	}
	return validateAudience(a.Audience)
}

// validateAudience 校验受众条件自洽性。
//
// fail-closed：范围是 role 却没有角色列表、范围是 college_grade 却两个维度
// 都为空，都会变成「谁也看不到」或「谁都看得到」的歧义，必须在写入时拒掉。
func validateAudience(a Audience) error {
	switch a.Scope {
	case AudienceAll:
		if len(a.Roles) > 0 || a.College != "" || a.Grade != "" {
			return content.Errorf(content.CodeValidationFailed, "受众范围为全员时不应携带条件")
		}
		return nil
	case AudienceRole:
		if len(a.Roles) == 0 {
			return content.Errorf(content.CodeValidationFailed, "按角色定向时必须指定角色")
		}
		for _, r := range a.Roles {
			if r < 0 {
				return content.Errorf(content.CodeValidationFailed, "角色取值非法")
			}
		}
		return nil
	case AudienceCollegeGrade:
		if a.College == "" && a.Grade == "" {
			return content.Errorf(content.CodeValidationFailed, "按学院/年级定向时至少指定一个维度")
		}
		return nil
	default:
		return content.Errorf(content.CodeValidationFailed, "受众范围非法")
	}
}
