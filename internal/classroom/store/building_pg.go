package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/classroom"
)

// BuildingPGRepo 是 classroom.Repo 的 PostgreSQL 实现。
type BuildingPGRepo struct {
	pool  *pgxpool.Pool
	nowFn func() time.Time
}

// NewBuildingPGRepo 构造 PG 仓储。
func NewBuildingPGRepo(pool *pgxpool.Pool, nowFn func() time.Time) *BuildingPGRepo {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &BuildingPGRepo{pool: pool, nowFn: nowFn}
}

const buildingCols = `
	id, building_key, building_no, zone, display_name, aliases, campus,
	latitude, longitude, map_px, map_py, address_text, note, enabled,
	created_at, updated_at`

// CreateBuilding 实现 classroom.Repo。
func (r *BuildingPGRepo) CreateBuilding(ctx context.Context, b *classroom.Building) error {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO classroom_building
			(building_key, building_no, zone, display_name, aliases, campus,
			 latitude, longitude, map_px, map_py, address_text, note, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING id, created_at, updated_at`,
		b.Key, b.No, b.Zone, b.DisplayName, b.Aliases, b.Campus,
		b.Lat, b.Lng, b.MapPx, b.MapPy, b.AddressText, b.Note, b.Enabled,
	).Scan(&b.ID, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		// 唯一约束冲突翻译成 400：这是管理员重复录入，不是服务端故障。
		if isUniqueViolation(err) {
			return apperr.Errorf(apperr.CodeValidationFailed, "楼栋键 %q 已存在", b.Key)
		}
		return err
	}
	return nil
}

// UpdateBuilding 实现 classroom.Repo（不更新 building_key）。
func (r *BuildingPGRepo) UpdateBuilding(ctx context.Context, b *classroom.Building) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE classroom_building SET
			display_name = $2, aliases = $3, campus = $4,
			latitude = $5, longitude = $6, map_px = $7, map_py = $8,
			address_text = $9, note = $10,
			enabled = $11, updated_at = $12
		 WHERE id = $1`,
		b.ID, b.DisplayName, b.Aliases, b.Campus,
		b.Lat, b.Lng, b.MapPx, b.MapPy, b.AddressText, b.Note, b.Enabled, r.nowFn())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.Errorf(apperr.CodeNotFound, "楼栋不存在")
	}
	b.UpdatedAt = r.nowFn()
	return nil
}

// FindBuildingByKey 实现 classroom.Repo（只返回启用的）。
func (r *BuildingPGRepo) FindBuildingByKey(ctx context.Context, key string) (*classroom.Building, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+buildingCols+` FROM classroom_building WHERE building_key = $1 AND enabled`, key)
	b, err := scanBuilding(row)
	if err == pgx.ErrNoRows {
		return nil, apperr.Errorf(apperr.CodeNotFound, "楼栋不存在")
	}
	return b, err
}

// FindBuildingByID 实现 classroom.Repo（含停用）。
func (r *BuildingPGRepo) FindBuildingByID(ctx context.Context, id int64) (*classroom.Building, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+buildingCols+` FROM classroom_building WHERE id = $1`, id)
	b, err := scanBuilding(row)
	if err == pgx.ErrNoRows {
		return nil, apperr.Errorf(apperr.CodeNotFound, "楼栋不存在")
	}
	return b, err
}

// ListBuildings 实现 classroom.Repo。
//
// 排序在 SQL 里做：`building_no` 是 TEXT，直接用字典序会把 `10` 排在 `2`
// 前（列表看起来乱序）。这里用 `(building_no)::int` 做数值序，
// 但要防非数字楼栋号导致 cast 失败——用正则守卫，非数字的排最后。
func (r *BuildingPGRepo) ListBuildings(ctx context.Context, campus string, includeDisabled bool) ([]*classroom.Building, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+buildingCols+` FROM classroom_building
		 WHERE ($1 = '' OR campus = $1)
		   AND ($2 OR enabled)
		 ORDER BY
		   CASE WHEN building_no ~ '^[0-9]+$' THEN building_no::int ELSE 2147483647 END,
		   zone, id`,
		campus, includeDisabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectBuildings(rows)
}

// DeleteBuilding 实现 classroom.Repo（级联由外键 ON DELETE CASCADE 完成）。
func (r *BuildingPGRepo) DeleteBuilding(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM classroom_building WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.Errorf(apperr.CodeNotFound, "楼栋不存在")
	}
	return nil
}

// CreateRoute 实现 classroom.Repo（路线 + 点在同一事务里写）。
func (r *BuildingPGRepo) CreateRoute(ctx context.Context, rt *classroom.Route) error {
	return r.withTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO classroom_route
				(building_id, title, description, start_label, recommended, sort_order, enabled)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			RETURNING id, created_at, updated_at`,
			rt.BuildingID, rt.Title, rt.Description, rt.StartLabel,
			rt.Recommended, rt.SortOrder, rt.Enabled,
		).Scan(&rt.ID, &rt.CreatedAt, &rt.UpdatedAt); err != nil {
			if isForeignKeyViolation(err) {
				return apperr.Errorf(apperr.CodeNotFound, "楼栋不存在")
			}
			return err
		}
		return insertPoints(ctx, tx, rt.ID, rt.Points)
	})
}

// UpdateRoute 实现 classroom.Repo（整体替换点集合，同一事务）。
func (r *BuildingPGRepo) UpdateRoute(ctx context.Context, rt *classroom.Route) error {
	return r.withTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE classroom_route SET
				title = $2, description = $3, start_label = $4,
				recommended = $5, sort_order = $6, enabled = $7, updated_at = $8
			 WHERE id = $1`,
			rt.ID, rt.Title, rt.Description, rt.StartLabel,
			rt.Recommended, rt.SortOrder, rt.Enabled, r.nowFn())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.Errorf(apperr.CodeNotFound, "路线不存在")
		}
		// 整体替换：先删后插。用事务保证「删了但插失败」不会留下空路线。
		if _, err := tx.Exec(ctx, `DELETE FROM classroom_route_point WHERE route_id = $1`, rt.ID); err != nil {
			return err
		}
		return insertPoints(ctx, tx, rt.ID, rt.Points)
	})
}

// FindRoute 实现 classroom.Repo。
func (r *BuildingPGRepo) FindRoute(ctx context.Context, id int64) (*classroom.Route, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, building_id, title, description, start_label, recommended,
		       sort_order, enabled, created_at, updated_at
		  FROM classroom_route WHERE id = $1`, id)
	var rt classroom.Route
	err := row.Scan(&rt.ID, &rt.BuildingID, &rt.Title, &rt.Description, &rt.StartLabel,
		&rt.Recommended, &rt.SortOrder, &rt.Enabled, &rt.CreatedAt, &rt.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, apperr.Errorf(apperr.CodeNotFound, "路线不存在")
	}
	if err != nil {
		return nil, err
	}
	pts, err := r.loadPoints(ctx, []int64{rt.ID})
	if err != nil {
		return nil, err
	}
	rt.Points = pts[rt.ID]
	return &rt, nil
}

// ListRoutes 实现 classroom.Repo。
func (r *BuildingPGRepo) ListRoutes(ctx context.Context, buildingID int64, includeDisabled bool) ([]*classroom.Route, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, building_id, title, description, start_label, recommended,
		       sort_order, enabled, created_at, updated_at
		  FROM classroom_route
		 WHERE building_id = $1 AND ($2 OR enabled)
		 ORDER BY sort_order, id`, buildingID, includeDisabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*classroom.Route, 0, 4)
	ids := make([]int64, 0, 4)
	for rows.Next() {
		var rt classroom.Route
		if err := rows.Scan(&rt.ID, &rt.BuildingID, &rt.Title, &rt.Description, &rt.StartLabel,
			&rt.Recommended, &rt.SortOrder, &rt.Enabled, &rt.CreatedAt, &rt.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &rt)
		ids = append(ids, rt.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	// 一次查全部点（避免 N+1）。
	pts, err := r.loadPoints(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, rt := range out {
		rt.Points = pts[rt.ID]
	}
	return out, nil
}

// DeleteRoute 实现 classroom.Repo。
func (r *BuildingPGRepo) DeleteRoute(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM classroom_route WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.Errorf(apperr.CodeNotFound, "路线不存在")
	}
	return nil
}

// ---------- 内部工具 ----------

func (r *BuildingPGRepo) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func insertPoints(ctx context.Context, tx pgx.Tx, routeID int64, pts []classroom.RoutePoint) error {
	if len(pts) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, p := range pts {
		batch.Queue(`INSERT INTO classroom_route_point
		             (route_id, seq, latitude, longitude, map_px, map_py, label)
		             VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			routeID, p.Seq, p.Lat, p.Lng, p.MapPx, p.MapPy, p.Label)
	}
	br := tx.SendBatch(ctx, batch)
	defer br.Close()
	for range pts {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// loadPoints 按路线 ID 批量取点，返回 routeID → 点切片（按 seq 升序）。
func (r *BuildingPGRepo) loadPoints(ctx context.Context, routeIDs []int64) (map[int64][]classroom.RoutePoint, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT route_id, seq, latitude, longitude, map_px, map_py, label
		  FROM classroom_route_point
		 WHERE route_id = ANY($1)
		 ORDER BY route_id, seq`, routeIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64][]classroom.RoutePoint{}
	for rows.Next() {
		var (
			rid int64
			p   classroom.RoutePoint
		)
		if err := rows.Scan(&rid, &p.Seq, &p.Lat, &p.Lng,
			&p.MapPx, &p.MapPy, &p.Label); err != nil {
			return nil, err
		}
		out[rid] = append(out[rid], p)
	}
	return out, rows.Err()
}

func scanBuilding(row pgx.Row) (*classroom.Building, error) {
	var b classroom.Building
	err := row.Scan(&b.ID, &b.Key, &b.No, &b.Zone, &b.DisplayName, &b.Aliases, &b.Campus,
		&b.Lat, &b.Lng, &b.MapPx, &b.MapPy, &b.AddressText, &b.Note, &b.Enabled,
		&b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if b.Aliases == nil {
		b.Aliases = []string{}
	}
	return &b, nil
}

func collectBuildings(rows pgx.Rows) ([]*classroom.Building, error) {
	out := make([]*classroom.Building, 0, 8)
	for rows.Next() {
		var b classroom.Building
		if err := rows.Scan(&b.ID, &b.Key, &b.No, &b.Zone, &b.DisplayName, &b.Aliases, &b.Campus,
			&b.Lat, &b.Lng, &b.AddressText, &b.Note, &b.Enabled, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		if b.Aliases == nil {
			b.Aliases = []string{}
		}
		out = append(out, &b)
	}
	return out, rows.Err()
}

// 编译期确认。
var _ classroom.Repo = (*BuildingPGRepo)(nil)
