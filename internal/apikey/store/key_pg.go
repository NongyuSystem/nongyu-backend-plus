package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/apikey"
	"nongyu-backend-plus/internal/apperr"
)

// KeyPGRepo 是 apikey.Repo 的 PostgreSQL 实现。
//
// 存的是**密文**（key_cipher BYTEA）。本仓储不解密——解密是 Service 的职责
// （它持有 Cipher）。这样即使仓储实现被换掉，加密语义也不会漏。
type KeyPGRepo struct {
	pool *pgxpool.Pool
}

// NewKeyPGRepo 构造 PG 仓储。
func NewKeyPGRepo(pool *pgxpool.Pool) *KeyPGRepo {
	return &KeyPGRepo{pool: pool}
}

const keyCols = `
	id, platform, key_cipher, key_hint, qps_limit, enabled,
	last_used_at, used_count, created_at, updated_at`

// Create 实现 apikey.Repo。
func (r *KeyPGRepo) Create(ctx context.Context, k *apikey.Key) error {
	return r.pool.QueryRow(ctx, `
		INSERT INTO amap_key (platform, key_cipher, key_hint, qps_limit, enabled)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, created_at, updated_at`,
		string(k.Platform), k.Cipher, k.Hint, k.QPSLimit, k.Enabled,
	).Scan(&k.ID, &k.CreatedAt, &k.UpdatedAt)
}

// Update 实现 apikey.Repo（platform 不可改）。
func (r *KeyPGRepo) Update(ctx context.Context, k *apikey.Key) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE amap_key SET
			key_cipher = $2, key_hint = $3, qps_limit = $4, enabled = $5, updated_at = $6
		 WHERE id = $1`,
		k.ID, k.Cipher, k.Hint, k.QPSLimit, k.Enabled, time.Now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.Errorf(apperr.CodeNotFound, "密钥不存在")
	}
	return nil
}

// FindByID 实现 apikey.Repo。
func (r *KeyPGRepo) FindByID(ctx context.Context, id int64) (*apikey.Key, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+keyCols+` FROM amap_key WHERE id = $1`, id)
	k, err := scanKey(row)
	if err == pgx.ErrNoRows {
		return nil, apperr.Errorf(apperr.CodeNotFound, "密钥不存在")
	}
	return k, err
}

// List 实现 apikey.Repo（含停用）。
func (r *KeyPGRepo) List(ctx context.Context, platform apikey.Platform) ([]*apikey.Key, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+keyCols+` FROM amap_key
		 WHERE ($1 = '' OR platform = $1)
		 ORDER BY id`, string(platform))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectKeys(rows)
}

// ListEnabled 实现 apikey.Repo（供轮换）。
func (r *KeyPGRepo) ListEnabled(ctx context.Context, platform apikey.Platform) ([]*apikey.Key, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+keyCols+` FROM amap_key
		 WHERE platform = $1 AND enabled
		 ORDER BY id`, string(platform))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectKeys(rows)
}

// Delete 实现 apikey.Repo。
func (r *KeyPGRepo) Delete(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM amap_key WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.Errorf(apperr.CodeNotFound, "密钥不存在")
	}
	return nil
}

// MarkUsed 实现 apikey.Repo（单条窄 UPDATE，避免与管理员编辑互相覆盖）。
func (r *KeyPGRepo) MarkUsed(ctx context.Context, id int64, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE amap_key SET used_count = used_count + 1, last_used_at = $2 WHERE id = $1`, id, at)
	return err
}

func scanKey(row pgx.Row) (*apikey.Key, error) {
	var (
		k  apikey.Key
		pt string
	)
	err := row.Scan(&k.ID, &pt, &k.Cipher, &k.Hint, &k.QPSLimit, &k.Enabled,
		&k.LastUsedAt, &k.UsedCount, &k.CreatedAt, &k.UpdatedAt)
	if err != nil {
		return nil, err
	}
	k.Platform = apikey.Platform(pt)
	return &k, nil
}

func collectKeys(rows pgx.Rows) ([]*apikey.Key, error) {
	out := make([]*apikey.Key, 0, 8)
	for rows.Next() {
		var (
			k  apikey.Key
			pt string
		)
		if err := rows.Scan(&k.ID, &pt, &k.Cipher, &k.Hint, &k.QPSLimit, &k.Enabled,
			&k.LastUsedAt, &k.UsedCount, &k.CreatedAt, &k.UpdatedAt); err != nil {
			return nil, err
		}
		k.Platform = apikey.Platform(pt)
		out = append(out, &k)
	}
	return out, rows.Err()
}

// 编译期确认。
var _ apikey.Repo = (*KeyPGRepo)(nil)
