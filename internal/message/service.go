package message

import (
	"context"
	"strconv"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/platform"
)

// Limiter 是「按发送者限频」的注入接缝。
//
// 复用注入而非本包自己实现滑动窗口：限频的窗口/阈值属装配层配置
// （config.RateLimitPerMinute、server.RateLimiter），业务域不该重复实现一份。
// nil 表示不限频，便于纯逻辑与 HTTP 集成测不必构造限流器。
type Limiter interface {
	// Allow 返回本次是否放行。key 由 MailLimitKey 生成（按发送者隔离）。
	// 返回 error 表示限流后端不可判定：调用方按**失败即拒绝**处理，
	// 且不把内部错误包成 4xx（handler 会统一映射成 500 且不泄露细节）。
	Allow(ctx context.Context, key string) (bool, error)
}

// MailLimitKey 生成按发送者隔离的限流键。
// 导出便于装配层与测试对齐口径（也便于观测）。
func MailLimitKey(senderID int64) string {
	return "message:mail:" + strconv.FormatInt(senderID, 10)
}

// UserExistsFunc 判定一个用户 id 是否对应真实存在的用户。
//
// 由装配层注入（用户实体属身份域，业务域不得 import identity）。
// nil 时 CreateMail 按 fail-closed 全部拒收件人，不会误发。
type UserExistsFunc func(ctx context.Context, userID int64) (bool, error)

// Options 是构造 Service 的依赖。
type Options struct {
	// Repo 必填（缺失即 panic，与仓储解析器/公告域一致：依赖缺失应在装配期暴露）。
	Repo Repo
	// Clock 可为 nil，默认真实时钟；测试注入固定时刻以便断言 readAt / createdAt。
	Clock platform.Clock
	// UserExists 收件人存在性校验。nil 时按 fail-closed 拒收。
	UserExists UserExistsFunc
	// Limiter 内部信按发送者限频。nil 时不限频。
	Limiter Limiter
}

// Service 是站内消息的业务逻辑。
//
// 只做四件事：校验入参、注入当前用户、驱动已读/软删状态、算通知幂等键。
// 不感知 HTTP、不直接碰 SQL，也不 import server / app / identity / config。
type Service struct {
	repo       Repo
	clock      platform.Clock
	userExists UserExistsFunc
	limiter    Limiter
}

// New 构造 Service。
func New(opts Options) *Service {
	if opts.Repo == nil {
		panic("message: Options.Repo 必填")
	}
	if opts.Clock == nil {
		opts.Clock = platform.NewRealClock()
	}
	return &Service{
		repo: opts.Repo, clock: opts.Clock,
		userExists: opts.UserExists, limiter: opts.Limiter,
	}
}

// ---------- 纯校验函数（无 IO，供纯函数单测直接锁定） ----------

// ParseMailKind 校验**外部创建请求**的 kind。
//
// Spec「站内消息」：POST /messages **仅 internal_mail**；通知不接受外部直接建。
// 空串按默认 internal_mail 处理（不强迫客户端显式写形态）；显式传 notification
// 或任何未知取值一律拒——不能静默把 notification 改写成 internal_mail，
// 否则「通知只能由服务端内部路径产生」这条边界就被客户端绕过了。
//
// 纯函数、无 IO：这是 Testing Decisions ① 里「kind 校验」的落点。
func ParseMailKind(raw string) (Kind, error) {
	if raw == "" {
		return KindInternalMail, nil
	}
	k, ok := ParseKind(raw)
	if !ok || k != KindInternalMail {
		return "", apperr.Errorf(apperr.CodeValidationFailed, "仅允许创建内部信，通知不接受外部直接建")
	}
	return k, nil
}

// ValidateMail 校验创建内部信的可写字段。
//
// 纯函数、无 IO：不去查收件人是否存在、不碰限流器——那两件是有副作用/有依赖的，
// 放在 CreateMail 里。这样「body 长度上限」「不能发自己」这类规则可以无 IO 单测。
//
// 长度用字节数（与 internal/content/announcement 的 validate 口径一致）。
func ValidateMail(in CreateMailInput) error {
	if in.SenderID <= 0 || in.RecipientID <= 0 {
		return apperr.Errorf(apperr.CodeValidationFailed, "发送者或收件人 id 非法")
	}
	if in.SenderID == in.RecipientID {
		return apperr.Errorf(apperr.CodeSelfSendForbidden, "不能给自己发内部信")
	}
	if in.Title == "" {
		return apperr.Errorf(apperr.CodeValidationFailed, "标题不能为空")
	}
	if len(in.Title) > MaxTitleLen {
		return apperr.Errorf(apperr.CodeValidationFailed, "标题超长")
	}
	if in.Body == "" {
		return apperr.Errorf(apperr.CodeValidationFailed, "正文不能为空")
	}
	if len(in.Body) > MaxBodyLen {
		return apperr.Errorf(apperr.CodeValidationFailed, "正文超长")
	}
	return nil
}

// ---------- 列表 / 未读数 ----------

// ListResult 是列表的一次返回。
type ListResult struct {
	Items []*Message
	// Next 非空表示「还有更多」，调用方拿它作为下一次的 cursor。
	// 过滤发生在 repo 内（MatchesFilter），所以 Next 指向本次返回的最后一行，
	// 不存在「被过滤掉的行在下一页重新扫到」的问题。
	Next *PageCursor
}

// List 返回当前用户的消息：createdAt 倒序、游标分页、可按 kind 与「仅未读」过滤。
//
// RecipientID 必须由调用方（handler）注入当前会话用户，绝不接受客户端自报，
// 否则「只返回本人」这条就形同虚设。
func (s *Service) List(ctx context.Context, f ListFilter, after PageCursor, limit int) (ListResult, error) {
	if f.RecipientID <= 0 {
		return ListResult{}, apperr.Errorf(apperr.CodeValidationFailed, "收件人 id 非法")
	}
	limit = normalizeLimit(limit)
	rows, err := s.repo.List(ctx, f, after, limit)
	if err != nil {
		return ListResult{}, err
	}
	var next *PageCursor
	if len(rows) == limit && len(rows) > 0 {
		last := rows[len(rows)-1]
		next = &PageCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return ListResult{Items: rows, Next: next}, nil
}

// UnreadCount 返回未读数。
//
// 口径固定自 决策-消息域模型.md Q4：count(readAt is null and deletedAt is null)。
// 软删优先——删掉的未读消息不再计数；kind 过滤与列表共用同一份 ListFilter。
func (s *Service) UnreadCount(ctx context.Context, f ListFilter) (int, error) {
	if f.RecipientID <= 0 {
		return 0, apperr.Errorf(apperr.CodeValidationFailed, "收件人 id 非法")
	}
	return s.repo.UnreadCount(ctx, f)
}

// ---------- 已读 / 软删（归属判定） ----------

// MarkRead 把单条置为已读，返回更新后的消息。
//
// **非本人 → 404**（apperr.CodeNotFound），与「不存在」返回同一个码和同一句文案，
// 不泄露资源存在性；已软删的行同样视为不存在。已读重复调用幂等成功。
func (s *Service) MarkRead(ctx context.Context, userID, id int64) (*Message, error) {
	if id <= 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "消息 id 非法")
	}
	m, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if m.RecipientID != userID || m.DeletedAt != nil {
		return nil, apperr.Errorf(apperr.CodeNotFound, "消息不存在")
	}
	if err := s.repo.MarkRead(ctx, id, s.clock.Now()); err != nil {
		return nil, err
	}
	return s.repo.FindByID(ctx, id)
}

// MarkAllRead 按当前筛选条件批量置已读，返回受影响行数。
//
// 「当前筛选条件」= GET /messages 的同一组参数（kind + unreadOnly），
// 由 handler 用 parseFilter 组装成同一个 ListFilter 传进来，避免两处口径漂移。
// 恒只作用于未读、未软删、属本人的行（UnreadOnly 开关不改变这个结果集）。
func (s *Service) MarkAllRead(ctx context.Context, f ListFilter) (int64, error) {
	if f.RecipientID <= 0 {
		return 0, apperr.Errorf(apperr.CodeValidationFailed, "收件人 id 非法")
	}
	return s.repo.MarkAllRead(ctx, f, s.clock.Now())
}

// Delete 软删一条；不计未读。非本人 → 404（不泄露存在性），重复删除幂等。
func (s *Service) Delete(ctx context.Context, userID, id int64) error {
	if id <= 0 {
		return apperr.Errorf(apperr.CodeValidationFailed, "消息 id 非法")
	}
	m, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if m.RecipientID != userID {
		return apperr.Errorf(apperr.CodeNotFound, "消息不存在")
	}
	return s.repo.MarkDeleted(ctx, id, s.clock.Now())
}

// ---------- 内部信创建（HTTP） ----------

// CreateMail 创建一条内部信。
//
// 校验顺序刻意固定：纯字段校验 → 收件人存在性（fail-closed）→ 限频 → 落库。
// 限频放在最后，是为了让非法请求不消耗配额。SenderID 由 handler 从会话注入，
// 忽略客户端自报（实名可见，不能让人伪造发件人）。
func (s *Service) CreateMail(ctx context.Context, in CreateMailInput) (*Message, error) {
	if err := ValidateMail(in); err != nil {
		return nil, err
	}

	// 收件人必须存在，**fail-closed**：
	//   - 没注入校验器 → 无法判定 → 拒；
	//   - 判定为不存在 → 拒；
	//   - 校验器返回 error → 同样拒（且统一映射成 recipient_not_found，
	//     不把上游故障变成可探测的 500 差异）。
	if s.userExists == nil {
		return nil, apperr.Errorf(apperr.CodeRecipientNotFound, "收件人不存在")
	}
	exists, err := s.userExists(ctx, in.RecipientID)
	if err != nil || !exists {
		return nil, apperr.Errorf(apperr.CodeRecipientNotFound, "收件人不存在")
	}

	// 按发送者限频；nil 时不限频（便于测试）。
	// 限流后端不可判定时返回**非领域错误**，handler 统一 500 且不泄露内部细节。
	if s.limiter != nil {
		allowed, err := s.limiter.Allow(ctx, MailLimitKey(in.SenderID))
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, apperr.Errorf(apperr.CodeRateLimited, "发送过于频繁，请稍后再试")
		}
	}

	m := &Message{
		RecipientID: in.RecipientID,
		Kind:        KindInternalMail,
		Title:       in.Title,
		Body:        in.Body,
		Hits:        1,
		SenderID:    in.SenderID,
		CreatedAt:   s.clock.Now(),
	}
	if err := s.repo.Create(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

// ---------- 通知写入面（供 app/notify 编排层调用，非 HTTP） ----------

// Emit 是通知的**服务端内部写入口**，供 internal/app 的 app/notify 薄编排单元调用。
//
// 语义：
//  1. IsSelfEmit 命中 → **静默丢弃并返回 nil**（不是失败）。这是 决策-消息域模型.md D2
//     的模型层过滤，与编排层 Q5 的「算收件人时先排除自己」构成双保险。
//  2. 用 NotificationKeyOf 算幂等键 (recipientId, sourceDomain, sourceObjectId)，
//     交给 Repo.UpsertNotification 做 upsert 合并（hits++ 且 readAt 重置为未读）。
//  3. 返回 error **仅供编排层计数 dropped / 写审计**；按 决策-通知派生规则.md Q3，
//     编排层**不得**把这个错误抛给主业务、不得回滚主业务、不得抛给用户——
//     通知是异步旁路，「发帖成功但通知没投递」是被明确记录的容忍度。
//
// 通知不接受外部直接创建（POST /messages 会拒 kind=notification），此方法是唯一入口。
func (s *Service) Emit(ctx context.Context, in EmitInput) error {
	if IsSelfEmit(in) {
		return nil
	}
	if in.RecipientID <= 0 || in.SourceDomain == "" || in.SourceObjectID == "" {
		return apperr.Errorf(apperr.CodeValidationFailed, "通知写入入参非法")
	}
	if len(in.Title) > MaxTitleLen || len(in.Body) > MaxBodyLen {
		return apperr.Errorf(apperr.CodeValidationFailed, "通知快照超长")
	}
	key := NotificationKeyOf(in)
	_, err := s.repo.UpsertNotification(ctx, key, in.Title, in.Body, s.clock.Now())
	return err
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
