package platform

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGIdentityStore 是身份域四类仓储的 Postgres 实现（UserRepo / ProfileRepo /
// LoginMethodRepo / SessionStore 都在这一类型上，装配层按接口注入）。
//
// 只做持久化，不做任何业务判断：谁能不能登录、能不能改哪个字段，都在 identity 域里决定。
type PGIdentityStore struct {
	pool *pgxpool.Pool
}

// NewPGIdentityStore 构造仓储。
func NewPGIdentityStore(opts PGOptions) (*PGIdentityStore, error) {
	if opts.DSN == "" {
		return nil, fmt.Errorf("platform: PGIdentityStore 需要 DSN")
	}
	cfg, err := pgxpool.ParseConfig(opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("platform: 解析 DSN 失败：%w", err)
	}
	cfg.MaxConns = 8
	cfg.ConnConfig.ConnectTimeout = 2 * time.Second // 快速失败，坏依赖不拖秒级
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	return &PGIdentityStore{pool: pool}, nil
}

// Ping 探测连通性。
func (s *PGIdentityStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Close 释放连接池。
func (s *PGIdentityStore) Close() error { s.pool.Close(); return nil }

// isUniqueViolation 判定唯一约束冲突（Postgres 23505）。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ── UserRepo ──────────────────────────────────────────────────────

// Create 实现 UserRepo。
func (s *PGIdentityStore) Create(ctx context.Context, studentNo string, role Role) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (student_no, role) VALUES ($1, $2)
		 RETURNING id, student_no, role, status, created_at`,
		studentNo, int16(role),
	).Scan(&u.ID, &u.StudentNo, &u.Role, &u.Status, &u.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrAlreadyExists
		}
		return User{}, err
	}
	return u, nil
}

// ByStudentNo 实现 UserRepo。
func (s *PGIdentityStore) ByStudentNo(ctx context.Context, studentNo string) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx,
		`SELECT id, student_no, role, status, created_at FROM users WHERE student_no = $1`,
		studentNo,
	).Scan(&u.ID, &u.StudentNo, &u.Role, &u.Status, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// ByID 实现 UserRepo。
func (s *PGIdentityStore) ByID(ctx context.Context, id int64) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx,
		`SELECT id, student_no, role, status, created_at FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.StudentNo, &u.Role, &u.Status, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// SetStatus 实现 UserRepo。
func (s *PGIdentityStore) SetStatus(ctx context.Context, id int64, status UserStatus) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET status = $2, updated_at = now() WHERE id = $1`, id, int16(status))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRole 改用户角色（管理台用，不在 UserRepo 接口里，避免业务域随手改权限）。
func (s *PGIdentityStore) SetRole(ctx context.Context, id int64, role Role) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET role = $2, updated_at = now() WHERE id = $1`, id, int16(role))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── ProfileRepo ───────────────────────────────────────────────────

// Upsert 实现 ProfileRepo（整体写入；字段分权由业务域保证）。
func (s *PGIdentityStore) Upsert(ctx context.Context, p Profile) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_profiles
			(user_id, name, id_card_sealed, id_card_suffix4, college, major, class_name, grade, qq, avatar_url, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10, now())
		ON CONFLICT (user_id) DO UPDATE SET
			name = EXCLUDED.name,
			id_card_sealed = EXCLUDED.id_card_sealed,
			id_card_suffix4 = EXCLUDED.id_card_suffix4,
			college = EXCLUDED.college,
			major = EXCLUDED.major,
			class_name = EXCLUDED.class_name,
			grade = EXCLUDED.grade,
			qq = EXCLUDED.qq,
			avatar_url = EXCLUDED.avatar_url,
			updated_at = now()`,
		p.UserID, p.Name, p.IDCardSealed, p.IDCardSuffix4,
		p.College, p.Major, p.ClassName, p.Grade, p.QQ, p.AvatarURL)
	return err
}

// ByUserID 实现 ProfileRepo。
func (s *PGIdentityStore) ByUserID(ctx context.Context, userID int64) (Profile, error) {
	var p Profile
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, name, id_card_sealed, id_card_suffix4, college, major, class_name, grade, qq, avatar_url, updated_at
		  FROM user_profiles WHERE user_id = $1`, userID,
	).Scan(&p.UserID, &p.Name, &p.IDCardSealed, &p.IDCardSuffix4,
		&p.College, &p.Major, &p.ClassName, &p.Grade, &p.QQ, &p.AvatarURL, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, err
	}
	return p, nil
}

// ── LoginMethodRepo ───────────────────────────────────────────────

// Bind 实现 LoginMethodRepo。
func (s *PGIdentityStore) Bind(ctx context.Context, m LoginMethod) (LoginMethod, error) {
	var out LoginMethod
	var kind string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO login_methods (user_id, kind, identifier, secret_ref)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, kind, identifier, secret_ref, bound_at`,
		m.UserID, string(m.Kind), m.Identifier, m.SecretSealed,
	).Scan(&out.ID, &out.UserID, &kind, &out.Identifier, &out.SecretSealed, &out.BoundAt)
	if err != nil {
		if isUniqueViolation(err) {
			return LoginMethod{}, ErrAlreadyExists
		}
		return LoginMethod{}, err
	}
	out.Kind = LoginMethodKind(kind)
	return out, nil
}

// ByIdentifier 实现 LoginMethodRepo。
func (s *PGIdentityStore) ByIdentifier(ctx context.Context, kind LoginMethodKind, identifier string) (LoginMethod, error) {
	var m LoginMethod
	var k string
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, kind, identifier, secret_ref, bound_at
		  FROM login_methods WHERE kind = $1 AND identifier = $2`, string(kind), identifier,
	).Scan(&m.ID, &m.UserID, &k, &m.Identifier, &m.SecretSealed, &m.BoundAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginMethod{}, ErrNotFound
	}
	if err != nil {
		return LoginMethod{}, err
	}
	m.Kind = LoginMethodKind(k)
	return m, nil
}

// ByUserAndKind 实现 LoginMethodRepo。
func (s *PGIdentityStore) ByUserAndKind(ctx context.Context, userID int64, kind LoginMethodKind) (LoginMethod, error) {
	var m LoginMethod
	var k string
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, kind, identifier, secret_ref, bound_at
		  FROM login_methods WHERE user_id = $1 AND kind = $2`, userID, string(kind),
	).Scan(&m.ID, &m.UserID, &k, &m.Identifier, &m.SecretSealed, &m.BoundAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginMethod{}, ErrNotFound
	}
	if err != nil {
		return LoginMethod{}, err
	}
	m.Kind = LoginMethodKind(k)
	return m, nil
}

// UpdateSecret 实现 LoginMethodRepo。
func (s *PGIdentityStore) UpdateSecret(ctx context.Context, userID int64, kind LoginMethodKind, sealed []byte) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE login_methods SET secret_ref = $3 WHERE user_id = $1 AND kind = $2`,
		userID, string(kind), sealed)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Unbind 实现 LoginMethodRepo。
func (s *PGIdentityStore) Unbind(ctx context.Context, userID int64, kind LoginMethodKind) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM login_methods WHERE user_id = $1 AND kind = $2`, userID, string(kind))
	return err
}

// ── SessionStore ──────────────────────────────────────────────────

// CreateSession 实现 SessionStore。
func (s *PGIdentityStore) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (id, user_id, scope, device_id, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5, now())`,
		sess.ID, sess.UserID, string(sess.Scope), sess.DeviceID, sess.ExpiresAt)
	return err
}

// SessionByID 实现 SessionStore。
func (s *PGIdentityStore) SessionByID(ctx context.Context, id string) (Session, error) {
	var sess Session
	var scope string
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, scope, device_id, expires_at, revoked_at, created_at
		  FROM sessions WHERE id = $1`, id,
	).Scan(&sess.ID, &sess.UserID, &scope, &sess.DeviceID,
		&sess.ExpiresAt, &sess.RevokedAt, &sess.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	sess.Scope = SessionScope(scope)
	return sess, nil
}

// Revoke 实现 SessionStore（幂等：已吊销或不存在都不报错）。
func (s *PGIdentityStore) Revoke(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	return err
}

// RevokeByUserScope 吊销该用户在该 scope 下所有未吊销会话，返回受影响条数。
// 「App 单设备登录顶替」与「管理台强制下线」都走这里。
func (s *PGIdentityStore) RevokeByUserScope(ctx context.Context, userID int64, scope SessionScope) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sessions SET revoked_at = now()
		 WHERE user_id = $1 AND scope = $2 AND revoked_at IS NULL`,
		userID, string(scope))
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
