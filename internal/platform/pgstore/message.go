package pgstore

import (
	"context"
	"sort"
	"sync"
	"time"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/message"
)

// MessageMemoryRepo 是 message.Repo 的**内存实现**。
//
// 与 AnnouncementMemoryRepo 同理：哪怕只是内存实现，也放 platform 子包而不放
// 业务域内（README 架构约束第 4 条）。用途是装配层在无 DATABASE_URL 时让端点
// 可跑、以及业务域 HTTP 集成测不必起 PG。
//
// 它不是 PG 的等价物，但有一样东西必须与 PG 的部分唯一索引等价：
// **notifIdx 在 mu 保护下原子完成「查键 → 命中则合并 / 未命中则插行 + 记键」**，
// 从而并发 Emit 同键时也是合并成一行，绝不会产生两行（Spec 验收）。
type MessageMemoryRepo struct {
	mu   sync.RWMutex
	rows map[int64]*message.Message
	// notifIdx 是 kind=notification 部分唯一索引
	// (recipientId, sourceDomain, sourceObjectId) 的内存承载。
	// value 是对应行的 id；只在 Create/Upsert 的写锁内读写。
	notifIdx map[string]int64
	seq      int64
	nowFn    func() time.Time
}

// NewMessageMemoryRepo 构造内存仓储。nowFn 可为 nil（默认 time.Now）。
func NewMessageMemoryRepo(nowFn func() time.Time) *MessageMemoryRepo {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &MessageMemoryRepo{
		rows:     map[int64]*message.Message{},
		notifIdx: map[string]int64{},
		nowFn:    nowFn,
	}
}

// Create 实现 message.Repo：插入一条消息并回填 ID。
//
// 只供内部信使用；通知必须走 UpsertNotification（否则会绕开唯一键约束）。
func (r *MessageMemoryRepo) Create(_ context.Context, m *message.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	m.ID = r.seq
	if m.CreatedAt.IsZero() {
		m.CreatedAt = r.nowFn()
	}
	if m.Hits <= 0 {
		m.Hits = 1
	}
	r.rows[m.ID] = cloneMessage(m)
	return nil
}

// UpsertNotification 实现 message.Repo：按幂等键合并通知。
//
// 整段在**同一把写锁**内完成，因此并发的同键 Emit 会串行化：
// 第一个插入并登记 notifIdx，其余全部命中 idx 走 hits++ 分支，
// 最终只有一行（等价于 PG 的部分唯一索引 + ON CONFLICT DO UPDATE）。
//
// 合并语义（决策-消息域模型.md Q3 / 决策-通知派生规则.md Q4）：
//   - hits 累加 1；
//   - readAt 重置为 nil（变回未读）——「同一帖被反复点赞」要重新提醒；
//   - title/body 刷新为最新快照（触发方写入时落库的快照，D1）；
//   - createdAt **保持首次写入**（列表按 createdAt 倒序，合并不应把老通知顶到最前）。
//
// 已软删的行仍留在 idx 里：同键再次触发会被合并回同一行，而 deletedAt 保持
// 非空 → 对用户仍不可见、不计未读。这是「删了就不再被同一来源反复打扰」的
// 保守取向（若将来要「删后重新触发再出现」，须在 Spec 里另行裁定）。
func (r *MessageMemoryRepo) UpsertNotification(_ context.Context, key message.NotificationKey, title, body string, at time.Time) (*message.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	idxKey := key.String()
	if id, ok := r.notifIdx[idxKey]; ok {
		if m, ok := r.rows[id]; ok {
			m.Hits++
			m.ReadAt = nil
			m.Title = title
			m.Body = body
			return cloneMessage(m), nil
		}
		// 理论上不会出现（行未被物理删除）；真出现就按「重新插入」自愈。
		delete(r.notifIdx, idxKey)
	}

	r.seq++
	m := &message.Message{
		ID:             r.seq,
		RecipientID:    key.RecipientID,
		Kind:           message.KindNotification,
		SourceDomain:   key.SourceDomain,
		SourceObjectID: key.SourceObjectID,
		Title:          title,
		Body:           body,
		Hits:           1,
		CreatedAt:      at,
	}
	r.rows[m.ID] = m
	r.notifIdx[idxKey] = m.ID
	return cloneMessage(m), nil
}

// FindByID 实现 message.Repo。含已软删的行（归属判定与不泄露存在性由服务层做）。
func (r *MessageMemoryRepo) FindByID(_ context.Context, id int64) (*message.Message, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.rows[id]
	if !ok {
		return nil, apperr.Errorf(apperr.CodeNotFound, "消息不存在")
	}
	return cloneMessage(m), nil
}

// List 实现 message.Repo：只返回本人、未软删、命中 kind/仅未读过滤，
// createdAt 倒序（同刻按 ID 倒序），从 after 之后继续，最多 limit 条。
func (r *MessageMemoryRepo) List(_ context.Context, f message.ListFilter, after message.PageCursor, limit int) ([]*message.Message, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*message.Message, 0, len(r.rows))
	for _, m := range r.rows {
		if !message.MatchesFilter(m, f) {
			continue
		}
		if !message.AfterCursor(m, after) {
			continue
		}
		all = append(all, cloneMessage(m))
	}
	sort.SliceStable(all, func(i, j int) bool {
		return message.LessForOrder(all[i], all[j])
	})
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// UnreadCount 实现 message.Repo：count(readAt is null and deletedAt is null)。
//
// 注意 UnreadOnly 开关不改变结果（计数本身就只数未读行），但 kind 过滤仍生效，
// 与 GET /messages?kind=...&unread=true 保持同一份 ListFilter 口径。
func (r *MessageMemoryRepo) UnreadCount(_ context.Context, f message.ListFilter) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	n := 0
	for _, m := range r.rows {
		if m.RecipientID != f.RecipientID || m.DeletedAt != nil {
			continue
		}
		if f.Kind != "" && m.Kind != f.Kind {
			continue
		}
		if m.ReadAt != nil {
			continue
		}
		n++
	}
	return n, nil
}

// MarkRead 实现 message.Repo：单条置已读。已读/已删幂等成功。
func (r *MessageMemoryRepo) MarkRead(_ context.Context, id int64, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.rows[id]
	if !ok {
		return apperr.Errorf(apperr.CodeNotFound, "消息不存在")
	}
	if m.DeletedAt != nil || m.ReadAt != nil {
		return nil
	}
	t := at
	m.ReadAt = &t
	return nil
}

// MarkAllRead 实现 message.Repo：按筛选条件批量置已读，返回受影响行数。
//
// 只作用于「该收件人 + 未软删 + 未读 + 命中 kind」的行；UnreadOnly 开关
// 不改变结果集（目标本来就是未读行），仅为与列表/未读数共用 ListFilter。
func (r *MessageMemoryRepo) MarkAllRead(_ context.Context, f message.ListFilter, at time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for _, m := range r.rows {
		if m.RecipientID != f.RecipientID || m.DeletedAt != nil || m.ReadAt != nil {
			continue
		}
		if f.Kind != "" && m.Kind != f.Kind {
			continue
		}
		t := at
		m.ReadAt = &t
		n++
	}
	return n, nil
}

// MarkDeleted 实现 message.Repo：软删一条。已删视为成功（幂等）。
func (r *MessageMemoryRepo) MarkDeleted(_ context.Context, id int64, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.rows[id]
	if !ok {
		return apperr.Errorf(apperr.CodeNotFound, "消息不存在")
	}
	if m.DeletedAt != nil {
		return nil
	}
	t := at
	m.DeletedAt = &t
	return nil
}

// cloneMessage 深拷贝一条消息，避免调用方改到仓储内部状态
// （尤其 readAt / deletedAt 指针，以及将来可能加的切片字段）。
func cloneMessage(m *message.Message) *message.Message {
	cp := *m
	if m.ReadAt != nil {
		t := *m.ReadAt
		cp.ReadAt = &t
	}
	if m.DeletedAt != nil {
		t := *m.DeletedAt
		cp.DeletedAt = &t
	}
	return &cp
}

// 编译期确认。
var _ message.Repo = (*MessageMemoryRepo)(nil)
