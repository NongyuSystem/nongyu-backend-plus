package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/release"
)

// ReleasePGRepo 是 release.Repo 的 **PostgreSQL 实现**。
//
// 与 ReleaseMemoryRepo 是同一领域接口的两个实现，行为必须逐条对齐：
//   - 版本不可变：(app_id, version_code) 唯一，重复 Create 返回 ErrReleaseExists；
//   - 最新 = version_code 最大者（ORDER BY version_code DESC LIMIT 1），无激活指针。
//
// 表结构见 migrations/0012_app_releases.sql。列清单写死而非 SELECT *：
// 迁移只增不改，SELECT * 会在加列时让 Scan 参数错位，报错点在运行时且难定位。
type ReleasePGRepo struct {
	pool  *pgxpool.Pool
	nowFn func() time.Time
}

// NewReleasePGRepo 构造 PG 仓储。pool 不得为 nil（装配层只在配了
// DATABASE_URL 时构造本类型）；now 可为 nil，默认 time.Now。
func NewReleasePGRepo(pool *pgxpool.Pool, now func() time.Time) *ReleasePGRepo {
	if now == nil {
		now = time.Now
	}
	return &ReleasePGRepo{pool: pool, nowFn: now}
}

// releaseColumns 是读路径列清单；顺序与 scanRelease 一一对应。
const releaseColumns = `id, app_id, version_code, version_name, min_sdk,
	min_supported_version_code, changelog, object_key, sha256, size, created_by, created_at`

// Create 实现 release.Repo。ID 由库生成（BIGSERIAL），created_at 取注入时钟
// （忽略入参时间，与内存实现同构）。唯一约束冲突映射成 ErrReleaseExists。
func (r *ReleasePGRepo) Create(ctx context.Context, rec *release.Release) error {
	now := r.nowFn()
	err := r.pool.QueryRow(ctx, `
		INSERT INTO app_releases (
			app_id, version_code, version_name, min_sdk, min_supported_version_code,
			changelog, object_key, sha256, size, created_by, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id, created_at`,
		rec.AppID, rec.VersionCode, rec.VersionName, rec.MinSdk, rec.MinSupportedVersionCode,
		rec.Changelog, rec.ObjectKey, rec.SHA256, rec.Size, rec.CreatedBy, now,
	).Scan(&rec.ID, &rec.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return release.ErrReleaseExists
		}
		return err
	}
	return nil
}

// GetByRef 实现 release.Repo。
func (r *ReleasePGRepo) GetByRef(ctx context.Context, appID string, versionCode int64) (*release.Release, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+releaseColumns+`
		FROM app_releases WHERE app_id = $1 AND version_code = $2`, appID, versionCode)
	rec, err := scanRelease(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, release.ErrReleaseNotFound
		}
		return nil, err
	}
	return rec, nil
}

// Latest 实现 release.Repo：versionCode 最大的那条。
func (r *ReleasePGRepo) Latest(ctx context.Context, appID string) (*release.Release, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+releaseColumns+`
		FROM app_releases WHERE app_id = $1
		ORDER BY version_code DESC LIMIT 1`, appID)
	rec, err := scanRelease(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, release.ErrNoRelease
		}
		return nil, err
	}
	return rec, nil
}

// scanRelease 把一行扫进 Release。
func scanRelease(row pgx.Row) (*release.Release, error) {
	var rec release.Release
	err := row.Scan(
		&rec.ID, &rec.AppID, &rec.VersionCode, &rec.VersionName, &rec.MinSdk,
		&rec.MinSupportedVersionCode, &rec.Changelog, &rec.ObjectKey,
		&rec.SHA256, &rec.Size, &rec.CreatedBy, &rec.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// isUniqueViolation 判断错误是否为唯一约束冲突（SQLSTATE 23505）。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
