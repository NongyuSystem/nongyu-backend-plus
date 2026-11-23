package post

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/platform"
)

// Options 是构造 Service 的依赖。
type Options struct {
	// Repo 必填。
	Repo Repo
	// Clock 缺省用真实时钟。
	Clock platform.Clock
	// ObjectStore 可选：为 nil 时详情不返回预签名 URL、软删不做对象清理、
	// 上传端点返回 validation_failed（仅测试/骨架期）。
	ObjectStore platform.ObjectStore
	// Cache 可选：帖子列表与详情的短 TTL 缓存。为 nil 时退化为直查（不缓存）。
	Cache Cache
	// PresignTTL 是附件预签名 URL 的有效期，缺省 DefaultPresignTTL。
	PresignTTL time.Duration
	// Logger 可选：异步清理失败时记日志，为 nil 时丢弃。
	Logger *slog.Logger
}

// Cache 是帖子域需要的缓存能力。
//
// 只要 Get/Set/Delete 三个方法，避免把整个 RedisCache 的语义泄进业务域，
// 也让测试可以塞一个 map 假件。注入是**可选**的：nil 时 Service 退化为不缓存。
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
}

// Service 是帖子业务逻辑。不感知 HTTP，也不直接碰 SQL。
type Service struct {
	// notify 是通知派生的窄钩子（装配层注入，可为 nil）。
	notify NotifyHook

	repo       Repo
	clock      platform.Clock
	store      platform.ObjectStore
	cache      Cache
	presignTTL time.Duration
	logger     *slog.Logger

	// ownership 记录「对象 key → 上传者」的归属，用于附件引用校验与清理判定。
	// 由 New 统一构造，所有 post.New(Options{...}) 调用点无需改动即可获得该能力。
	ownership *AttachmentOwnership

	// listEpoch 是每个板块的列表缓存「代」。写操作只自增对应板块的代，
	// 列表键含代值 → 旧代的所有游标页一次性全部失效，无需枚举游标。
	// 进程内计数，确定性（不依赖时钟），便于测试。
	listEpoch sync.Map // Board -> *atomic.Int64

	// cleanupFailures 累计异步清理失败的对象数（失败只记日志+计数，不阻塞主业务）。
	cleanupFailures atomic.Int64
}

// New 构造 Service。
func New(opts Options) *Service {
	if opts.Repo == nil {
		panic("post: Options.Repo 必填")
	}
	if opts.Clock == nil {
		opts.Clock = platform.NewRealClock()
	}
	if opts.PresignTTL <= 0 {
		opts.PresignTTL = DefaultPresignTTL
	}
	return &Service{
		repo: opts.Repo, clock: opts.Clock, store: opts.ObjectStore,
		cache: opts.Cache, presignTTL: opts.PresignTTL, logger: opts.Logger,
		ownership: NewAttachmentOwnership(),
	}
}

// CleanupFailureCount 返回异步附件清理累计失败的对象数（可观测性/测试用）。
func (s *Service) CleanupFailureCount() int64 { return s.cleanupFailures.Load() }

// ---------- 缓存键与失效 ----------

// listCacheKey 含 board + 游标 + 代 + limit（Spec：「键含 board+游标」）。
//
// authorID 也是键的一部分：**这是隐私边界**。若不区分作者，
// A 的「我的帖子」缓存会被 B 命中，B 就看到 A 的私下列表——
// 这比「缓存命中率低」严重得多，所以 authorID 必须进键（0 表示公开列表）。
//
// 同一作者的列表命中的是同一个键，写操作通过 boardEpoch 自增整体失效，
// 所以这里不需要为 mine 单独做失效逻辑。
func listCacheKey(board Board, authorID, epoch int64, after PageCursor, limit int) string {
	return fmt.Sprintf("post:list:%s:a%d:%d:%d:%d:%d",
		board, authorID, epoch, after.PublishedAt.UnixNano(), after.ID, limit)
}

func detailCacheKey(id int64) string { return fmt.Sprintf("post:detail:%d", id) }

func (s *Service) boardEpoch(board Board) int64 {
	v, _ := s.listEpoch.LoadOrStore(board, &atomic.Int64{})
	return v.(*atomic.Int64).Load()
}

// invalidateBoard 让某板块的全部列表缓存失效。
//
// Spec 说「写操作删对应 board 键」；由于列表键还含游标，一个板块会有无
// 数个 (board,cursor) 键，无法逐键 Delete。这里改为自增该板块的「代」，
// 列表键含代值，自增后所有旧键自然不可达 —— 等效于一次性删掉该板块的
// 全部列表键，且是 O(1)。短 TTL 兜底回收旧值。
func (s *Service) invalidateBoard(board Board) {
	v, _ := s.listEpoch.LoadOrStore(board, &atomic.Int64{})
	v.(*atomic.Int64).Add(1)
}

func (s *Service) invalidateDetail(ctx context.Context, id int64) {
	if s.cache != nil {
		_ = s.cache.Delete(ctx, detailCacheKey(id))
	}
}

// ---------- 入参 ----------

// CreateInput 是发帖入参。
type CreateInput struct {
	Board       string
	Title       string
	Body        string
	Attachments []string
}

// UpdateInput 是编辑入参（指针语义：nil 表示不改）。
type UpdateInput struct {
	Title       *string
	Body        *string
	Attachments *[]string
}

// ListResult 是一次列表返回。
type ListResult struct {
	Items []*Post
	Next  *PageCursor
}

// Detail 是一条帖子的详情（服务层产物，附件 URL 已按短 TTL 现签）。
type Detail struct {
	Post           *Post
	Replies        []*Reply
	AttachmentURLs []string
}

// ReactionState 是某用户对某靶子的反应状态（不缓存，逐请求现算）。
type ReactionState struct {
	LikedByMe     bool `json:"likedByMe"`
	FavoritedByMe bool `json:"favoritedByMe"`
}

// ---------- 写操作 ----------

// Create 发一条帖子。
func (s *Service) Create(ctx context.Context, authorID int64, in CreateInput) (*Post, error) {
	if authorID <= 0 {
		return nil, apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份")
	}
	board, err := ValidateBoard(in.Board)
	if err != nil {
		return nil, err
	}
	if err := ValidateText("标题", in.Title, MaxTitleLen); err != nil {
		return nil, err
	}
	if err := ValidateText("正文", in.Body, MaxBodyLen); err != nil {
		return nil, err
	}
	if err := ValidateAttachments(in.Attachments); err != nil {
		return nil, err
	}
	// 归属校验（安全）：每个附件 key 必须是「当前用户自己上传且已登记」的。
	// 纯函数 ValidateAttachments 只校验前缀，挡不住「引用他人对象 key + 删自己帖
	// 清掉他人附件」的越权破坏，故这里在落库前补一道 owner==actorID 判定。
	if err := s.ownership.ValidateOwned(in.Attachments, authorID); err != nil {
		return nil, err
	}
	now := s.clock.Now()
	p := &Post{
		Board: board, Title: in.Title, Body: in.Body,
		Attachments: append([]string(nil), in.Attachments...), AuthorID: authorID,
		PublishedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.Create(ctx, p); err != nil {
		return nil, err
	}
	s.invalidateBoard(board)
	return p, nil
}

// Update 编辑一条帖子：仅作者、且必须在 publishedAt 起 24h 窗口内。
func (s *Service) Update(ctx context.Context, actorID, id int64, in UpdateInput) (*Post, error) {
	if actorID <= 0 {
		return nil, apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份")
	}
	p, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Deleted() {
		return nil, apperr.Errorf(apperr.CodeNotFound, "帖子不存在")
	}
	// 非作者返回 not_found 而非 forbidden，避免用状态码探测「这条帖子存在」。
	if p.AuthorID != actorID {
		return nil, apperr.Errorf(apperr.CodeNotFound, "帖子不存在")
	}
	now := s.clock.Now()
	if !WithinEditWindow(p.PublishedAt, now) {
		return nil, apperr.Errorf(apperr.CodeLocked, "超过 %d 小时编辑窗口", int(EditWindow.Hours()))
	}
	if in.Title != nil {
		if err := ValidateText("标题", *in.Title, MaxTitleLen); err != nil {
			return nil, err
		}
		p.Title = *in.Title
	}
	if in.Body != nil {
		if err := ValidateText("正文", *in.Body, MaxBodyLen); err != nil {
			return nil, err
		}
		p.Body = *in.Body
	}
	if in.Attachments != nil {
		if err := ValidateAttachments(*in.Attachments); err != nil {
			return nil, err
		}
		// 同 Create：编辑时替换附件也必须只引用本人上传的 key。
		if err := s.ownership.ValidateOwned(*in.Attachments, actorID); err != nil {
			return nil, err
		}
		p.Attachments = append([]string(nil), (*in.Attachments)...)
	}
	t := now
	p.EditedAt = &t
	p.UpdatedAt = now
	if err := s.repo.Update(ctx, p); err != nil {
		return nil, err
	}
	s.invalidateBoard(p.Board)
	s.invalidateDetail(ctx, id)
	return p, nil
}

// Delete 软删一条帖子（仅作者）并异步清理附件对象。
func (s *Service) Delete(ctx context.Context, actorID, id int64) error {
	if actorID <= 0 {
		return apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份")
	}
	p, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if p.Deleted() {
		return nil // 幂等
	}
	if p.AuthorID != actorID {
		return apperr.Errorf(apperr.CodeNotFound, "帖子不存在")
	}
	if err := s.repo.MarkDeleted(ctx, id, s.clock.Now()); err != nil {
		return err
	}
	s.invalidateBoard(p.Board)
	s.invalidateDetail(ctx, id)
	// 只清理「属主 == 帖子作者」的 key：作者删自己的帖子时 actorID==p.AuthorID。
	s.cleanupAttachments(p.Attachments, p.AuthorID)
	return nil
}

// AdminDelete 是管理端软删（不校验作者；记审计由装配层负责）。
func (s *Service) AdminDelete(ctx context.Context, id int64) error {
	p, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if p.Deleted() {
		return nil
	}
	if err := s.repo.MarkDeleted(ctx, id, s.clock.Now()); err != nil {
		return err
	}
	s.invalidateBoard(p.Board)
	s.invalidateDetail(ctx, id)
	// 管理端删除没有「本人」语义；用帖子作者的身份做归属判定，只删该帖真正属主
	// 上传的 key（fail-closed：查不到归属的 key 一律跳过）。
	s.cleanupAttachments(p.Attachments, p.AuthorID)
	return nil
}

// cleanupAttachments 异步清理附件对象。
//
// 旁路语义（Spec：「失败只记日志 + 计数，不阻塞软删」）：用独立 goroutine
// 与独立 context（请求 ctx 在响应后会被 cancel），逐个 Delete；失败只累加
// 计数并记 Warn，绝不回滚或影响已成功的软删。
//
// ownerID 是「本次删除所依据的属主身份」（读者侧删帖=当前作者，管理端删帖=帖子作者）。
// **归属判定（安全）**：只删 s.ownership.OwnedBy(k, ownerID) 为真的 key。
// 未登记归属、属主不符的 key 一律跳过（fail-closed），从而堵住「把别人的对象 key
// 塞进自己帖子、再删自己帖子清掉他人附件」的越权破坏。
func (s *Service) cleanupAttachments(keys []string, ownerID int64) {
	if s.store == nil || len(keys) == 0 {
		return
	}
	store, logger := s.store, s.logger
	// 在启动 goroutine **之前**就把要删的 key 过滤好：归属是确定的，不需要在
	// 异步里重算；也避免在 goroutine 里持有 ownership 锁做逐条过滤。
	owned := make([]string, 0, len(keys))
	for _, k := range keys {
		if s.ownership.OwnedBy(k, ownerID) {
			owned = append(owned, k)
		} else if logger != nil {
			// 只记标识性字段（key），不记归属内容；旁路日志不影响主业务。
			logger.Warn("附件对象清理跳过：归属不明或属主不符（fail-closed）", "key", k)
		}
	}
	if len(owned) == 0 {
		return
	}
	go func() {
		// **必须带超时**：用 Background 且不设期限的话，对象存储挂住时
		// 这个 goroutine 会无限期阻塞；反复删帖就会不断泄漏 goroutine，
		// 最终把进程拖死。旁路任务更应该自我约束，而不是指望下游会返回。
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		for _, k := range owned {
			if err := store.Delete(ctx, k); err != nil {
				s.cleanupFailures.Add(1)
				if logger != nil {
					logger.Warn("附件对象清理失败（旁路，不影响主业务）", "key", k, "err", err)
				}
			}
		}
	}()
}

// AddReply 发一条留言（无嵌套）。
func (s *Service) AddReply(ctx context.Context, actorID, postID int64, body string) (*Reply, error) {
	if actorID <= 0 {
		return nil, apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份")
	}
	if err := ValidateText("留言", body, MaxReplyBodyLen); err != nil {
		return nil, err
	}
	p, err := s.repo.FindByID(ctx, postID)
	if err != nil {
		return nil, err
	}
	if p.Deleted() {
		return nil, apperr.Errorf(apperr.CodeNotFound, "帖子不存在")
	}
	r := &Reply{PostID: postID, Body: body, AuthorID: actorID, PublishedAt: s.clock.Now()}
	if err := s.repo.CreateReply(ctx, r); err != nil {
		return nil, err
	}
	// 留言改变了详情里的回复列表，但 per-user 反应状态不缓存，无需动 board 列表。
	s.invalidateDetail(ctx, postID)

	// 通知派生（旁路）：帖子被回复 → 收件人是帖主。
	// 必须在业务写入成功**之后**触发；钩子内部非阻塞，失败不影响本次返回。
	s.fire(ctx, Actor{Action: "reply", ActorID: actorID, PostID: postID, ReplyID: r.ID})
	return r, nil
}

// React 建立或取消一条点赞/收藏。
//
// targetType/kind 用 string 接 HTTP 传入的枚举值，靶子 id 单独用 int64。
// 重复建立/重复取消都是幂等（repo 返回 created/removed=false，不报错）。
//
// 兼容说明：此签名被装配层的通知派生测试（internal/app/notify_wiring_test.go）
// 直接调用，故保持不变、不追加 postID 参数。postID 传 0 表示「非嵌套资源调用，
// 不做父子归属校验」。HTTP 的嵌套资源路由（/posts/:id/replies/:replyId/like）
// 必须改走 ReactOnComment，由它把 :id 传进来做归属校验。
func (s *Service) React(ctx context.Context, actorID int64, targetType string, targetID int64, kind string, add bool) error {
	return s.react(ctx, actorID, targetType, targetID, kind, add, 0)
}

// ReactOnComment 供嵌套资源路由 POST/DELETE /posts/:id/replies/:replyId/like 调用。
//
// 安全修复：旧实现里 reactCommentHandler 完全忽略 URL 里的 :id，只用全局 replyId
// 查靶子，于是「POST /posts/任意数字/replies/<他人replyId>/like」都能越过父资源
// 边界对全站任意留言点赞（并派生通知）。这里把 postID 一起带进来，在建立反应前
// 校验「该留言确实属于 URL 里的这条帖子」，不匹配返回 not_found（与不存在同码，
// 不泄露资源存在性）。
func (s *Service) ReactOnComment(ctx context.Context, actorID, postID, replyID int64, kind string, add bool) error {
	if postID <= 0 {
		return apperr.Errorf(apperr.CodeValidationFailed, "帖子 id 非法")
	}
	return s.react(ctx, actorID, string(TargetComment), replyID, kind, add, postID)
}

// react 是 React / ReactOnComment 的共用实现。
//
// postID > 0 且靶子是 comment 时，ensureTarget 会校验 r.PostID == postID。
// postID==0（React 的直接调用/内部触发）沿用旧行为，不做父子归属校验。
func (s *Service) react(ctx context.Context, actorID int64, targetType string, targetID int64, kind string, add bool, postID int64) error {
	if actorID <= 0 {
		return apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份")
	}
	tt, ok := ParseTargetType(targetType)
	if !ok {
		return apperr.Errorf(apperr.CodeValidationFailed, "靶子类型非法")
	}
	rt, ok := ParseReactionKind(kind)
	if !ok {
		return apperr.Errorf(apperr.CodeValidationFailed, "反应类型非法")
	}
	if targetID <= 0 {
		return apperr.Errorf(apperr.CodeValidationFailed, "靶子 id 非法")
	}
	// 靶子必须存在且未软删，否则会留下指向空对象的反应。
	// 嵌套资源调用时还要校验留言归属（见 ensureTarget 的 postID 分支）。
	if err := s.ensureTarget(ctx, tt, targetID, postID); err != nil {
		return err
	}
	if add {
		created, err := s.repo.AddReaction(ctx, &Reaction{
			TargetType: tt, TargetID: targetID, Kind: rt, UserID: actorID,
		})
		if err != nil {
			return err
		}
		// 只在「首次真正建立」时触发通知：重复点赞（幂等 no-op）不该再打扰帖主。
		// 收藏不产生通知（决策-通知派生规则.md Q1 的 5 个触发点不含收藏）。
		if created && rt == ReactionLike {
			ev := Actor{ActorID: actorID}
			switch tt {
			case TargetPost:
				ev.Action = "like:post"
				ev.PostID = targetID
			case TargetComment:
				ev.Action = "like:comment"
				ev.ReplyID = targetID
			}
			s.fire(ctx, ev)
		}
		return nil
	}
	_, err := s.repo.RemoveReaction(ctx, tt, targetID, rt, actorID)
	return err
}

// ensureTarget 确认靶子存在且未软删。
//
// postID > 0 时（嵌套资源调用）对 comment 追加归属校验：留言必须属于该帖子，
// 否则返回 not_found。这是「留言点赞忽略 :id」越权漏洞的修复判定点。
func (s *Service) ensureTarget(ctx context.Context, tt TargetType, id, postID int64) error {
	switch tt {
	case TargetPost:
		p, err := s.repo.FindByID(ctx, id)
		if err != nil {
			return err
		}
		if p.Deleted() {
			return apperr.Errorf(apperr.CodeNotFound, "帖子不存在")
		}
		return nil
	case TargetComment:
		r, err := s.repo.FindReplyByID(ctx, id)
		if err != nil {
			return err
		}
		if r.Deleted() {
			return apperr.Errorf(apperr.CodeNotFound, "留言不存在")
		}
		// 嵌套资源：留言必须属于 URL 里的父帖子。用 not_found 与「不存在」同码，
		// 不泄露「这条留言存在但不属于这个帖子」这类存在性差异。
		if postID > 0 && r.PostID != postID {
			return apperr.Errorf(apperr.CodeNotFound, "留言不存在")
		}
		return nil
	default:
		return apperr.Errorf(apperr.CodeValidationFailed, "靶子类型非法")
	}
}

// ReactionStateOf 取某用户对某靶子的反应状态（逐请求现算，不缓存）。
// viewerID 为 0 表示匿名，两个布尔均为 false。
func (s *Service) ReactionStateOf(ctx context.Context, tt TargetType, id, viewerID int64) ReactionState {
	if viewerID <= 0 {
		return ReactionState{}
	}
	st := ReactionState{}
	if ok, err := s.repo.HasReaction(ctx, tt, id, ReactionLike, viewerID); err == nil {
		st.LikedByMe = ok
	}
	if ok, err := s.repo.HasReaction(ctx, tt, id, ReactionFavorite, viewerID); err == nil {
		st.FavoritedByMe = ok
	}
	return st
}

// ReactionCounts 返回某靶子的点赞/收藏数（逐请求现算，不缓存）。
func (s *Service) ReactionCounts(ctx context.Context, tt TargetType, id int64) (likes, favorites int) {
	likes, favorites, _ = s.repo.ReactionCounts(ctx, tt, id)
	return
}

// ---------- 读操作（带短 TTL 缓存） ----------

// cachedList 是列表缓存里存的东西：只存领域行，不存 isMine 等 per-user 字段。
type cachedList struct {
	Items []*Post `json:"items"`
}

// cachedDetail 是详情缓存里存的东西：只存 Post + Replies，不存预签名 URL
// （URL 有短 TTL，必须逐请求现签，避免缓存把过期 URL 继续发出去）。
type cachedDetail struct {
	Post    *Post    `json:"post"`
	Replies []*Reply `json:"replies"`
}

// List 返回某板块（board 为空表示不过滤）的帖子列表，publishedAt 倒序 + 游标。
// List 返回帖子列表。
//
// authorID > 0 时只返回该作者自己的帖子（「我的帖子」）；0 表示公开列表。
// 两种形态共用同一套缓存，靠 authorID 进缓存键来隔离，见 listCacheKey。
func (s *Service) List(ctx context.Context, board Board, authorID int64, after PageCursor, limit int) (ListResult, error) {
	limit = normalizeLimit(limit)

	// mine 请求必须有明确作者；调用方（handler）已挡住未登录，这里再 fail-closed 一次：
	// 若 authorID 被传成 0 却走了 mine 分支，会退化成公开列表——那是隐私事故。
	byAuthor := authorID > 0

	fetch := func(ctx context.Context) ([]*Post, error) {
		if byAuthor {
			return s.repo.ListByAuthor(ctx, authorID, board, after, limit)
		}
		return s.repo.List(ctx, board, after, limit)
	}

	if s.cache != nil {
		key := listCacheKey(board, authorID, s.boardEpoch(board), after, limit)
		if raw, ok := s.cache.Get(ctx, key); ok {
			var cl cachedList
			if err := json.Unmarshal(raw, &cl); err == nil {
				return assembleList(cl.Items, limit), nil
			}
			// 缓存值损坏：当作未命中，继续回源（不把 5xx 抛给客户端）。
		} else {
			rows, err := fetch(ctx)
			if err != nil {
				return ListResult{}, err
			}
			if b, err := json.Marshal(cachedList{Items: rows}); err == nil {
				_ = s.cache.Set(ctx, key, b, DefaultCacheTTL)
			}
			return assembleList(rows, limit), nil
		}
	}

	rows, err := fetch(ctx)
	if err != nil {
		return ListResult{}, err
	}
	return assembleList(rows, limit), nil
}

// assembleList 从「已按序、已截断到 limit」的行组装列表结果与下一个游标。
func assembleList(rows []*Post, limit int) ListResult {
	var next *PageCursor
	if len(rows) == limit && len(rows) > 0 {
		last := rows[len(rows)-1]
		c := PageCursor{PublishedAt: last.PublishedAt, ID: last.ID}
		next = &c
	}
	return ListResult{Items: rows, Next: next}
}

// Get 返回帖子详情 + 回复 + 附件预签名 URL。软删的视为不存在。
func (s *Service) Get(ctx context.Context, id int64) (*Detail, error) {
	post, replies, err := s.loadDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	if post.Deleted() {
		return nil, apperr.Errorf(apperr.CodeNotFound, "帖子不存在")
	}
	urls := s.presign(ctx, post.Attachments)
	return &Detail{Post: post, Replies: replies, AttachmentURLs: urls}, nil
}

// loadDetail 走缓存拿 Post + Replies（不含软删过滤，由调用方判）。
func (s *Service) loadDetail(ctx context.Context, id int64) (*Post, []*Reply, error) {
	if s.cache != nil {
		key := detailCacheKey(id)
		if raw, ok := s.cache.Get(ctx, key); ok {
			var cd cachedDetail
			if err := json.Unmarshal(raw, &cd); err == nil && cd.Post != nil {
				return cd.Post, cd.Replies, nil
			}
		} else {
			post, replies, err := s.fetchDetail(ctx, id)
			if err != nil {
				return nil, nil, err
			}
			if b, err := json.Marshal(cachedDetail{Post: post, Replies: replies}); err == nil {
				_ = s.cache.Set(ctx, key, b, DefaultCacheTTL)
			}
			return post, replies, nil
		}
	}
	return s.fetchDetail(ctx, id)
}

func (s *Service) fetchDetail(ctx context.Context, id int64) (*Post, []*Reply, error) {
	post, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	replies, err := s.repo.ListReplies(ctx, id, MaxPageSize)
	if err != nil {
		return nil, nil, err
	}
	return post, replies, nil
}

// presign 逐个为附件 key 生成短 TTL 预签名 URL。
// 单个失败只跳过该 URL（旁路），不让整条帖子 500；Store 为 nil 时返回空。
func (s *Service) presign(ctx context.Context, keys []string) []string {
	urls := make([]string, 0, len(keys))
	if s.store == nil {
		return urls
	}
	for _, key := range keys {
		u, err := s.store.PresignGet(ctx, key, s.presignTTL)
		if err != nil {
			continue
		}
		urls = append(urls, u)
	}
	return urls
}

// AdminList 返回管理端列表（不过滤板块，含软删信息由 view 决定）。
func (s *Service) AdminList(ctx context.Context, after PageCursor, limit int) (ListResult, error) {
	limit = normalizeLimit(limit)
	rows, err := s.repo.ListAll(ctx, after, limit)
	if err != nil {
		return ListResult{}, err
	}
	return assembleList(rows, limit), nil
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return DefaultPageSize
	}
	if limit > MaxPageSize {
		return MaxPageSize
	}
	return limit
}

// FindPost 取一条帖子（含已软删），供装配层的通知派生查收件人用。
//
// 为什么需要一个「不判软删」的读口：通知派生要拿到帖主是谁，
// 而软删的帖子在派生时机上可能刚好被删（并发），此时仍应能算出收件人
// 并让编排层决定是否投递。判读可见性是 handler 的事，不是这里。
//
// 它是**只读、不做权限判定**的窄口：调用方只有装配层。
func (s *Service) FindPost(ctx context.Context, id int64) (*Post, error) {
	if id <= 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "帖子 id 非法")
	}
	return s.repo.FindByID(ctx, id)
}

// FindReply 取一条留言（含已软删），供通知派生查收件人。
func (s *Service) FindReply(ctx context.Context, id int64) (*Reply, error) {
	if id <= 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "留言 id 非法")
	}
	return s.repo.FindReplyByID(ctx, id)
}
