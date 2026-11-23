package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"nongyu-backend-plus/internal/apikey"
	"nongyu-backend-plus/internal/apperr"
)

// KeyMemoryRepo 是 apikey.Repo 的内存实现。
//
// 它存的同样是**密文**（Cipher 列），不存明文——内存实现也要遵守
// ADR-0002 的加密铁律，否则「只在测试里明文」会慢慢变成「生产也明文」。
type KeyMemoryRepo struct {
	mu   sync.RWMutex
	keys map[int64]*apikey.Key
	seq  int64
}

// NewKeyMemoryRepo 构造内存仓储。
func NewKeyMemoryRepo() *KeyMemoryRepo {
	return &KeyMemoryRepo{keys: map[int64]*apikey.Key{}}
}

// Create 实现 apikey.Repo。
func (r *KeyMemoryRepo) Create(_ context.Context, k *apikey.Key) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	k.ID = r.seq
	r.keys[k.ID] = cloneKey(k)
	return nil
}

// Update 实现 apikey.Repo。
func (r *KeyMemoryRepo) Update(_ context.Context, k *apikey.Key) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.keys[k.ID]
	if !ok {
		return apperr.Errorf(apperr.CodeNotFound, "密钥不存在")
	}
	cp := cloneKey(k)
	// 平台不可改：改平台等于换一把 key（高德按平台绑定）。
	cp.Platform = old.Platform
	r.keys[k.ID] = cp
	return nil
}

// FindByID 实现 apikey.Repo。
func (r *KeyMemoryRepo) FindByID(_ context.Context, id int64) (*apikey.Key, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.keys[id]
	if !ok {
		return nil, apperr.Errorf(apperr.CodeNotFound, "密钥不存在")
	}
	return cloneKey(k), nil
}

// List 实现 apikey.Repo（含停用）。
func (r *KeyMemoryRepo) List(_ context.Context, platform apikey.Platform) ([]*apikey.Key, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*apikey.Key, 0, len(r.keys))
	for _, k := range r.keys {
		if platform != "" && k.Platform != platform {
			continue
		}
		out = append(out, cloneKey(k))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ListEnabled 实现 apikey.Repo（只返回启用的，供轮换）。
func (r *KeyMemoryRepo) ListEnabled(_ context.Context, platform apikey.Platform) ([]*apikey.Key, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*apikey.Key, 0, len(r.keys))
	for _, k := range r.keys {
		if k.Platform != platform || !k.Enabled {
			continue
		}
		out = append(out, cloneKey(k))
	}
	// 按 ID 稳定排序：轮询顺序必须是确定的，否则「均匀消耗」无从保证。
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Delete 实现 apikey.Repo。
func (r *KeyMemoryRepo) Delete(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.keys[id]; !ok {
		return apperr.Errorf(apperr.CodeNotFound, "密钥不存在")
	}
	delete(r.keys, id)
	return nil
}

// MarkUsed 实现 apikey.Repo。
func (r *KeyMemoryRepo) MarkUsed(_ context.Context, id int64, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.keys[id]
	if !ok {
		return apperr.Errorf(apperr.CodeNotFound, "密钥不存在")
	}
	k.UsedCount++
	t := at
	k.LastUsedAt = &t
	return nil
}

func cloneKey(k *apikey.Key) *apikey.Key {
	cp := *k
	if k.Cipher != nil {
		cp.Cipher = append([]byte(nil), k.Cipher...)
	}
	if k.LastUsedAt != nil {
		v := *k.LastUsedAt
		cp.LastUsedAt = &v
	}
	return &cp
}

// 编译期确认。
var _ apikey.Repo = (*KeyMemoryRepo)(nil)
