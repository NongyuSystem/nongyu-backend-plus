package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/classroom"
)

// MapPGRepo 是 classroom.MapRepo 的 PostgreSQL 实现（规格 §6.1）。
type MapPGRepo struct {
	pool  *pgxpool.Pool
	nowFn func() time.Time
}

// NewMapPGRepo 构造底图仓储。
func NewMapPGRepo(pool *pgxpool.Pool, nowFn func() time.Time) *MapPGRepo {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &MapPGRepo{pool: pool, nowFn: nowFn}
}

const mapCols = `
	id, campus, object_key, content_type, size_bytes, etag,
	width, height, coord_system,
	ar, ai, tx0, ty0, lng0, lat0, kx, ky,
	calibration_note, enabled, created_at, updated_at`

// scanMap 把一行读成实体。
//
// 换算参数八列**全有或全无**（迁移里的 CHECK 保证）。这里用可空扫描：
// 全空 → Cal 为 nil（未标定）；全非空 → 组装 Calibration。
func scanMap(row pgx.Row) (*classroom.Map, error) {
	var m classroom.Map
	var (
		ar, ai, tx0, ty0, lng0, lat0, kx, ky *float64
	)
	err := row.Scan(
		&m.ID, &m.Campus, &m.ObjectKey, &m.ContentType, &m.SizeBytes, &m.ETag,
		&m.Width, &m.Height, &m.CoordSystem,
		&ar, &ai, &tx0, &ty0, &lng0, &lat0, &kx, &ky,
		&m.CalibrationNote, &m.Enabled, &m.CreatedAt, &m.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if ar != nil && ai != nil && tx0 != nil && ty0 != nil &&
		lng0 != nil && lat0 != nil && kx != nil && ky != nil {
		m.Cal = &classroom.Calibration{
			AR: *ar, AI: *ai, TX0: *tx0, TY0: *ty0,
			LNG0: *lng0, LAT0: *lat0, KX: *kx, KY: *ky,
		}
	}
	return &m, nil
}

// FindMapByCampus 实现 classroom.MapRepo。
func (r *MapPGRepo) FindMapByCampus(ctx context.Context, campus classroom.Campus) (*classroom.Map, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+mapCols+` FROM classroom_map WHERE campus = $1 AND enabled`, string(campus))
	m, err := scanMap(row)
	if err == pgx.ErrNoRows {
		return nil, apperr.Errorf(apperr.CodeNotFound, "该校区暂无地图")
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// ListMaps 实现 classroom.MapRepo（含停用，供管理端）。
func (r *MapPGRepo) ListMaps(ctx context.Context) ([]*classroom.Map, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+mapCols+` FROM classroom_map ORDER BY campus`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*classroom.Map
	for rows.Next() {
		m, err := scanMap(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpsertMapImage 实现 classroom.MapRepo。
//
// **只更新「图」相关列**，绝不碰换算参数：换图不等于重新标定。
// 若这里顺手把参数写成 NULL，会让本来已标定的校区突然无法用像素定位，
// 而这种退化在客户端表现为「坐标没了」，很难追溯到「有人换了张图」。
func (r *MapPGRepo) UpsertMapImage(ctx context.Context, m *classroom.Map) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO classroom_map
			(campus, object_key, content_type, size_bytes, etag,
			 width, height, coord_system, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (campus) DO UPDATE SET
			object_key   = EXCLUDED.object_key,
			content_type = EXCLUDED.content_type,
			size_bytes   = EXCLUDED.size_bytes,
			etag         = EXCLUDED.etag,
			width        = EXCLUDED.width,
			height       = EXCLUDED.height,
			coord_system = EXCLUDED.coord_system,
			updated_at   = now()`,
		string(m.Campus), m.ObjectKey, m.ContentType, m.SizeBytes, m.ETag,
		m.Width, m.Height, m.CoordSystem, m.Enabled)
	return err
}

// SetCalibration 实现 classroom.MapRepo。
//
// 校区行不存在时先建一行（只有参数、没有图）：运维可能先标定后传图。
// 此时 object_key 为空串、尺寸为 0，符合 ck_classroom_map_uploaded 的
// 「未上传」分支。
func (r *MapPGRepo) SetCalibration(
	ctx context.Context, campus classroom.Campus, cal *classroom.Calibration, note string,
) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE classroom_map SET
			ar = $2, ai = $3, tx0 = $4, ty0 = $5,
			lng0 = $6, lat0 = $7, kx = $8, ky = $9,
			calibration_note = $10, updated_at = now()
		 WHERE campus = $1`,
		string(campus), cal.AR, cal.AI, cal.TX0, cal.TY0,
		cal.LNG0, cal.LAT0, cal.KX, cal.KY, note)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}

	// 没有该校区行：插入一行「只有参数」的记录。
	_, err = r.pool.Exec(ctx, `
		INSERT INTO classroom_map
			(campus, object_key, content_type, size_bytes, width, height, coord_system,
			 ar, ai, tx0, ty0, lng0, lat0, kx, ky, calibration_note, enabled)
		VALUES ($1,'','image/gif',0,0,0,'GCJ-02',$2,$3,$4,$5,$6,$7,$8,$9,$10,TRUE)`,
		string(campus), cal.AR, cal.AI, cal.TX0, cal.TY0,
		cal.LNG0, cal.LAT0, cal.KX, cal.KY, note)
	return err
}
