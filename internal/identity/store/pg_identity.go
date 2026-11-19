package store

// PGIdentityStore：身份域四类仓储（model.UserRepo / model.ProfileRepo / model.LoginMethodRepo / model.SessionStore）
// 的 **Postgres 单一实现**（原 pg_identity.go）。
//
// 为什么一个类型承担四个接口：它们操作的是同一批表、同一连接池、同一事务语义，
// 拆成四个类型只会让装配层多 new 三次、还多三份池。接口边界由 identity.go 的
// 四个窄接口保证，调用方拿到哪个接口由装配层决定，看不到本类型。
//
// 职责红线：**只做持久化，不做业务判断**。谁能不能登录、能改哪个字段，都在 identity 域。

import (
	"nongyu-backend-plus/internal/platform/model"

	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGOptions 是 Postgres 连接配置（身份仓储与测试基建共用）。
type PGOptions struct {
	// DSN 形如 postgres://user:pass@host:port/db?sslmode=disable
	DSN string
}

// PGIdentityStore 是身份域四类仓储的 Postgres 实现（model.UserRepo / model.ProfileRepo /
// model.LoginMethodRepo / model.SessionStore 都在这一类型上，装配层按接口注入）。
//
// 只做持久化，不做任何业务判断：谁能不能登录、能不能改哪个字段，都在 identity 域里决定。
type PGIdentityStore struct {
	pool *pgxpool.Pool
}

// Pool 暴露底层连接池，供诊断/批处理工具使用（业务代码不应依赖它）。
func (s *PGIdentityStore) Pool() *pgxpool.Pool { return s.pool }

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

// ── model.UserRepo ──────────────────────────────────────────────────────

// Create 实现 model.UserRepo。
func (s *PGIdentityStore) Create(ctx context.Context, studentNo string, role model.Role) (model.User, error) {
	var u model.User
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (student_no, role) VALUES ($1, $2)
		 RETURNING id, student_no, role, status, created_at,
		           last_login_ip, last_login_device, last_login_at`,
		studentNo, int16(role),
	).Scan(&u.ID, &u.StudentNo, &u.Role, &u.Status, &u.CreatedAt, &u.LastLoginIP, &u.LastLoginDevice, &u.LastLoginAt)
	if err != nil {
		if isUniqueViolation(err) {
			return model.User{}, model.ErrAlreadyExists
		}
		return model.User{}, err
	}
	return u, nil
}

// ByStudentNo 实现 model.UserRepo。
func (s *PGIdentityStore) ByStudentNo(ctx context.Context, studentNo string) (model.User, error) {
	var u model.User
	err := s.pool.QueryRow(ctx,
		`SELECT id, student_no, role, status, created_at, last_login_ip, last_login_device, last_login_at FROM users WHERE student_no = $1`,
		studentNo,
	).Scan(&u.ID, &u.StudentNo, &u.Role, &u.Status, &u.CreatedAt, &u.LastLoginIP, &u.LastLoginDevice, &u.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, model.ErrNotFound
	}
	if err != nil {
		return model.User{}, err
	}
	return u, nil
}

// ByID 实现 model.UserRepo。
func (s *PGIdentityStore) ByID(ctx context.Context, id int64) (model.User, error) {
	var u model.User
	err := s.pool.QueryRow(ctx,
		`SELECT id, student_no, role, status, created_at, last_login_ip, last_login_device, last_login_at FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.StudentNo, &u.Role, &u.Status, &u.CreatedAt, &u.LastLoginIP, &u.LastLoginDevice, &u.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, model.ErrNotFound
	}
	if err != nil {
		return model.User{}, err
	}
	return u, nil
}

// SetStatus 实现 model.UserRepo。
func (s *PGIdentityStore) SetStatus(ctx context.Context, id int64, status model.UserStatus) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET status = $2, updated_at = now() WHERE id = $1`, id, int16(status))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return model.ErrNotFound
	}
	return nil
}

// TouchLogin 记录一次成功登录的审计三元组（issue #29）。
//
// 只在登录成功路径调用一次；不要挂到每个请求上（写放大会压垮 users 表）。
// ip/device 允许为空（取不到客户端信息时不阻塞登录）。
func (s *PGIdentityStore) TouchLogin(ctx context.Context, id int64, ip, device string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users
		    SET last_login_ip = $2, last_login_device = $3, last_login_at = now(), updated_at = now()
		  WHERE id = $1`, id, strings.TrimSpace(ip), strings.TrimSpace(device))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return model.ErrNotFound
	}
	return nil
}

// SetRole 改用户角色（管理台用，不在 model.UserRepo 接口里，避免业务域随手改权限）。
func (s *PGIdentityStore) SetRole(ctx context.Context, id int64, role model.Role) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET role = $2, updated_at = now() WHERE id = $1`, id, int16(role))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return model.ErrNotFound
	}
	return nil
}

// ── model.ProfileRepo ───────────────────────────────────────────────────

// Upsert 实现 model.ProfileRepo（整体写入；字段分权由业务域保证）。
func (s *PGIdentityStore) Upsert(ctx context.Context, p model.Profile) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_profiles
			(user_id, name, id_card_sealed, id_card_suffix4, college, major, class_name, grade,
			 qq, avatar_url, exam_no, gender, study_duration, education_level, new_major,
			 new_class_name, enrollment_status, enrollment_date, nation, political_status,
			 home_address, train_station, personal_phone, home_phone, specialty,
			 nickname, email, birthday, avatar_source_url, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,
		        $9,$10,$11,$12,$13,$14,$15,
		        $16,$17,$18,$19,$20,
		        $21,$22,$23,$24,$25,
		        $26,$27,$28,$29, now())
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
			exam_no = EXCLUDED.exam_no,
			gender = EXCLUDED.gender,
			study_duration = EXCLUDED.study_duration,
			education_level = EXCLUDED.education_level,
			new_major = EXCLUDED.new_major,
			new_class_name = EXCLUDED.new_class_name,
			enrollment_status = EXCLUDED.enrollment_status,
			enrollment_date = EXCLUDED.enrollment_date,
			nation = EXCLUDED.nation,
			political_status = EXCLUDED.political_status,
			home_address = EXCLUDED.home_address,
			train_station = EXCLUDED.train_station,
			personal_phone = EXCLUDED.personal_phone,
			home_phone = EXCLUDED.home_phone,
			specialty = EXCLUDED.specialty,
			nickname = EXCLUDED.nickname,
			email = EXCLUDED.email,
			birthday = EXCLUDED.birthday,
			avatar_source_url = EXCLUDED.avatar_source_url,
			updated_at = now()`,
		p.UserID, p.Name, p.IDCardSealed, p.IDCardSuffix4,
		p.College, p.Major, p.ClassName, p.Grade,
		p.QQ, p.AvatarURL, p.ExamNo, p.Gender, p.StudyDuration, p.EducationLevel, p.NewMajor,
		p.NewClassName, p.EnrollmentStatus, p.EnrollmentDate, p.Nation, p.PoliticalStatus,
		p.HomeAddress, p.TrainStation, p.PersonalPhone, p.HomePhone, p.Specialty,
		p.Nickname, p.Email, p.Birthday, p.AvatarSourceURL)
	return err
}

// ByUserID 实现 model.ProfileRepo。
func (s *PGIdentityStore) ByUserID(ctx context.Context, userID int64) (model.Profile, error) {
	var p model.Profile
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, name, id_card_sealed, id_card_suffix4, college, major, class_name, grade,
		       qq, avatar_url, exam_no, gender, study_duration, education_level, new_major,
		       new_class_name, enrollment_status, enrollment_date, nation, political_status,
		       home_address, train_station, personal_phone, home_phone, specialty,
		       nickname, email, birthday, avatar_source_url, updated_at
		  FROM user_profiles WHERE user_id = $1`, userID,
	).Scan(&p.UserID, &p.Name, &p.IDCardSealed, &p.IDCardSuffix4,
		&p.College, &p.Major, &p.ClassName, &p.Grade,
		&p.QQ, &p.AvatarURL, &p.ExamNo, &p.Gender, &p.StudyDuration, &p.EducationLevel, &p.NewMajor,
		&p.NewClassName, &p.EnrollmentStatus, &p.EnrollmentDate, &p.Nation, &p.PoliticalStatus,
		&p.HomeAddress, &p.TrainStation, &p.PersonalPhone, &p.HomePhone, &p.Specialty,
		&p.Nickname, &p.Email, &p.Birthday, &p.AvatarSourceURL, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Profile{}, model.ErrNotFound
	}
	if err != nil {
		return model.Profile{}, err
	}
	return p, nil
}

// ── model.LoginMethodRepo ───────────────────────────────────────────────

// Bind 实现 model.LoginMethodRepo。
func (s *PGIdentityStore) Bind(ctx context.Context, m model.LoginMethod) (model.LoginMethod, error) {
	var out model.LoginMethod
	var kind string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO login_methods (user_id, kind, identifier, secret_ref)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, kind, identifier, secret_ref, bound_at`,
		m.UserID, string(m.Kind), m.Identifier, m.SecretSealed,
	).Scan(&out.ID, &out.UserID, &kind, &out.Identifier, &out.SecretSealed, &out.BoundAt)
	if err != nil {
		if isUniqueViolation(err) {
			return model.LoginMethod{}, model.ErrAlreadyExists
		}
		return model.LoginMethod{}, err
	}
	out.Kind = model.LoginMethodKind(kind)
	return out, nil
}

// ByIdentifier 实现 model.LoginMethodRepo。
func (s *PGIdentityStore) ByIdentifier(ctx context.Context, kind model.LoginMethodKind, identifier string) (model.LoginMethod, error) {
	var m model.LoginMethod
	var k string
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, kind, identifier, secret_ref, bound_at
		  FROM login_methods WHERE kind = $1 AND identifier = $2`, string(kind), identifier,
	).Scan(&m.ID, &m.UserID, &k, &m.Identifier, &m.SecretSealed, &m.BoundAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.LoginMethod{}, model.ErrNotFound
	}
	if err != nil {
		return model.LoginMethod{}, err
	}
	m.Kind = model.LoginMethodKind(k)
	return m, nil
}

// ByUserAndKind 实现 model.LoginMethodRepo。
func (s *PGIdentityStore) ByUserAndKind(ctx context.Context, userID int64, kind model.LoginMethodKind) (model.LoginMethod, error) {
	var m model.LoginMethod
	var k string
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, kind, identifier, secret_ref, bound_at
		  FROM login_methods WHERE user_id = $1 AND kind = $2`, userID, string(kind),
	).Scan(&m.ID, &m.UserID, &k, &m.Identifier, &m.SecretSealed, &m.BoundAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.LoginMethod{}, model.ErrNotFound
	}
	if err != nil {
		return model.LoginMethod{}, err
	}
	m.Kind = model.LoginMethodKind(k)
	return m, nil
}

// UpdateSecret 实现 model.LoginMethodRepo。
func (s *PGIdentityStore) UpdateSecret(ctx context.Context, userID int64, kind model.LoginMethodKind, sealed []byte) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE login_methods SET secret_ref = $3 WHERE user_id = $1 AND kind = $2`,
		userID, string(kind), sealed)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return model.ErrNotFound
	}
	return nil
}

// Unbind 实现 model.LoginMethodRepo。
func (s *PGIdentityStore) Unbind(ctx context.Context, userID int64, kind model.LoginMethodKind) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM login_methods WHERE user_id = $1 AND kind = $2`, userID, string(kind))
	return err
}

// ── model.SessionStore ──────────────────────────────────────────────────

// CreateSession 实现 model.SessionStore。
func (s *PGIdentityStore) CreateSession(ctx context.Context, sess model.Session) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (id, user_id, scope, device_id, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5, now())`,
		sess.ID, sess.UserID, string(sess.Scope), sess.DeviceID, sess.ExpiresAt)
	return err
}

// SessionByID 实现 model.SessionStore。
func (s *PGIdentityStore) SessionByID(ctx context.Context, id string) (model.Session, error) {
	var sess model.Session
	var scope string
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, scope, device_id, expires_at, revoked_at, created_at
		  FROM sessions WHERE id = $1`, id,
	).Scan(&sess.ID, &sess.UserID, &scope, &sess.DeviceID,
		&sess.ExpiresAt, &sess.RevokedAt, &sess.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Session{}, model.ErrNotFound
	}
	if err != nil {
		return model.Session{}, err
	}
	sess.Scope = model.SessionScope(scope)
	return sess, nil
}

// Revoke 实现 model.SessionStore（幂等：已吊销或不存在都不报错）。
func (s *PGIdentityStore) Revoke(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	return err
}

// RevokeByUserScope 吊销该用户在该 scope 下所有未吊销会话，返回受影响条数。
// 「App 单设备登录顶替」与「管理台强制下线」都走这里。
func (s *PGIdentityStore) RevokeByUserScope(ctx context.Context, userID int64, scope model.SessionScope) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sessions SET revoked_at = now()
		 WHERE user_id = $1 AND scope = $2 AND revoked_at IS NULL`,
		userID, string(scope))
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
