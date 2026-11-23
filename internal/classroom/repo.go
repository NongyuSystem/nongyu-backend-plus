package classroom

import (
	"context"
)

// Repo 是楼栋与路线的持久化接口。
//
// 接口在域内、实现放 internal/classroom/store（与其它域一致）。
type Repo interface {
	// ---- 楼栋 ----

	// CreateBuilding 插入楼栋并回填 ID。
	CreateBuilding(ctx context.Context, b *Building) error
	// UpdateBuilding 按 ID 覆盖可写字段（不含 Key：Key 由楼栋号+分区派生，
	// 改它会让「已有教室名」突然指向另一栋楼，故不允许直接改）。
	UpdateBuilding(ctx context.Context, b *Building) error
	// FindBuildingByKey 按归一化键查（不含已停用）。不存在返回 not_found。
	FindBuildingByKey(ctx context.Context, key string) (*Building, error)
	// FindBuildingByID 按 ID 查（含已停用，供管理端编辑）。
	FindBuildingByID(ctx context.Context, id int64) (*Building, error)
	// ListBuildings 返回楼栋列表，可按校区过滤（空表示不过滤）。
	// includeDisabled 供管理端使用。
	ListBuildings(ctx context.Context, campus string, includeDisabled bool) ([]*Building, error)
	// DeleteBuilding 硬删楼栋（级联删路线）。
	//
	// 为什么硬删而不是软删：楼栋是**参考数据**，不是用户内容。
	// 软删会让 building_key 的唯一约束永远占着，导致「删了又建同一栋」失败。
	// 若担心误删，应由管理端做二次确认，而不是用软删占住键。
	DeleteBuilding(ctx context.Context, id int64) error

	// ---- 路线 ----

	// CreateRoute 插入路线（Points 一并发）。ID 与点的归属由实现回填。
	CreateRoute(ctx context.Context, r *Route) error
	// UpdateRoute 覆盖路线字段并**整体替换**点集合。
	//
	// 整体替换而不是逐点 diff：路线点通常是手工录入的一小串，
	// 全量替换的语义最简单（前端提交完整点列表即可），
	// 也不会出现「客户端删了第 2 点、服务端按 seq 更新导致错位」。
	UpdateRoute(ctx context.Context, r *Route) error
	// FindRoute 按 ID 查路线（含点，按 seq 升序）。
	FindRoute(ctx context.Context, id int64) (*Route, error)
	// ListRoutes 返回某楼栋的全部路线（含点，按 sort_order、id 升序）。
	ListRoutes(ctx context.Context, buildingID int64, includeDisabled bool) ([]*Route, error)
	// DeleteRoute 硬删路线（级联删点）。
	DeleteRoute(ctx context.Context, id int64) error
}
