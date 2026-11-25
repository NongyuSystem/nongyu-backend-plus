package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/classroom"
)

// MapMemoryRepo 是 classroom.MapRepo 的内存实现。
//
// 行为必须与 PG 版一致，尤其是：
//   - 一校区一图（campus 唯一）；
//   - UpsertMapImage 只改「图」相关字段，**不动换算参数**；
//   - SetCalibration 在校区行不存在时先建一行（只有参数、没有图）。
type MapMemoryRepo struct {
	mu    sync.RWMutex
	maps  map[classroom.Campus]*classroom.Map
	seqID int64
	nowFn func() time.Time
}

// NewMapMemoryRepo 构造内存仓储。
func NewMapMemoryRepo(nowFn func() time.Time) *MapMemoryRepo {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &MapMemoryRepo{
		maps:  map[classroom.Campus]*classroom.Map{},
		nowFn: nowFn,
	}
}

// cloneMap 返回浅拷贝（Cal 也拷一份，避免调用方改到仓储内部状态）。
func cloneMap(m *classroom.Map) *classroom.Map {
	if m == nil {
		return nil
	}
	cp := *m
	if m.Cal != nil {
		cal := *m.Cal
		cp.Cal = &cal
	}
	return &cp
}

// FindMapByCampus 实现 classroom.MapRepo。
func (r *MapMemoryRepo) FindMapByCampus(ctx context.Context, campus classroom.Campus) (*classroom.Map, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.maps[campus]
	if !ok || !m.Enabled {
		// 与 PG 版同一口径：不存在与已停用对调用方等价。
		return nil, apperr.Errorf(apperr.CodeNotFound, "该校区暂无地图")
	}
	return cloneMap(m), nil
}

// ListMaps 实现 classroom.MapRepo（含停用，供管理端）。
func (r *MapMemoryRepo) ListMaps(ctx context.Context) ([]*classroom.Map, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*classroom.Map, 0, len(r.maps))
	for _, m := range r.maps {
		out = append(out, cloneMap(m))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Campus < out[j].Campus })
	return out, nil
}

// UpsertMapImage 实现 classroom.MapRepo。
//
// 只覆盖「图」相关字段；换算参数与标定说明原样保留——换图不等于重新标定。
func (r *MapMemoryRepo) UpsertMapImage(ctx context.Context, m *classroom.Map) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.nowFn()
	if old, ok := r.maps[m.Campus]; ok {
		old.ObjectKey = m.ObjectKey
		old.ContentType = m.ContentType
		old.SizeBytes = m.SizeBytes
		old.ETag = m.ETag
		old.Width = m.Width
		old.Height = m.Height
		old.CoordSystem = m.CoordSystem
		old.UpdatedAt = now
		m.ID = old.ID
		if old.Cal != nil {
			cal := *old.Cal
			m.Cal = &cal
		}
		m.CalibrationNote = old.CalibrationNote
		return nil
	}

	r.seqID++
	m.ID = r.seqID
	m.CreatedAt, m.UpdatedAt = now, now
	r.maps[m.Campus] = cloneMap(m)
	return nil
}

// SetCalibration 实现 classroom.MapRepo。
func (r *MapMemoryRepo) SetCalibration(
	ctx context.Context, campus classroom.Campus, cal *classroom.Calibration, note string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.nowFn()
	m, ok := r.maps[campus]
	if !ok {
		// 只有参数、没有图：与 PG 版的插入分支一致。
		r.seqID++
		cp := *cal
		r.maps[campus] = &classroom.Map{
			ID: r.seqID, Campus: campus,
			ObjectKey: "", ContentType: "image/gif",
			Width: 0, Height: 0, CoordSystem: "GCJ-02",
			Cal: &cp, CalibrationNote: note,
			Enabled:   true,
			CreatedAt: now, UpdatedAt: now,
		}
		return nil
	}
	cp := *cal
	m.Cal = &cp
	m.CalibrationNote = note
	m.UpdatedAt = now
	return nil
}
