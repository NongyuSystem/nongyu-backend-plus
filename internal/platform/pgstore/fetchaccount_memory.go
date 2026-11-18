package pgstore

// 取数账号池的进程内存实现（本地开发/测试用，与 PG 行为对齐）。
// 凭据不加密（数据只在进程内），但对外接口同样不回显明文。

import (
	"context"
	"sort"
	"sync"

	"nongyu-backend-plus/internal/school/jiaowu"
)

type memFetchAccount struct {
	a       jiaowu.FetchAccount
	fails   int
	deleted bool
}

// FetchAccountMemoryRepo 同时实现 Pool 与管理员 CRUD。
type FetchAccountMemoryRepo struct {
	mu     sync.Mutex
	nextID int64
	rows   map[int64]*memFetchAccount
	cursor uint64
}

// NewFetchAccountMemoryRepo 构造空的内存池。
func NewFetchAccountMemoryRepo() *FetchAccountMemoryRepo {
	return &FetchAccountMemoryRepo{rows: map[int64]*memFetchAccount{}}
}

func (r *FetchAccountMemoryRepo) Create(_ context.Context, a jiaowu.FetchAccount) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	id := r.nextID
	a.ID = id
	r.rows[id] = &memFetchAccount{a: a}
	return id, nil
}

func (r *FetchAccountMemoryRepo) Update(_ context.Context, a jiaowu.FetchAccount) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[a.ID]
	if !ok || row.deleted {
		return ErrFetchAccountNotFound
	}
	row.a = a
	row.fails = 0
	return nil
}

func (r *FetchAccountMemoryRepo) List(_ context.Context) ([]jiaowu.FetchAccount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []jiaowu.FetchAccount{}
	for _, row := range r.rows {
		if row.deleted {
			continue
		}
		// 不回显凭据
		meta := row.a
		meta.TGT, meta.CookieHeader, meta.VPNTicket = "", "", ""
		out = append(out, meta)
	}
	return out, nil
}

func (r *FetchAccountMemoryRepo) SetEnabled(_ context.Context, id int64, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[id]
	if !ok || row.deleted {
		return ErrFetchAccountNotFound
	}
	row.a.Enabled = enabled
	return nil
}

func (r *FetchAccountMemoryRepo) Delete(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[id]
	if !ok || row.deleted {
		return ErrFetchAccountNotFound
	}
	row.deleted = true
	return nil
}

func (r *FetchAccountMemoryRepo) Pick(_ context.Context) (jiaowu.FetchAccount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := []int64{}
	for id, row := range r.rows {
		if !row.deleted && row.a.Enabled {
			ids = append(ids, id)
		}
	}
	// 按 ID 升序，保证轮询可预测（与 PG 实现的 ORDER BY id 对齐）。
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) == 0 {
		return jiaowu.FetchAccount{}, jiaowu.ErrFetchAccountUnavailable
	}
	idx := int(r.cursor % uint64(len(ids)))
	r.cursor++
	return r.rows[ids[idx]].a, nil
}

func (r *FetchAccountMemoryRepo) MarkFailure(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if row, ok := r.rows[id]; ok && !row.deleted {
		row.fails++
		if row.fails >= 5 {
			row.a.Enabled = false
		}
	}
	return nil
}
