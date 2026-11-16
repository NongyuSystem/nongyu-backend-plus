package pgstore

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/skin"
)

// SkinPGRepo 是 skin.Repo 的 **PostgreSQL 实现**。
//
// 与 SkinMemoryRepo 是同一个领域接口的两个实现，行为必须逐条对齐：
//   - 版本不可变：(pack_id, version) 唯一，重复 Create 返回 ErrVersionExists；
//   - 全局单指针：skin_current 单行表（id=1），Activate 用 upsert 原子切换；
//   - 指针不得悬空：外键指向 skin_packs(pack_id, version)。
//
// 表结构见 migrations/0007_skins.sql。列清单写死而非 SELECT *：迁移只增不改，
// SELECT * 会在加列时让 Scan 参数错位，报错点在运行时且难定位。
type SkinPGRepo struct {
	pool  *pgxpool.Pool
	nowFn func() time.Time
}

// NewSkinPGRepo 构造 PG 皮肤仓储。pool 不得为 nil（装配层只在配了
// DATABASE_URL 时构造本类型）；now 可为 nil，默认 time.Now。
func NewSkinPGRepo(pool *pgxpool.Pool, now func() time.Time) *SkinPGRepo {
	if now == nil {
		now = time.Now
	}
	return &SkinPGRepo{pool: pool, nowFn: now}
}

// skinPackColumns 是读路径列清单；顺序与 scanPack 一一对应。
const skinPackColumns = `id, pack_id, version, schema_version, min_app_version,
	display_name, object_key, pack_sha256, pack_size, manifest_json, created_at, created_by`

// Create 实现 skin.Repo。ID 由库生成（BIGSERIAL），created_at 取注入时钟
// （忽略入参时间，与内存实现同构）。唯一约束冲突映射成 ErrVersionExists。
func (r *SkinPGRepo) Create(ctx context.Context, p *skin.Pack) error {
	now := r.nowFn()
	err := r.pool.QueryRow(ctx, `
		INSERT INTO skin_packs (
			pack_id, version, schema_version, min_app_version, display_name,
			object_key, pack_sha256, pack_size, manifest_json, created_at, created_by
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id, created_at`,
		p.PackID, p.Version, p.SchemaVersion, p.MinAppVersion, p.DisplayName,
		p.ObjectKey, p.PackSHA256, p.PackSize, p.ManifestJSON, now, p.CreatedBy,
	).Scan(&p.ID, &p.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return skin.ErrVersionExists
		}
		return err
	}
	return nil
}

// GetByRef 实现 skin.Repo。
func (r *SkinPGRepo) GetByRef(ctx context.Context, packID, version string) (*skin.Pack, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+skinPackColumns+`
		FROM skin_packs WHERE pack_id = $1 AND version = $2`, packID, version)
	p, err := scanPack(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, skin.ErrVersionNotFound
		}
		return nil, err
	}
	return p, nil
}

// List 实现 skin.Repo（按创建时间倒序，新的在前；id 作同刻的决胜键）。
func (r *SkinPGRepo) List(ctx context.Context, limit int) ([]*skin.Pack, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+skinPackColumns+`
		FROM skin_packs ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*skin.Pack{}
	for rows.Next() {
		p, err := scanPack(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Current 实现 skin.Repo。
//
// 用 JOIN 而不是「先读指针再读包」：一次往返就拿到完整目录视图，且外键已
// 保证指针指向的包存在；没有指针行时返回 ErrNoCurrentPointer。
func (r *SkinPGRepo) Current(ctx context.Context) (*skin.Current, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT p.pack_id, p.version, p.schema_version, p.min_app_version,
		       p.display_name, p.object_key, p.pack_sha256, p.pack_size,
		       p.manifest_json, c.updated_at
		FROM skin_current c
		JOIN skin_packs p ON p.pack_id = c.pack_id AND p.version = c.version
		WHERE c.id = 1`)
	var cur skin.Current
	err := row.Scan(
		&cur.PackID, &cur.Version, &cur.SchemaVersion, &cur.MinAppVersion,
		&cur.DisplayName, &cur.ObjectKey, &cur.PackSHA256, &cur.PackSize,
		&cur.ManifestJSON, &cur.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, skin.ErrNoCurrentPointer
		}
		return nil, err
	}
	return &cur, nil
}

// Activate 实现 skin.Repo。
//
// 先确认目标包存在（外键只会挡「指针指向不存在的行」，但我们要把「包不存在」
// 明确映射成 ErrVersionNotFound 而不是外层 500）；再用单行 upsert 原子切指针。
// 两步都在同一进程内、以唯一约束兜底，不存在「两个请求各写一行」的竞态。
func (r *SkinPGRepo) Activate(ctx context.Context, packID, version string) error {
	var exists bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM skin_packs WHERE pack_id = $1 AND version = $2)`,
		packID, version).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return skin.ErrVersionNotFound
	}
	now := r.nowFn()
	_, err := r.pool.Exec(ctx, `
		INSERT INTO skin_current (id, pack_id, version, updated_at)
		VALUES (1, $1, $2, $3)
		ON CONFLICT (id) DO UPDATE
		    SET pack_id = EXCLUDED.pack_id,
		        version = EXCLUDED.version,
		        updated_at = EXCLUDED.updated_at`,
		packID, version, now)
	return err
}

// scanPack 把一行扫进 skin.Pack。row 用 pgx.Rows / pgx.Row 的公共 Scan 形状。
type scannable interface {
	Scan(dest ...any) error
}

func scanPack(row scannable) (*skin.Pack, error) {
	var p skin.Pack
	err := row.Scan(
		&p.ID, &p.PackID, &p.Version, &p.SchemaVersion, &p.MinAppVersion,
		&p.DisplayName, &p.ObjectKey, &p.PackSHA256, &p.PackSize,
		&p.ManifestJSON, &p.CreatedAt, &p.CreatedBy,
	)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// isUniqueViolation 判 Postgres 唯一约束冲突（SQLSTATE 23505）。
func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// 编译期确认满足接缝。
var _ skin.Repo = (*SkinPGRepo)(nil)
