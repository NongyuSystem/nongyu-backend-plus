package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/classroom"
)

// BuildingMemoryRepo 是 classroom.Repo 的内存实现。
//
// 行为必须与 PG 版一致，尤其是：
//   - 楼栋键唯一；
//   - 删楼栋级联删路线与点；
//   - 路线点按 seq 升序返回。
type BuildingMemoryRepo struct {
	mu         sync.RWMutex
	buildings  map[int64]*classroom.Building
	routes     map[int64]*classroom.Route
	points     map[int64][]classroom.RoutePoint
	seqB, seqR int64
	nowFn      func() time.Time
}

// NewBuildingMemoryRepo 构造内存仓储。
func NewBuildingMemoryRepo(nowFn func() time.Time) *BuildingMemoryRepo {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &BuildingMemoryRepo{
		buildings: map[int64]*classroom.Building{},
		routes:    map[int64]*classroom.Route{},
		points:    map[int64][]classroom.RoutePoint{},
		nowFn:     nowFn,
	}
}

// CreateBuilding 实现 classroom.Repo。
func (r *BuildingMemoryRepo) CreateBuilding(_ context.Context, b *classroom.Building) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// 键唯一：内存版也要守，否则「两栋楼同键」会让查询结果不确定。
	for _, ex := range r.buildings {
		if ex.Key == b.Key {
			return apperr.Errorf(apperr.CodeValidationFailed, "楼栋键 %q 已存在", b.Key)
		}
	}
	r.seqB++
	b.ID = r.seqB
	r.buildings[b.ID] = cloneBuilding(b)
	return nil
}

// UpdateBuilding 实现 classroom.Repo。
func (r *BuildingMemoryRepo) UpdateBuilding(_ context.Context, b *classroom.Building) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.buildings[b.ID]; !ok {
		return apperr.Errorf(apperr.CodeNotFound, "楼栋不存在")
	}
	r.buildings[b.ID] = cloneBuilding(b)
	return nil
}

// FindBuildingByKey 实现 classroom.Repo（只返回启用的）。
func (r *BuildingMemoryRepo) FindBuildingByKey(_ context.Context, key string) (*classroom.Building, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, b := range r.buildings {
		if b.Key == key && b.Enabled {
			return cloneBuilding(b), nil
		}
	}
	return nil, apperr.Errorf(apperr.CodeNotFound, "楼栋不存在")
}

// FindBuildingByID 实现 classroom.Repo（含停用）。
func (r *BuildingMemoryRepo) FindBuildingByID(_ context.Context, id int64) (*classroom.Building, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.buildings[id]
	if !ok {
		return nil, apperr.Errorf(apperr.CodeNotFound, "楼栋不存在")
	}
	return cloneBuilding(b), nil
}

// ListBuildings 实现 classroom.Repo。
func (r *BuildingMemoryRepo) ListBuildings(_ context.Context, campus string, includeDisabled bool) ([]*classroom.Building, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*classroom.Building, 0, len(r.buildings))
	for _, b := range r.buildings {
		if !includeDisabled && !b.Enabled {
			continue
		}
		if campus != "" && b.Campus != campus {
			continue
		}
		out = append(out, cloneBuilding(b))
	}
	// 稳定排序：先按楼栋号（数字感知），再按分区。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].No != out[j].No {
			return numericLess(out[i].No, out[j].No)
		}
		return out[i].Zone < out[j].Zone
	})
	return out, nil
}

// DeleteBuilding 实现 classroom.Repo（级联删路线与点）。
func (r *BuildingMemoryRepo) DeleteBuilding(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.buildings[id]; !ok {
		return apperr.Errorf(apperr.CodeNotFound, "楼栋不存在")
	}
	delete(r.buildings, id)
	for rid, rt := range r.routes {
		if rt.BuildingID == id {
			delete(r.routes, rid)
			delete(r.points, rid)
		}
	}
	return nil
}

// CreateRoute 实现 classroom.Repo。
func (r *BuildingMemoryRepo) CreateRoute(_ context.Context, rt *classroom.Route) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.buildings[rt.BuildingID]; !ok {
		return apperr.Errorf(apperr.CodeNotFound, "楼栋不存在")
	}
	r.seqR++
	rt.ID = r.seqR
	r.routes[rt.ID] = cloneRoute(rt)
	r.points[rt.ID] = append([]classroom.RoutePoint(nil), rt.Points...)
	return nil
}

// UpdateRoute 实现 classroom.Repo（整体替换点集合）。
func (r *BuildingMemoryRepo) UpdateRoute(_ context.Context, rt *classroom.Route) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.routes[rt.ID]; !ok {
		return apperr.Errorf(apperr.CodeNotFound, "路线不存在")
	}
	r.routes[rt.ID] = cloneRoute(rt)
	r.points[rt.ID] = append([]classroom.RoutePoint(nil), rt.Points...)
	return nil
}

// FindRoute 实现 classroom.Repo。
func (r *BuildingMemoryRepo) FindRoute(_ context.Context, id int64) (*classroom.Route, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.routes[id]
	if !ok {
		return nil, apperr.Errorf(apperr.CodeNotFound, "路线不存在")
	}
	cp := cloneRoute(rt)
	cp.Points = append([]classroom.RoutePoint(nil), r.points[id]...)
	return cp, nil
}

// ListRoutes 实现 classroom.Repo。
func (r *BuildingMemoryRepo) ListRoutes(_ context.Context, buildingID int64, includeDisabled bool) ([]*classroom.Route, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*classroom.Route, 0, 4)
	for id, rt := range r.routes {
		if rt.BuildingID != buildingID {
			continue
		}
		if !includeDisabled && !rt.Enabled {
			continue
		}
		cp := cloneRoute(rt)
		cp.Points = append([]classroom.RoutePoint(nil), r.points[id]...)
		out = append(out, cp)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// DeleteRoute 实现 classroom.Repo。
func (r *BuildingMemoryRepo) DeleteRoute(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.routes[id]; !ok {
		return apperr.Errorf(apperr.CodeNotFound, "路线不存在")
	}
	delete(r.routes, id)
	delete(r.points, id)
	return nil
}

func cloneBuilding(b *classroom.Building) *classroom.Building {
	cp := *b
	if b.Lat != nil {
		v := *b.Lat
		cp.Lat = &v
	}
	if b.Lng != nil {
		v := *b.Lng
		cp.Lng = &v
	}
	if b.Aliases != nil {
		cp.Aliases = append([]string(nil), b.Aliases...)
	}
	return &cp
}

func cloneRoute(r *classroom.Route) *classroom.Route {
	cp := *r
	if r.Points != nil {
		cp.Points = append([]classroom.RoutePoint(nil), r.Points...)
	}
	return &cp
}

// numericLess 按数值比较两个「可能含非数字」的字符串。
//
// 楼栋号是 `1`、`2`、`10` 这种，字典序会把 `10` 排在 `2` 前面，
// 列表顺序看起来是随机乱序。这里按前导数字比较，非数字部分忽略。
func numericLess(a, b string) bool {
	na, nb := leadingInt(a), leadingInt(b)
	if na != nb {
		return na < nb
	}
	return a < b
}

func leadingInt(s string) int {
	n := 0
	seen := false
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		seen = true
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			break
		}
	}
	if !seen {
		return 1 << 30 // 无数字的排最后
	}
	return n
}

// 编译期确认。
var _ classroom.Repo = (*BuildingMemoryRepo)(nil)
