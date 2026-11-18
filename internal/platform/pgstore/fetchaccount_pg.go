package pgstore

// 教务取数账号池的 PostgreSQL 实现。
//
// 表结构见 migrations/0008_fetch_accounts.sql。凭据以 AES-GCM 密文存 bytea，
// 列表/详情接口不回显任何明文；只有 Pick 在取数时解密。
//
// 两套实现（PG / 内存）行为对齐，装配层按 storeMode 二选一，不混用。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/jiaowu"
)

// ErrFetchAccountNotFound 表示账号不存在。
var ErrFetchAccountNotFound = errors.New("pgstore: 取数账号不存在")

// FetchAccountAdminRepo 是管理员维护取数账号的接口（挂 adminGroup）。
//
// 明文凭据只在 Create/Update 的入参里出现一次，返回体绝不含 TGT/VPNTicket。
type FetchAccountAdminRepo interface {
	List(ctx context.Context) ([]jiaowu.FetchAccount, error)
	Create(ctx context.Context, a jiaowu.FetchAccount) (int64, error)
	Update(ctx context.Context, a jiaowu.FetchAccount) error
	Delete(ctx context.Context, id int64) error
	SetEnabled(ctx context.Context, id int64, enabled bool) error
}

// fetchAccountBundle 是 tgt_sealed 里的明文结构，与 schoolauth.TGTBundle 同形。
type fetchAccountBundle struct {
	TGT          string `json:"TGT"`
	CookieHeader string `json:"CookieHeader"`
}

// ---------- PG 实现 ----------

// FetchAccountPGRepo 同时实现 Pool 与管理员 CRUD。
type FetchAccountPGRepo struct {
	pool      *pgxpool.Pool
	tgtCipher platform.Cipher
	vpnCipher platform.Cipher
	nowFn     func() time.Time
	// pickCursor 让 Pick 在多个账号间轮流，避免总打第一个。
	pickCursor uint64
	mu         sync.Mutex
}

// NewFetchAccountPGRepo 构造 PG 仓储。tgt/vpn 用不同派生密钥隔离。
func NewFetchAccountPGRepo(pool *pgxpool.Pool, ticketKey string, now func() time.Time) *FetchAccountPGRepo {
	if now == nil {
		now = time.Now
	}
	return &FetchAccountPGRepo{
		pool:      pool,
		tgtCipher: platform.NewAESCipher(ticketKey + ":fetch-account-tgt"),
		vpnCipher: platform.NewAESCipher(ticketKey + ":fetch-account-vpn"),
		nowFn:     now,
	}
}

// Create 新增一个取数账号，返回库生成的 ID。
func (r *FetchAccountPGRepo) Create(ctx context.Context, a jiaowu.FetchAccount) (int64, error) {
	tgtSealed, vpnSealed, err := r.sealCreds(a)
	if err != nil {
		return 0, err
	}
	var id int64
	err = r.pool.QueryRow(ctx, `
		INSERT INTO jiaowu_fetch_accounts
			(label, tgt_sealed, vpn_ticket_sealed, enabled, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$5)
		RETURNING id`,
		a.Label, tgtSealed, vpnSealed, a.Enabled, r.nowFn(),
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("pgstore: 创建取数账号失败：%w", err)
	}
	return id, nil
}

// Update 覆盖标签与两套凭据（enabled 由 SetEnabled 单独维护）。
func (r *FetchAccountPGRepo) Update(ctx context.Context, a jiaowu.FetchAccount) error {
	tgtSealed, vpnSealed, err := r.sealCreds(a)
	if err != nil {
		return err
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE jiaowu_fetch_accounts
		SET label=$2, tgt_sealed=$3, vpn_ticket_sealed=$4,
		    failure_count=0, updated_at=$5
		WHERE id=$1`,
		a.ID, a.Label, tgtSealed, vpnSealed, r.nowFn(),
	)
	if err != nil {
		return fmt.Errorf("pgstore: 更新取数账号失败：%w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFetchAccountNotFound
	}
	return nil
}

// List 返回账号元数据（不含任何明文凭据）。
func (r *FetchAccountPGRepo) List(ctx context.Context) ([]jiaowu.FetchAccount, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, label, enabled, failure_count, last_used_at, created_at, updated_at
		FROM jiaowu_fetch_accounts ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("pgstore: 列出取数账号失败：%w", err)
	}
	defer rows.Close()
	out := []jiaowu.FetchAccount{}
	for rows.Next() {
		var a jiaowu.FetchAccount
		var failures int
		var lastUsed *time.Time
		var created, updated time.Time
		if err := rows.Scan(&a.ID, &a.Label, &a.Enabled, &failures, &lastUsed, &created, &updated); err != nil {
			return nil, fmt.Errorf("pgstore: 扫描取数账号失败：%w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetEnabled 启停一个账号。
func (r *FetchAccountPGRepo) SetEnabled(ctx context.Context, id int64, enabled bool) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE jiaowu_fetch_accounts SET enabled=$2, updated_at=$3 WHERE id=$1`,
		id, enabled, r.nowFn())
	if err != nil {
		return fmt.Errorf("pgstore: 启停取数账号失败：%w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFetchAccountNotFound
	}
	return nil
}

// Delete 删除账号（物理删除，凭据随之消失）。
func (r *FetchAccountPGRepo) Delete(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM jiaowu_fetch_accounts WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("pgstore: 删除取数账号失败：%w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFetchAccountNotFound
	}
	return nil
}

// Pick 实现 jiaowu.FetchAccountPool：从启用账号里轮流选一个，解密返回凭据。
func (r *FetchAccountPGRepo) Pick(ctx context.Context) (jiaowu.FetchAccount, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, label, tgt_sealed, vpn_ticket_sealed
		FROM jiaowu_fetch_accounts WHERE enabled = TRUE ORDER BY id`)
	if err != nil {
		return jiaowu.FetchAccount{}, fmt.Errorf("pgstore: 选取数账号失败：%w", err)
	}
	defer rows.Close()
	type row struct {
		id                         int64
		label                      string
		tgtSealed, vpnTicketSealed []byte
	}
	all := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.id, &x.label, &x.tgtSealed, &x.vpnTicketSealed); err != nil {
			return jiaowu.FetchAccount{}, fmt.Errorf("pgstore: 扫描凭据失败：%w", err)
		}
		all = append(all, x)
	}
	if err := rows.Err(); err != nil {
		return jiaowu.FetchAccount{}, err
	}
	if len(all) == 0 {
		return jiaowu.FetchAccount{}, jiaowu.ErrFetchAccountUnavailable
	}
	r.mu.Lock()
	idx := int(r.pickCursor % uint64(len(all)))
	r.pickCursor++
	r.mu.Unlock()
	chosen := all[idx]

	bundle, err := r.openTGT(chosen.tgtSealed)
	if err != nil {
		return jiaowu.FetchAccount{}, jiaowu.ErrFetchAccountUnavailable
	}
	vpn, err := r.vpnCipher.Open(chosen.vpnTicketSealed)
	if err != nil {
		return jiaowu.FetchAccount{}, jiaowu.ErrFetchAccountUnavailable
	}
	_, _ = r.pool.Exec(ctx, `UPDATE jiaowu_fetch_accounts SET last_used_at=$2 WHERE id=$1`, chosen.id, r.nowFn())
	return jiaowu.FetchAccount{
		ID:           chosen.id,
		Label:        chosen.label,
		TGT:          bundle.TGT,
		CookieHeader: bundle.CookieHeader,
		VPNTicket:    string(vpn),
		Enabled:      true,
	}, nil
}

// MarkFailure 累加失败计数；连续失败达阈值自动停用，避免死凭据反复打学校站。
func (r *FetchAccountPGRepo) MarkFailure(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE jiaowu_fetch_accounts
		SET failure_count = failure_count + 1,
		    enabled = CASE WHEN failure_count + 1 >= 5 THEN FALSE ELSE enabled END,
		    updated_at = $2
		WHERE id = $1`, id, r.nowFn())
	return err
}

func (r *FetchAccountPGRepo) sealCreds(a jiaowu.FetchAccount) ([]byte, []byte, error) {
	if a.TGT == "" || a.VPNTicket == "" {
		return nil, nil, errors.New("pgstore: 取数账号必须同时提供 tgt 与 webvpn 票")
	}
	ch := a.CookieHeader
	if ch == "" {
		ch = "go-admin-tgt=" + a.TGT
	}
	plain, err := json.Marshal(fetchAccountBundle{TGT: a.TGT, CookieHeader: ch})
	if err != nil {
		return nil, nil, err
	}
	tgtSealed, err := r.tgtCipher.Seal(plain)
	if err != nil {
		return nil, nil, err
	}
	vpnSealed, err := r.vpnCipher.Seal([]byte(a.VPNTicket))
	if err != nil {
		return nil, nil, err
	}
	return tgtSealed, vpnSealed, nil
}

func (r *FetchAccountPGRepo) openTGT(sealed []byte) (fetchAccountBundle, error) {
	plain, err := r.tgtCipher.Open(sealed)
	if err != nil {
		return fetchAccountBundle{}, err
	}
	var b fetchAccountBundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return fetchAccountBundle{}, err
	}
	return b, nil
}
