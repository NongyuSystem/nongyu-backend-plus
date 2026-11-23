package store

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/content/post"
)

// PostMemoryRepo 是 post.Repo 的内存实现。
//
// 放 platform 子包而非业务域内（README 架构约束第 4 条），与
// AnnouncementMemoryRepo / MessageMemoryRepo 同构。
//
// 反应表的唯一性（同一用户 + 同一靶子 + 同一种反应只算一次）在这里用
// map 键 + 互斥锁实现——对应 PG 的复合唯一索引，是「取消即删行、
// 重复点赞不叠加」这条语义的物理承载。
type PostMemoryRepo struct {
	mu       sync.Mutex
	posts    map[int64]*post.Post
	replies  map[int64]*post.Reply
	reaction map[string]*post.Reaction
	seqPost  int64
	seqReply int64
	nowFn    func() time.Time
}

// NewPostMemoryRepo 构造内存仓储。nowFn 可为 nil。
func NewPostMemoryRepo(nowFn func() time.Time) *PostMemoryRepo {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &PostMemoryRepo{
		posts:    map[int64]*post.Post{},
		replies:  map[int64]*post.Reply{},
		reaction: map[string]*post.Reaction{},
		nowFn:    nowFn,
	}
}

// reactionKey 是反应唯一键：同一用户 + 同一靶子 + 同一种反应。
//
// 用 \x00 分隔而不是 ':'：靶子 id 是数字不会含分隔符，但类型/种类是外部
// 传入的字符串，用可见字符拼接会给「看起来不同实则应合并」的组合留缝隙。
func reactionKey(rt post.ReactionKind, tt post.TargetType, targetID, userID int64) string {
	return string(rt) + "\x00" + string(tt) + "\x00" +
		itoa64(targetID) + "\x00" + itoa64(userID)
}

// Create 实现 post.Repo。
func (r *PostMemoryRepo) Create(_ context.Context, p *post.Post) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seqPost++
	p.ID = r.seqPost
	now := r.nowFn()
	if p.PublishedAt.IsZero() {
		p.PublishedAt = now
	}
	p.CreatedAt, p.UpdatedAt = now, now
	r.posts[p.ID] = clonePost(p)
	return nil
}

// Update 实现 post.Repo。
func (r *PostMemoryRepo) Update(_ context.Context, p *post.Post) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.posts[p.ID]
	if !ok || old.DeletedAt != nil {
		return apperr.Errorf(apperr.CodeNotFound, "帖子不存在")
	}
	cp := clonePost(p)
	cp.CreatedAt = old.CreatedAt
	cp.UpdatedAt = r.nowFn()
	r.posts[p.ID] = cp
	// **回写到入参**：调用方（服务层）随后会把 p 直接当响应返回，
	// 只更新副本会让客户端拿到旧 updatedAt，看起来像「保存没生效」。
	p.CreatedAt, p.UpdatedAt = cp.CreatedAt, cp.UpdatedAt
	return nil
}

// FindByID 实现 post.Repo（含已软删行）。
func (r *PostMemoryRepo) FindByID(_ context.Context, id int64) (*post.Post, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[id]
	if !ok {
		return nil, apperr.Errorf(apperr.CodeNotFound, "帖子不存在")
	}
	return clonePost(p), nil
}

// List 实现 post.Repo。
func (r *PostMemoryRepo) List(_ context.Context, board post.Board, after post.PageCursor, limit int) ([]*post.Post, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]*post.Post, 0, len(r.posts))
	for _, p := range r.posts {
		if p.DeletedAt != nil {
			continue
		}
		if board != "" && p.Board != board {
			continue
		}
		if !post.AfterCursor(p, after) {
			continue
		}
		out = append(out, clonePost(p))
	}
	sort.SliceStable(out, func(i, j int) bool { return post.LessForOrder(out[i], out[j]) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ListByAuthor 实现 post.Repo（「我的帖子」：按作者过滤，board 为空表示跨板块）。
//
// 与 List 的差别只有 authorID 一个条件，但单列方法而不是加可选参数：
// 见 repo 接口处的说明——参数化会让「漏传 0 = 看到全站」成为静默越权。
func (r *PostMemoryRepo) ListByAuthor(_ context.Context, authorID int64, board post.Board, after post.PageCursor, limit int) ([]*post.Post, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]*post.Post, 0, len(r.posts))
	for _, p := range r.posts {
		if p.DeletedAt != nil {
			continue
		}
		if p.AuthorID != authorID {
			continue
		}
		if board != "" && p.Board != board {
			continue
		}
		if !post.AfterCursor(p, after) {
			continue
		}
		out = append(out, clonePost(p))
	}
	sort.SliceStable(out, func(i, j int) bool { return post.LessForOrder(out[i], out[j]) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ListByDetail 实现 post.Repo（按 detail 字段精确匹配）。
//
// 匹配规则与 PG 版保持一致：**大小写不敏感**的字符串相等。
// 用 strings.EqualFold 而不是 ==：教师名/课程名是人工录入的自然语言，
// 「王老师」与「王老师」不该因为大小写或全角差异而查不到（虽然中文
// 无大小写，但课程名常含英文，如「C语言」与「c语言」）。
func (r *PostMemoryRepo) ListByDetail(_ context.Context, board post.Board, match map[string]string, after post.PageCursor, limit int) ([]*post.Post, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]*post.Post, 0, len(r.posts))
	for _, p := range r.posts {
		if p.DeletedAt != nil {
			continue
		}
		if board != "" && p.Board != board {
			continue
		}
		if !detailMatches(p.Detail, match) {
			continue
		}
		if !post.AfterCursor(p, after) {
			continue
		}
		out = append(out, clonePost(p))
	}
	sort.SliceStable(out, func(i, j int) bool { return post.LessForOrder(out[i], out[j]) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// detailMatches 判断 detail 是否满足全部条件（AND）。
func detailMatches(detail map[string]any, match map[string]string) bool {
	for k, want := range match {
		raw, ok := detail[k]
		if !ok {
			return false
		}
		s, ok := raw.(string)
		if !ok {
			return false // 只支持字符串字段的匹配
		}
		if !strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(want)) {
			return false
		}
	}
	return true
}

// ListAll 实现 post.Repo（管理端：跨板块）。
func (r *PostMemoryRepo) ListAll(_ context.Context, after post.PageCursor, limit int) ([]*post.Post, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]*post.Post, 0, len(r.posts))
	for _, p := range r.posts {
		if p.DeletedAt != nil {
			continue
		}
		if !post.AfterCursor(p, after) {
			continue
		}
		out = append(out, clonePost(p))
	}
	sort.SliceStable(out, func(i, j int) bool { return post.LessForOrder(out[i], out[j]) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MarkDeleted 实现 post.Repo。
func (r *PostMemoryRepo) MarkDeleted(_ context.Context, id int64, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[id]
	if !ok {
		return apperr.Errorf(apperr.CodeNotFound, "帖子不存在")
	}
	if p.DeletedAt != nil {
		return nil // 幂等
	}
	t := at
	p.DeletedAt = &t
	p.UpdatedAt = at
	return nil
}

// CreateReply 实现 post.Repo。
func (r *PostMemoryRepo) CreateReply(_ context.Context, rp *post.Reply) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seqReply++
	rp.ID = r.seqReply
	if rp.PublishedAt.IsZero() {
		rp.PublishedAt = r.nowFn()
	}
	r.replies[rp.ID] = cloneReply(rp)
	return nil
}

// ListReplies 实现 post.Repo（publishedAt 升序）。
func (r *PostMemoryRepo) ListReplies(_ context.Context, postID int64, limit int) ([]*post.Reply, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]*post.Reply, 0, 8)
	for _, rp := range r.replies {
		if rp.PostID != postID || rp.DeletedAt != nil {
			continue
		}
		out = append(out, cloneReply(rp))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].PublishedAt.Equal(out[j].PublishedAt) {
			return out[i].PublishedAt.Before(out[j].PublishedAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// FindReplyByID 实现 post.Repo。
func (r *PostMemoryRepo) FindReplyByID(_ context.Context, id int64) (*post.Reply, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rp, ok := r.replies[id]
	if !ok {
		return nil, apperr.Errorf(apperr.CodeNotFound, "留言不存在")
	}
	return cloneReply(rp), nil
}

// AddReaction 实现 post.Repo（同键幂等）。
func (r *PostMemoryRepo) AddReaction(_ context.Context, rn *post.Reaction) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := reactionKey(rn.Kind, rn.TargetType, rn.TargetID, rn.UserID)
	if _, exists := r.reaction[key]; exists {
		return false, nil // 已赞过：幂等 no-op
	}
	cp := *rn
	r.reaction[key] = &cp
	return true, nil
}

// RemoveReaction 实现 post.Repo。
func (r *PostMemoryRepo) RemoveReaction(_ context.Context, tt post.TargetType, targetID int64, rt post.ReactionKind, userID int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := reactionKey(rt, tt, targetID, userID)
	if _, exists := r.reaction[key]; !exists {
		return false, nil
	}
	delete(r.reaction, key)
	return true, nil
}

// ReactionCounts 实现 post.Repo。
func (r *PostMemoryRepo) ReactionCounts(_ context.Context, tt post.TargetType, targetID int64) (int, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var likes, favs int
	for _, rn := range r.reaction {
		if rn.TargetType != tt || rn.TargetID != targetID {
			continue
		}
		switch rn.Kind {
		case post.ReactionLike:
			likes++
		case post.ReactionFavorite:
			favs++
		}
	}
	return likes, favs, nil
}

// HasReaction 实现 post.Repo。
func (r *PostMemoryRepo) HasReaction(_ context.Context, tt post.TargetType, targetID int64, rt post.ReactionKind, userID int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.reaction[reactionKey(rt, tt, targetID, userID)]
	return ok, nil
}

// ---------- 深拷贝 ----------

func clonePost(p *post.Post) *post.Post {
	cp := *p
	if p.EditedAt != nil {
		t := *p.EditedAt
		cp.EditedAt = &t
	}
	if p.DeletedAt != nil {
		t := *p.DeletedAt
		cp.DeletedAt = &t
	}
	if p.Attachments != nil {
		cp.Attachments = append([]string(nil), p.Attachments...)
	}
	// detail 必须深拷贝：它是 map，浅拷贝会让调用方改到仓储内部状态
	// （内存实现里这类 bug 极难发现——数据会「莫名其妙」被改掉）。
	if p.Detail != nil {
		cp.Detail = make(map[string]any, len(p.Detail))
		for k, v := range p.Detail {
			cp.Detail[k] = v
		}
	}
	return &cp
}

func cloneReply(rp *post.Reply) *post.Reply {
	cp := *rp
	if rp.DeletedAt != nil {
		t := *rp.DeletedAt
		cp.DeletedAt = &t
	}
	return &cp
}

// itoa64 是极简整数转字符串，避免为一个小转换引入 strconv 到本文件。
func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// 编译期确认。
var _ post.Repo = (*PostMemoryRepo)(nil)
