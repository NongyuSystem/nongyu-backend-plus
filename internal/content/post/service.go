package post

import (
	"context"
	"time"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/platform"
)

// Cache 是帖子域需要的缓存能力（只 Get/Set/Delete，不暴露 Redis 语义）。
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
}

// Options 是构造 Service 的依赖。
type Options struct {
	// Repo 必填（缺失即 panic：依赖缺失应在装配期暴露）。
	Repo Repo
	// Clock 可为 nil，默认真实时钟。
	Clock platform.Clock
	// Store 可选：附件对象存储。nil 时详情不返回预签名 URL、软删不清理对象。
	Store platform.ObjectStore
	// Cache 可选：列表与详情的短 TTL 缓存。nil 时退化为直查。
	Cache Cache
	// PresignTTL 覆盖 DefaultPresignTTL。
	PresignTTL time.Duration
	// CacheTTL 覆盖 DefaultCacheTTL。
	CacheTTL time.Duration
}

// Service 是帖子业务逻辑。不感知 HTTP，不直接碰 SQL。
type Service struct {
	repo       Repo
	clock      platform.Clock
	store      platform.ObjectStore
	cache      Cache
	presignTTL time.Duration
	cacheTTL   time.Duration
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
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = DefaultCacheTTL
	}
	return &Service{
		repo: opts.Repo, clock: opts.Clock, store: opts.Store,
		cache: opts.Cache, presignTTL: opts.PresignTTL, cacheTTL: opts.CacheTTL,
	}
}

// CreateInput 是发帖入参。
type CreateInput struct {
	Board       string
	Title       string
	Body        string
	Attachments []string
}

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

	now := s.clock.Now()
	p := &Post{
		Board: board, Title: in.Title, Body: in.Body,
		Attachments: in.Attachments, AuthorID: authorID,
		PublishedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.Create(ctx, p); err != nil {
		return nil, err
	}
	s.invalidateBoard(ctx, board)
	return p, nil
}

// List 返回某板块的帖子列表（boardStr 为空表示不过滤）。
func (s *Service) List(ctx context.Context, boardStr string, after PageCursor, limit int) ([]*Post, *PageCursor, error) {
	var board Board
	if boardStr != "" {
		b, err := ValidateBoard(boardStr)
		if err != nil {
			return nil, nil, err
		}
		board = b
	}
	limit = normalizeLimit(limit)

	rows, err := s.repo.List(ctx, board, after, limit)
	if err != nil {
		return nil, nil, err
	}
	var next *PageCursor
	if len(rows) == limit && len(rows) > 0 {
		last := rows[len(rows)-1]
		next = &PageCursor{PublishedAt: last.PublishedAt, ID: last.ID}
	}
	return rows, next, nil
}

// Detail 是一条帖子的详情视图（附件已换成预签名 URL）。
type Detail struct {
	Post    *Post
	Replies []*Reply
	// AttachmentURLs 与 Post.Attachments 一一对应（Store 为 nil 时为空）。
	AttachmentURLs []string
}

// Get 返回帖子详情 + 留言。
//
// 软删的帖子对读者不可见（返回 not_found，与不存在同码，不泄露存在性）。
func (s *Service) Get(ctx context.Context, id int64) (*Detail, error) {
	if id <= 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "帖子 id 非法")
	}
	p, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Deleted() {
		return nil, apperr.Errorf(apperr.CodeNotFound, "帖子不存在")
	}
	replies, err := s.repo.ListReplies(ctx, id, MaxPageSize)
	if err != nil {
		return nil, err
	}
	urls := s.presignAll(ctx, p.Attachments)
	return &Detail{Post: p, Replies: replies, AttachmentURLs: urls}, nil
}

// presignAll 为附件批量签名。
//
// 单个附件签名失败不该让整条帖子 500：跳过该 URL 即可，前端看到少一张图
// 远好于整页打不开。
func (s *Service) presignAll(ctx context.Context, keys []string) []string {
	if s.store == nil || len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		u, err := s.store.PresignGet(ctx, k, s.presignTTL)
		if err != nil {
			continue
		}
		out = append(out, u)
	}
	return out
}

// UpdateInput 是编辑入参（指针语义：nil 表示不改）。
type UpdateInput struct {
	Title       *string
	Body        *string
	Attachments *[]string
}

// Update 编辑一条帖子。仅作者、且必须在编辑窗口内。
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
	// 非作者返回 not_found 而不是 forbidden：避免用状态码差异探测帖子是否存在。
	if p.AuthorID != actorID {
		return nil, apperr.Errorf(apperr.CodeNotFound, "帖子不存在")
	}
	now := s.clock.Now()
	if !WithinEditWindow(p.PublishedAt, now) {
		return nil, apperr.Errorf(apperr.CodeLocked,
			"超过 %d 小时编辑窗口", int(EditWindow.Hours()))
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
		p.Attachments = *in.Attachments
	}
	t := now
	p.EditedAt = &t
	p.UpdatedAt = now
	if err := s.repo.Update(ctx, p); err != nil {
		return nil, err
	}
	s.invalidateBoard(ctx, p.Board)
	return p, nil
}

// Delete 软删一条帖子（仅作者），并异步清理附件对象。
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
	return s.softDelete(ctx, p)
}

// AdminDelete 是管理端软删（不校验作者）。
func (s *Service) AdminDelete(ctx context.Context, id int64) error {
	p, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if p.Deleted() {
		return nil
	}
	return s.softDelete(ctx, p)
}

// softDelete 是两条删除路径的公共尾部。
func (s *Service) softDelete(ctx context.Context, p *Post) error {
	if err := s.repo.MarkDeleted(ctx, p.ID, s.clock.Now()); err != nil {
		return err
	}
	s.invalidateBoard(ctx, p.Board)
	s.cleanupAttachments(ctx, p.Attachments)
	return nil
}

// cleanupAttachments 异步清理附件对象。
//
// 旁路语义（Spec：「失败只记日志 + 计数，不阻塞软删」）：用 goroutine，
// 丢弃错误。对象存储失败不该影响「软删已经成功」这件事。
func (s *Service) cleanupAttachments(ctx context.Context, keys []string) {
	if s.store == nil || len(keys) == 0 {
		return
	}
	// 用独立 context：请求 context 在响应返回后会被 cancel。
	bg := context.WithoutCancel(ctx)
	go func() {
		for _, k := range keys {
			_ = s.store.Delete(bg, k)
		}
	}()
}

// AddReply 发一条留言。
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
	now := s.clock.Now()
	r := &Reply{PostID: postID, Body: body, PublishedAt: now}
	if err := s.repo.CreateReply(ctx, r); err != nil {
		return nil, err
	}
	s.invalidateBoard(ctx, p.Board)
	return r, nil
}

// React 建立或取消一条点赞/收藏。
func (s *Service) React(ctx context.Context, actorID int64, targetTypeStr string, targetID int64, kindStr string, add bool) error {
	if actorID <= 0 {
		return apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份")
	}
	tt, ok := ParseTargetType(targetTypeStr)
	if !ok {
		return apperr.Errorf(apperr.CodeValidationFailed, "靶子类型非法")
	}
	kind, ok := ParseReactionKind(kindStr)
	if !ok {
		return apperr.Errorf(apperr.CodeValidationFailed, "反应类型非法")
	}
	if targetID <= 0 {
		return apperr.Errorf(apperr.CodeValidationFailed, "靶子 id 非法")
	}
	// 靶子必须存在，否则会留下指向空对象的反应。
	if err := s.ensureTarget(ctx, tt, targetID); err != nil {
		return err
	}
	if add {
		_, err := s.repo.AddReaction(ctx, &Reaction{
			TargetType: tt, TargetID: targetID, Kind: kind, UserID: actorID,
		})
		return err
	}
	_, err := s.repo.RemoveReaction(ctx, kind, tt, targetID, actorID)
	return err
}

// ensureTarget 确认靶子存在且未软删。
func (s *Service) ensureTarget(ctx context.Context, tt TargetType, id int64) error {
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
		return nil
	default:
		return apperr.Errorf(apperr.CodeValidationFailed, "靶子类型非法")
	}
}

// ReactionState 是某靶子的反应状态（供详情页展示「我是否已赞」）。
type ReactionState struct {
	Likes     int  `json:"likes"`
	Favorites int  `json:"favorites"`
	LikedByMe bool `json:"likedByMe"`
	FavedByMe bool `json:"favedByMe"`
}

// ReactionStateOf 取某靶子的反应状态；viewerID 为 0 表示未登录。
func (s *Service) ReactionStateOf(ctx context.Context, ttStr string, id, viewerID int64) (ReactionState, error) {
	tt, ok := ParseTargetType(ttStr)
	if !ok {
		return ReactionState{}, apperr.Errorf(apperr.CodeValidationFailed, "靶子类型非法")
	}
	likes, favs, err := s.repo.ReactionCounts(ctx, tt, id)
	if err != nil {
		return ReactionState{}, err
	}
	st := ReactionState{Likes: likes, Favorites: favs}
	if viewerID > 0 {
		if ok, err := s.repo.HasReaction(ctx, ReactionLike, tt, id, viewerID); err == nil {
			st.LikedByMe = ok
		}
		if ok, err := s.repo.HasReaction(ctx, ReactionFavorite, tt, id, viewerID); err == nil {
			st.FavedByMe = ok
		}
	}
	return st, nil
}

// invalidateBoard 删除某板块的列表缓存（写操作后调用）。
//
// Spec：缓存只给 GET /posts 与 /posts/:id；写操作删对应 board 键；
// per-user 列表与点赞数不缓存。
func (s *Service) invalidateBoard(ctx context.Context, board Board) {
	if s.cache == nil {
		return
	}
	_ = s.cache.Delete(ctx, CacheKeyBoard(board))
}

// CacheKeyBoard 是板块列表缓存键。
func CacheKeyBoard(board Board) string { return "posts:board:" + string(board) }

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
