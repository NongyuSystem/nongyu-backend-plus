package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---- from store.go ----
// ---- from store.go ----
// Store 是业务域可见的最小存储契约。
//
// 骨架期故意只定义一个方法：它表达的是「按 key 取一份配置/资源描述」这一抽象能力，
// 而不是任何具体数据库的形状。新增能力时在此扩展方法，不要另起一套平行接口。
type Store interface {
	// Get 按 key 读取一份值；key 不存在时返回 ErrNotFound。
	Get(ctx context.Context, key string) ([]byte, error)
}

// ErrNotFound 表示 key 不存在。
// 用哨兵错误而非返回 nil，让调用方能区分「没值」与「读失败」。
var ErrNotFound = &notFoundError{}

type notFoundError struct{}

func (e *notFoundError) Error() string { return "platform: key not found" }

// IsNotFound 判断错误是否为「key 不存在」。
func IsNotFound(err error) bool {
	_, ok := err.(*notFoundError)
	return ok
}

// ErrAlreadyExists 表示唯一约束冲突（如学号已被占用、登录方式已绑定）。
var ErrAlreadyExists = &alreadyExistsError{}

type alreadyExistsError struct{}

func (e *alreadyExistsError) Error() string { return "platform: already exists" }

// IsAlreadyExists 判断错误是否为唯一约束冲突。
func IsAlreadyExists(err error) bool {
	_, ok := err.(*alreadyExistsError)
	return ok
}

// ---- from pg_identity.go ----
// PGIdentityStore 是身份域四类仓储的 Postgres 实现（UserRepo / ProfileRepo /
// LoginMethodRepo / SessionStore 都在这一类型上，装配层按接口注入）。
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

// ── UserRepo ──────────────────────────────────────────────────────

// Create 实现 UserRepo。
func (s *PGIdentityStore) Create(ctx context.Context, studentNo string, role Role) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (student_no, role) VALUES ($1, $2)
		 RETURNING id, student_no, role, status, created_at,
		           last_login_ip, last_login_device, last_login_at`,
		studentNo, int16(role),
	).Scan(&u.ID, &u.StudentNo, &u.Role, &u.Status, &u.CreatedAt, &u.LastLoginIP, &u.LastLoginDevice, &u.LastLoginAt)
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
		`SELECT id, student_no, role, status, created_at, last_login_ip, last_login_device, last_login_at FROM users WHERE student_no = $1`,
		studentNo,
	).Scan(&u.ID, &u.StudentNo, &u.Role, &u.Status, &u.CreatedAt, &u.LastLoginIP, &u.LastLoginDevice, &u.LastLoginAt)
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
		`SELECT id, student_no, role, status, created_at, last_login_ip, last_login_device, last_login_at FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.StudentNo, &u.Role, &u.Status, &u.CreatedAt, &u.LastLoginIP, &u.LastLoginDevice, &u.LastLoginAt)
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

// ByUserID 实现 ProfileRepo。
func (s *PGIdentityStore) ByUserID(ctx context.Context, userID int64) (Profile, error) {
	var p Profile
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

// ---- from pg_audit.go ----
// PGOptions 是 Postgres 审计存储的配置。
type PGOptions struct {
	// DSN 形如 postgres://user:pass@host:port/db?sslmode=disable
	DSN string
	// BufferSize 是异步缓冲容量；满后丢弃新记录（审计宁可丢也不能挡住业务）。
	BufferSize int
	// BatchSize 是一次批量插入的最大条数。
	BatchSize int
	// FlushPeriod 是后台批写周期。
	FlushPeriod time.Duration
}

// PGAuditStore 是 AuditStore 的 Postgres 实现。
//
// 设计要点：
//   - Append 只做「入缓冲」，永不阻塞业务线程；
//   - 缓冲满时丢弃并计数（Dropped），而不是让登录因为审计写不进去而失败；
//   - 后台按 FlushPeriod 批量 INSERT，一次提交多条，减少往返。
type PGAuditStore struct {
	pool  *pgxpool.Pool
	opts  PGOptions
	ch    chan AuditEntry
	drops atomic.Int64

	closeOnce sync.Once
	done      chan struct{}
	stopCh    chan struct{}
	flushMu   sync.Mutex
}

// NewPGAuditStore 构造审计存储并启动后台批写协程。
func NewPGAuditStore(opts PGOptions) (*PGAuditStore, error) {
	if opts.DSN == "" {
		return nil, fmt.Errorf("platform: PGAuditStore 需要 DSN")
	}
	if opts.BufferSize <= 0 {
		opts.BufferSize = 1024
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 64
	}
	if opts.FlushPeriod <= 0 {
		opts.FlushPeriod = time.Second
	}

	cfg, err := pgxpool.ParseConfig(opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("platform: 解析 DSN 失败：%w", err)
	}
	// 快速失败：数据库不可用不应把请求拖到秒级。
	cfg.MaxConns = 4
	cfg.ConnConfig.ConnectTimeout = 2 * time.Second

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, err
	}

	s := &PGAuditStore{
		pool:   pool,
		opts:   opts,
		ch:     make(chan AuditEntry, opts.BufferSize),
		done:   make(chan struct{}),
		stopCh: make(chan struct{}),
	}
	go s.loop()
	return s, nil
}

// Ping 探测连通性（装配层启动期调用）。
func (s *PGAuditStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Append 投递一条审计。永不阻塞：缓冲满则丢弃并计数。
func (s *PGAuditStore) Append(_ context.Context, entry AuditEntry) error {
	select {
	case s.ch <- entry:
		return nil
	default:
		s.drops.Add(1)
		return nil
	}
}

// Dropped 返回因缓冲满被丢弃的条数。
func (s *PGAuditStore) Dropped() int64 { return s.drops.Load() }

func (s *PGAuditStore) loop() {
	defer close(s.done)
	ticker := time.NewTicker(s.opts.FlushPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			s.drainAndWrite()
			return
		case <-ticker.C:
			s.drainAndWrite()
		}
	}
}

// drainAndWrite 在写锁内完成「从 channel 取到写库」的整段，
// 保证 Flush（同样持锁）不会与后台协程交错，从而不丢记录。
func (s *PGAuditStore) drainAndWrite() {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	batch := make([]AuditEntry, 0, s.opts.BatchSize)
	for {
		select {
		case e := <-s.ch:
			batch = append(batch, e)
			if len(batch) >= s.opts.BatchSize {
				s.writeBatchLocked(batch)
				batch = batch[:0]
			}
		default:
			if len(batch) > 0 {
				s.writeBatchLocked(batch)
			}
			return
		}
	}
}

// writeBatchLocked 一次批量插入，调用方须已持有 flushMu。
// 失败只把该批计入丢弃数，绝不 panic、绝不阻塞业务。
func (s *PGAuditStore) writeBatchLocked(batch []AuditEntry) {
	if len(batch) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 用多行 VALUES 一次提交，减少往返。
	sql := `INSERT INTO audit_log (request_id, user_id, action, target, result, occurred_at) VALUES `
	args := make([]any, 0, len(batch)*6)
	for i, e := range batch {
		if i > 0 {
			sql += ", "
		}
		base := i * 6
		sql += fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6)
		args = append(args, e.RequestID, e.UserID, e.Action, e.Target, e.Result, e.OccurredAt)
	}
	if _, err := s.pool.Exec(ctx, sql, args...); err != nil {
		s.drops.Add(int64(len(batch)))
	}
}

// Flush 把当前缓冲同步落库（测试与关停前使用）。
// 与后台协程共用同一把写锁，因此不会出现「后台已取走但未写」的丢失窗口。
func (s *PGAuditStore) Flush(ctx context.Context) error {
	s.drainAndWrite()
	return s.pool.Ping(ctx)
}

// Close 停止后台协程并释放连接池。
func (s *PGAuditStore) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopCh)
		<-s.done
		s.pool.Close()
	})
	return nil
}

// QueryAuditByRequestID 按流水号查询审计记录（测试与运维排障用）。
func QueryAuditByRequestID(ctx context.Context, dsn, requestID string) ([]AuditEntry, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer pool.Close()

	rows, err := pool.Query(ctx,
		`SELECT request_id, user_id, action, target, result, occurred_at
		   FROM audit_log WHERE request_id = $1 ORDER BY id`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.RequestID, &e.UserID, &e.Action, &e.Target, &e.Result, &e.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- from identity.go ----
// 身份域的外部能力接口。业务域（internal/identity）只依赖这里，不依赖实现。
//
// 数据边界见 docs/adr/0002-身份仓数据边界.md：
//   - 密文字段一律以 []byte 传递，平台层不解释内容；
//   - 接口本身不暴露任何解密方法，加解密走 Cipher。

// Role 是三档权限，沿用旧农屿语义。
type Role int16

const (
	RoleUser      Role = 0 // 普通用户
	RoleAdmin     Role = 1 // 管理员
	RoleSuperUser Role = 2 // 超级管理员
)

// Valid 判断 role 是否是已知取值。
func (r Role) Valid() bool { return r >= RoleUser && r <= RoleSuperUser }

// UserStatus 是用户状态。
type UserStatus int16

const (
	UserDisabled UserStatus = 0
	UserActive   UserStatus = 1
)

// User 是身份域的用户实体（不含画像）。
type User struct {
	ID        int64
	StudentNo string
	Role      Role
	Status    UserStatus
	CreatedAt time.Time

	// ── 登录审计（来自客户端请求头 + 登录时刻，issue #29）──────────
	// 存在 users 而不是 profile：它们是「账号」属性，不是「画像」。
	LastLoginIP     string
	LastLoginDevice string
	// LastLoginAt 零值表示从未登录。
	// 用指针而不是 time.Time：last_login_at 在从未登录时是 SQL NULL，
	// 直接扫进 time.Time 会报 "cannot scan NULL into *time.Time"。
	LastLoginAt *time.Time
}

// Profile 是用户画像。学校档案字段与农屿扩展字段混在一行，
// 但**写入权限分开**：前者只允许服务端同步，后者只允许本人修改。
type Profile struct {
	UserID int64
	Name   string
	// IDCardSealed 是身份证号的密文；平台层不理解内容。
	IDCardSealed []byte
	// IDCardSuffix4 是派生的后四位，供登录方式校验（非明文全号）。
	IDCardSuffix4 string

	College   string
	Major     string
	ClassName string
	Grade     string

	QQ        string
	AvatarURL string

	// ── 以下字段来自教务档案页 bjiben.asp（issue #29）──────────────
	// 实测表头：考生号 | 学号 | 姓名 | 性别 | 系别 | 专业 | 学制 | 培养层次 |
	//          年级 | 班级 | 新专业 | 新班级 | 学籍状态 | 入学日期 | 民族 |
	//          政治面貌 | 家庭通讯地址 | 火车站点 | 个人电话 | 家庭电话 | 特长
	ExamNo           string // 考生号
	Gender           string // 性别
	StudyDuration    string // 学制（四年/三年…）
	EducationLevel   string // 培养层次（本科/研究生）
	NewMajor         string // 新专业
	NewClassName     string // 新班级
	EnrollmentStatus string // 学籍状态（在读/休学…）
	EnrollmentDate   string // 入学日期
	Nation           string // 民族
	PoliticalStatus  string // 政治面貌
	HomeAddress      string // 家庭通讯地址
	TrainStation     string // 火车站点
	PersonalPhone    string // 个人电话
	HomePhone        string // 家庭电话
	Specialty        string // 特长

	// ── 农屿扩展字段（本人可改，不被学校数据覆盖）──────────────────
	Nickname string // 昵称
	Email    string // 邮箱

	// ── 以下字段来自 ywtb getUserInfo（它需要 ywtb 自己的 Bearer）────
	// 生日：用于推算年龄；身份证本身加密存在 IDCardSealed。
	Birthday        string
	AvatarSourceURL string // ywtb 头像地址

	UpdatedAt time.Time
}

// LoginMethodKind 是登录方式类型。
type LoginMethodKind string

const (
	// LoginSchool 学校统一身份认证（账密 / 微信 / 电话）。唯一能直接建号的方式。
	LoginSchool LoginMethodKind = "school"
	// LoginPassword 农屿账密（密码镜像）。
	LoginPassword LoginMethodKind = "password"
	// LoginMailCode 农屿邮箱验证码。
	LoginMailCode LoginMethodKind = "mailcode"
	// LoginIDCard 学号 + 身份证后四位 + 姓名拼音首字母。
	LoginIDCard LoginMethodKind = "idcard"
)

// LoginMethod 是一条「这个用户可以用这种方式登录」的记录。
type LoginMethod struct {
	ID         int64
	UserID     int64
	Kind       LoginMethodKind
	Identifier string
	// SecretSealed 是凭据密文；为 nil 表示该方式尚未设置凭据（如农屿账密未设置）。
	SecretSealed []byte
	BoundAt      time.Time
}

// SessionScope 区分 App 与管理台两套会话。
type SessionScope string

const (
	ScopeApp   SessionScope = "app"
	ScopeAdmin SessionScope = "admin"
)

// Session 是一次登录的产物。ID 即令牌里携带的 session_id。
type Session struct {
	ID        string
	UserID    int64
	Scope     SessionScope
	DeviceID  string
	ExpiresAt time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}

// Active 判断会话当前是否可用于鉴权。
// 判定要点：未过期、未吊销。scope 与 device 的匹配由调用方按场景补充。
func (s Session) Active(now time.Time) bool {
	if s.RevokedAt != nil {
		return false
	}
	return now.Before(s.ExpiresAt)
}

// UserRepo 是用户的持久化能力。
type UserRepo interface {
	// Create 创建用户；学号已存在时返回 ErrAlreadyExists。
	Create(ctx context.Context, studentNo string, role Role) (User, error)
	// ByStudentNo 按学号查用户；不存在返回 ErrNotFound。
	ByStudentNo(ctx context.Context, studentNo string) (User, error)
	// ByID 按主键查用户；不存在返回 ErrNotFound。
	ByID(ctx context.Context, id int64) (User, error)
	// SetStatus 改用户状态。
	SetStatus(ctx context.Context, id int64, status UserStatus) error
	// TouchLogin 记录一次成功登录的审计三元组（IP / 设备 / 时间）。
	//
	// 只在**登录成功**时调用一次，不要在每个请求里写（避免写放大）。
	TouchLogin(ctx context.Context, id int64, ip, device string) error
}

// ProfileRepo 是画像的持久化能力。
type ProfileRepo interface {
	// Upsert 写入画像（整体）。业务域负责按字段分权后再调用。
	Upsert(ctx context.Context, p Profile) error
	// ByUserID 读画像；不存在返回 ErrNotFound。
	ByUserID(ctx context.Context, userID int64) (Profile, error)
}

// LoginMethodRepo 是登录方式的持久化能力。
type LoginMethodRepo interface {
	// Bind 绑定一条登录方式；(kind, identifier) 或 (user_id, kind) 冲突返回 ErrAlreadyExists。
	Bind(ctx context.Context, m LoginMethod) (LoginMethod, error)
	// ByIdentifier 按 (kind, identifier) 反查；不存在返回 ErrNotFound。
	ByIdentifier(ctx context.Context, kind LoginMethodKind, identifier string) (LoginMethod, error)
	// ByUserAndKind 按 (userID, kind) 查；不存在返回 ErrNotFound。
	ByUserAndKind(ctx context.Context, userID int64, kind LoginMethodKind) (LoginMethod, error)
	// UpdateSecret 更新凭据密文（密码镜像写入 / 清除时传 nil）。
	UpdateSecret(ctx context.Context, userID int64, kind LoginMethodKind, sealed []byte) error
	// Unbind 解绑。
	Unbind(ctx context.Context, userID int64, kind LoginMethodKind) error
}

// SessionStore 是会话的持久化能力。Postgres 为权威，实现可另加 Redis 副本。
type SessionStore interface {
	// CreateSession 写入一条会话。
	CreateSession(ctx context.Context, s Session) error
	// SessionByID 读会话；不存在返回 ErrNotFound。
	SessionByID(ctx context.Context, id string) (Session, error)
	// Revoke 吊销一条会话（幂等）。
	Revoke(ctx context.Context, id string) error
	// RevokeByUserScope 吊销某用户在某 scope 下的全部有效会话，返回受影响条数。
	// 用于「App 单设备登录顶替」与「管理台强制下线」。
	RevokeByUserScope(ctx context.Context, userID int64, scope SessionScope) (int, error)
}

// StudentBrief 是给其它业务域读取画像的**最小字段集**。
// 刻意不含身份证、QQ、头像等——最小暴露由接口形状保证，而非调用方自觉。
type StudentBrief struct {
	StudentNo string
	Name      string
	College   string
	Major     string
	ClassName string
	Grade     string
}

// StudentBriefReader 是 identity 域对外提供的窄读接口，
// 由 identity/user 实现、在装配层注册，其它业务域只 import 本包。
type StudentBriefReader interface {
	Brief(ctx context.Context, studentNo string) (StudentBrief, error)
}

// ---- from testschema.go ----
// ---- from testschema.go ----
// 集成测试的数据库隔离。
//
// 问题：多个测试包并行运行时共用同一个 Postgres，会互相看到/删除对方的数据，
// 表现为「单独跑通过、连跑失败」。实测踩过：identity 包与 flow 包用同一张 users 表。
//
// 方案：每个测试包用**独立 schema**。schema 名由包名派生（如 `t_identity`），
// 包启动时创建、跑完清理自己的行。这样：
//   - 并行安全：不同包写不同 schema；
//   - 无需多套数据库实例；
//   - 生产 DSN 不变，只在测试里改 search_path。

// TestSchemaName 由包名派生一个合法且可读的 schema 名。
//
// 例："nongyu-backend-plus/internal/identity" → "t_internal_identity_ab12cd"
// 后缀取哈希前 6 位，避免过长的包路径碰撞。
func TestSchemaName(pkgPath string) string {
	clean := strings.TrimPrefix(pkgPath, "nongyu-backend-plus/")
	clean = strings.TrimPrefix(clean, "internal/")
	replacer := strings.NewReplacer("/", "_", "-", "_", ".", "_")
	clean = replacer.Replace(clean)

	sum := sha256.Sum256([]byte(pkgPath))
	suffix := hex.EncodeToString(sum[:])[:6]

	name := "t_" + clean + "_" + suffix
	// Postgres 标识符上限 63；超出则截断保留后缀。
	if len(name) > 63 {
		keep := 63 - len(suffix) - 2
		if keep < 1 {
			keep = 1
		}
		name = name[:keep] + "_" + suffix
	}
	return strings.ToLower(name)
}

// WithSearchPath 往 DSN 里加上 search_path，把连接限定到指定 schema。
//
// 支持 `postgres://...` 形式的 URL DSN。
func WithSearchPath(dsn, schema string) (string, error) {
	if schema == "" {
		return dsn, nil
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("platform: DSN 解析失败：%w", err)
	}
	q := u.Query()
	// libpq 的 search_path 选项；pgx 会透传。
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// EnsureTestSchema 创建 schema 并返回一个指向它的新 DSN。
//
// 调用方应在测试包初始化时执行一次，用完不必删除（下次运行复用，
// 但每个包的测试自己负责清掉自己的行）。
func EnsureTestSchema(ctx context.Context, dsn, pkgPath string) (string, error) {
	admin, err := NewPGIdentityStore(PGOptions{DSN: dsn})
	if err != nil {
		return "", err
	}
	defer admin.Close()

	schema := TestSchemaName(pkgPath)
	// 建 schema 与建表都在这个 schema 下：用 SET search_path 的方式逐条执行。
	if err := admin.ExecDDL(ctx, "CREATE SCHEMA IF NOT EXISTS "+quoteIdent(schema)); err != nil {
		return "", err
	}
	// 首次使用时把四张表也建到这个 schema 里（幂等）。
	if err := admin.ExecDDLInSchema(ctx, schema, identityDDLStatements()); err != nil {
		return "", err
	}
	return WithSearchPath(dsn, schema)
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// ---- from testschema_ddl.go ----
// 测试用 DDL：与 migrations/0002_identity.sql 保持一致。
//
// 为什么不直接读 migrations 文件：测试二进制的工作目录不固定，
// 读文件容易因路径失败而把「测试没跑」伪装成「测试通过」。
// 这里内嵌一份，并在测试里断言它与迁移文件同构（见 testschema_test.go）。

// ExecDDL 执行一条 DDL（调用方负责拼好标识符）。
func (s *PGIdentityStore) ExecDDL(ctx context.Context, stmt string) error {
	_, err := s.pool.Exec(ctx, stmt)
	return err
}

// ExecDDLInSchema 在指定 schema 下执行一组 DDL。
//
// 通过临时设置连接的 search_path 实现；同一个连接内执行，避免影响池里其它连接。
func (s *PGIdentityStore) ExecDDLInSchema(ctx context.Context, schema string, stmts []string) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SET search_path TO "+quoteIdent(schema)); err != nil {
		return err
	}
	for _, stmt := range stmts {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// DeleteUser 按主键删除用户（CASCADE 带走画像、登录方式与会话）。
//
// 仅供**测试清理**使用：生产代码里没有「物理删除用户」这条业务，
// 用户下线应该用 SetStatus(UserDisabled)。
func (s *PGIdentityStore) DeleteUser(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	return err
}

// identityDDLStatements 返回测试 schema 需要的全部建表语句（幂等）。
// 与 migrations/ 下的迁移保持一致：0001 审计表 + 0002 身份域四表。
func identityDDLStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS audit_log (
			id          bigserial   PRIMARY KEY,
			request_id  text        NOT NULL,
			user_id     text        NOT NULL DEFAULT '',
			action      text        NOT NULL,
			target      text        NOT NULL DEFAULT '',
			result      text        NOT NULL DEFAULT '',
			occurred_at timestamptz NOT NULL,
			created_at  timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id          bigserial   PRIMARY KEY,
			student_no  char(9)     NOT NULL UNIQUE,
			role        smallint    NOT NULL DEFAULT 0 CHECK (role IN (0, 1, 2)),
			status      smallint    NOT NULL DEFAULT 1 CHECK (status IN (0, 1)),
			created_at  timestamptz NOT NULL DEFAULT now(),
			updated_at  timestamptz NOT NULL DEFAULT now(),
			-- 登录审计（issue #29）：迁移用 ALTER ADD 追加，列顺序在 updated_at 之后
			last_login_ip     text NOT NULL DEFAULT '',
			last_login_device text NOT NULL DEFAULT '',
			last_login_at     timestamptz
		)`,
		`CREATE TABLE IF NOT EXISTS user_profiles (
			user_id         bigint      PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
			name            text        NOT NULL DEFAULT '',
			id_card_sealed  bytea,
			id_card_suffix4 char(4),
			college         text        NOT NULL DEFAULT '',
			major           text        NOT NULL DEFAULT '',
			class_name      text        NOT NULL DEFAULT '',
			grade           text        NOT NULL DEFAULT '',
			qq              text        NOT NULL DEFAULT '',
			avatar_url      text        NOT NULL DEFAULT '',
			updated_at      timestamptz NOT NULL DEFAULT now(),
			-- 教务档案页字段（issue #29）：迁移用 ALTER ADD 追加，列顺序在 updated_at 之后
			exam_no           text NOT NULL DEFAULT '',
			gender            text NOT NULL DEFAULT '',
			study_duration    text NOT NULL DEFAULT '',
			education_level   text NOT NULL DEFAULT '',
			new_major         text NOT NULL DEFAULT '',
			new_class_name    text NOT NULL DEFAULT '',
			enrollment_status text NOT NULL DEFAULT '',
			enrollment_date   text NOT NULL DEFAULT '',
			nation            text NOT NULL DEFAULT '',
			political_status  text NOT NULL DEFAULT '',
			home_address      text NOT NULL DEFAULT '',
			train_station     text NOT NULL DEFAULT '',
			personal_phone    text NOT NULL DEFAULT '',
			home_phone        text NOT NULL DEFAULT '',
			specialty         text NOT NULL DEFAULT '',
			-- 农屿扩展（本人可改）
			nickname          text NOT NULL DEFAULT '',
			email             text NOT NULL DEFAULT '',
			-- ywtb 字段
			birthday          text NOT NULL DEFAULT '',
			avatar_source_url text NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS login_methods (
			id          bigserial   PRIMARY KEY,
			user_id     bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
			kind        text        NOT NULL,
			identifier  text        NOT NULL DEFAULT '',
			secret_ref  bytea,
			bound_at    timestamptz NOT NULL DEFAULT now(),
			UNIQUE (kind, identifier),
			UNIQUE (user_id, kind)
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id          text        PRIMARY KEY,
			user_id     bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
			scope       text        NOT NULL CHECK (scope IN ('app', 'admin')),
			device_id   text        NOT NULL DEFAULT '',
			expires_at  timestamptz NOT NULL,
			revoked_at  timestamptz,
			created_at  timestamptz NOT NULL DEFAULT now()
		)`,
	}
}

// ── 学工系统（xsgz）完整档案落库 ──────────────────────────────────
//
// 唯一数据源：xsgz detail 端点。客户端完成统一登录后把 main-app-access-token
// 交给后端，后端取数并落到这里。jiaowu / ywtb 的取数逻辑已 disable。
//
// 落库策略（对应迁移 0004）：
//   - student_profiles：用户清单里的常用字段，正规列
//   - student_profile_groups：13 个分组的完整原文（jsonb），不丢任何字段
//   - student_family_members / student_resumes：列表分组独立成表
//
// 全部在一个事务里完成；重复同步按 student_no 覆盖。

// XSGZProfileInput 是落库所需的输入（由 schoolauth.XSGZStudentDetail 转换而来）。
//
// 刻意不直接依赖 schoolauth 包（platform 是最底层，不能反向依赖业务域）：
// 调用方负责把 detail 的 Groups / Typed 拆成这个结构。
type XSGZProfileInput struct {
	StudentNo string
	// 常用字段（对应 student_profiles 的列）
	Fields map[string]string
	// 分组原文：key = 分组名，value = 该分组的完整 JSON
	Groups map[string]string
	// 列表分组原文：每个元素是一条完整 JSON
	FamilyMembers []string
	Resumes       []string
	// 身份证密文与派生后四位（由上层加密后传入，本层不解释）
	IDCardSealed  []byte
	IDCardSuffix4 string
	Age           int
}

// SaveXSGZProfile 把学工档案完整落库（事务内 4 张表一起写）。
//
// 幂等：按 student_no 覆盖；分组与列表分组采用「先删后插」，保证与本次同步
// 结果完全一致，不会残留上游已删除的家庭成员/简历。
func (s *PGIdentityStore) SaveXSGZProfile(ctx context.Context, in XSGZProfileInput) error {
	if strings.TrimSpace(in.StudentNo) == "" {
		return fmt.Errorf("platform: SaveXSGZProfile 缺少学号")
	}
	f := func(k string) string { return in.Fields[k] }

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// ① 主表（常用字段）
	_, err = tx.Exec(ctx, `
		INSERT INTO student_profiles (
			student_no, one_card_no, student_name, sex, sex_name, nationality, nationality_name,
			id_card_sealed, id_card_suffix4, birthday, photo,
			college_no, college_name, specialty_no, specialty_name, grade, class_no, class_name,
			campus_name, dorm_address, study_years, in_school_date, student_state_name, current_state_name,
			examinee_no, high_school, source_location,
			email, mobile_phone, wechat, qq,
			emergency_contact, emergency_relationship, emergency_telephone, emergency_address,
			family_address, family_detail_address, family_telephone, family_postal_no,
			name_pinyin, blood_type_name, height, weight, native_place,
			nickname, avatar_url, age, synced_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,
			$8,$9,$10,$11,
			$12,$13,$14,$15,$16,$17,$18,
			$19,$20,$21,$22,$23,$24,
			$25,$26,$27,
			$28,$29,$30,$31,
			$32,$33,$34,$35,
			$36,$37,$38,$39,
			$40,$41,$42,$43,$44,
			$45,$46,$47, now(), now()
		)
		ON CONFLICT (student_no) DO UPDATE SET
			one_card_no=EXCLUDED.one_card_no, student_name=EXCLUDED.student_name,
			sex=EXCLUDED.sex, sex_name=EXCLUDED.sex_name,
			nationality=EXCLUDED.nationality, nationality_name=EXCLUDED.nationality_name,
			id_card_sealed=EXCLUDED.id_card_sealed, id_card_suffix4=EXCLUDED.id_card_suffix4,
			birthday=EXCLUDED.birthday, photo=EXCLUDED.photo,
			college_no=EXCLUDED.college_no, college_name=EXCLUDED.college_name,
			specialty_no=EXCLUDED.specialty_no, specialty_name=EXCLUDED.specialty_name,
			grade=EXCLUDED.grade, class_no=EXCLUDED.class_no, class_name=EXCLUDED.class_name,
			campus_name=EXCLUDED.campus_name, dorm_address=EXCLUDED.dorm_address,
			study_years=EXCLUDED.study_years, in_school_date=EXCLUDED.in_school_date,
			student_state_name=EXCLUDED.student_state_name, current_state_name=EXCLUDED.current_state_name,
			examinee_no=EXCLUDED.examinee_no, high_school=EXCLUDED.high_school,
			source_location=EXCLUDED.source_location,
			email=EXCLUDED.email, mobile_phone=EXCLUDED.mobile_phone,
			wechat=EXCLUDED.wechat, qq=EXCLUDED.qq,
			emergency_contact=EXCLUDED.emergency_contact,
			emergency_relationship=EXCLUDED.emergency_relationship,
			emergency_telephone=EXCLUDED.emergency_telephone,
			emergency_address=EXCLUDED.emergency_address,
			family_address=EXCLUDED.family_address,
			family_detail_address=EXCLUDED.family_detail_address,
			family_telephone=EXCLUDED.family_telephone, family_postal_no=EXCLUDED.family_postal_no,
			name_pinyin=EXCLUDED.name_pinyin, blood_type_name=EXCLUDED.blood_type_name,
			height=EXCLUDED.height, weight=EXCLUDED.weight, native_place=EXCLUDED.native_place,
			-- 农屿扩展字段：保留本人已填值，不被学校数据覆盖
			nickname=student_profiles.nickname, avatar_url=student_profiles.avatar_url,
			age=EXCLUDED.age, synced_at=now(), updated_at=now()`,
		in.StudentNo,
		f("oneCardNo"), f("studentName"), f("sex"), f("sexName"), f("nationality"), f("nationalityName"),
		in.IDCardSealed, in.IDCardSuffix4, f("birthday"), f("photo"),
		f("collegeNo"), f("collegeName"), f("specialtyNo"), f("specialtyName"), f("grade"), f("classNo"), f("className"),
		f("campusName"), f("dormAddress"), f("studyYears"), f("inSchoolDate"), f("studentStateName"), f("currentStateName"),
		f("examineeNo"), f("highSchool"), f("sourceLocation"),
		f("email"), f("mobilePhone"), f("wechat"), f("qq"),
		f("emergencyContact"), f("emergencyContactRelationship"), f("emergencyContactTelephone"), f("emergencyContactAddress"),
		f("familyAddress"), f("familyDetailAddress"), f("familyTelephone"), f("familyPostalNo"),
		f("namePinyin"), f("bloodTypeName"), f("height"), f("weight"), f("nativePlace"),
		f("nickname"), f("avatarUrl"), in.Age)
	if err != nil {
		return fmt.Errorf("写 student_profiles 失败：%w", err)
	}

	// ② 13 个分组的完整原文（jsonb）
	_, err = tx.Exec(ctx, `DELETE FROM student_profile_groups WHERE student_no = $1`, in.StudentNo)
	if err != nil {
		return err
	}
	for key, payload := range in.Groups {
		if payload == "" {
			payload = "{}"
		}
		if _, err = tx.Exec(ctx,
			`INSERT INTO student_profile_groups (student_no, group_key, payload) VALUES ($1,$2,$3::jsonb)`,
			in.StudentNo, key, payload); err != nil {
			return fmt.Errorf("写分组 %s 失败：%w", key, err)
		}
	}

	// ③ 家庭成员列表
	if _, err = tx.Exec(ctx, `DELETE FROM student_family_members WHERE student_no = $1`, in.StudentNo); err != nil {
		return err
	}
	for i, raw := range in.FamilyMembers {
		if _, err = tx.Exec(ctx, `
			INSERT INTO student_family_members
				(student_no, ordinal, uuid, name, relationship, relationship_name, unit, occupation, contact_number, is_guarder, payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb)`,
			in.StudentNo, i,
			JSONStringField(raw, "uuid"), JSONStringField(raw, "name"),
			JSONStringField(raw, "relationship"), JSONStringField(raw, "relationshipName"),
			JSONStringField(raw, "unit"), JSONStringField(raw, "occupation"),
			JSONStringField(raw, "contactNumber"), JSONStringField(raw, "isGuarder"),
			orEmptyObject(raw)); err != nil {
			return fmt.Errorf("写家庭成员 #%d 失败：%w", i, err)
		}
	}

	// ④ 教育经历列表
	if _, err = tx.Exec(ctx, `DELETE FROM student_resumes WHERE student_no = $1`, in.StudentNo); err != nil {
		return err
	}
	for i, raw := range in.Resumes {
		if _, err = tx.Exec(ctx, `
			INSERT INTO student_resumes
				(student_no, ordinal, uuid, begin_date, end_date, study_unit, position, award, payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)`,
			in.StudentNo, i,
			JSONStringField(raw, "uuid"), JSONStringField(raw, "beginDate"), JSONStringField(raw, "endDate"),
			JSONStringField(raw, "studyUnit"), JSONStringField(raw, "position"), JSONStringField(raw, "award"),
			orEmptyObject(raw)); err != nil {
			return fmt.Errorf("写教育经历 #%d 失败：%w", i, err)
		}
	}

	return tx.Commit(ctx)
}

// jsonStringField 从一条 JSON 对象里安全取字符串字段（number/bool 也能取字面量）。
func JSONStringField(raw, key string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &m) != nil {
		return ""
	}
	v, ok := m[key]
	if !ok || len(v) == 0 || string(v) == "null" {
		return ""
	}
	if v[0] == '"' {
		var s string
		if json.Unmarshal(v, &s) == nil {
			return s
		}
	}
	return string(v)
}

func orEmptyObject(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	return raw
}

// BuildXSGZProfileInput 把学工 detail 的「分组原文 + 常用字段」转成落库输入。
//
// 放在 platform 层、参数是纯数据结构，避免 platform 反向依赖 schoolauth。
func BuildXSGZProfileInput(
	studentNo string,
	groups map[string]json.RawMessage,
	typed XSGZTypedLite,
	familyMembers, resumes []json.RawMessage,
	idCardSealed []byte, idCardSuffix4 string,
) XSGZProfileInput {
	fields := map[string]string{
		"oneCardNo": typed.OneCardNo, "studentName": typed.StudentName,
		"sex": typed.Sex, "sexName": typed.SexName,
		"nationality": typed.Nationality, "nationalityName": typed.NationalityName,
		"birthday": typed.Birthday, "photo": typed.Photo,
		"collegeNo": typed.CollegeNo, "collegeName": typed.CollegeName,
		"specialtyNo": typed.SpecialtyNo, "specialtyName": typed.SpecialtyName,
		"grade": typed.Grade, "classNo": typed.ClassNo, "className": typed.ClassName,
		"campusName": typed.CampusName, "dormAddress": typed.DormAddress,
		"studyYears": typed.StudyYears, "inSchoolDate": typed.InSchoolDate,
		"studentStateName": typed.StudentStateName, "currentStateName": typed.CurrentStateName,
		"examineeNo": typed.ExamineeNo, "highSchool": typed.HighSchool,
		"sourceLocation": typed.SourceLocation,
		"email":          typed.Email, "mobilePhone": typed.MobilePhone,
		"wechat": typed.Wechat, "qq": typed.QQ,
		"emergencyContact":             typed.EmergencyContact,
		"emergencyContactRelationship": typed.EmergencyRelationship,
		"emergencyContactTelephone":    typed.EmergencyTelephone,
		"emergencyContactAddress":      typed.EmergencyAddress,
		"familyAddress":                typed.FamilyAddress,
		"familyDetailAddress":          typed.FamilyDetailAddress,
		"familyTelephone":              typed.FamilyTelephone, "familyPostalNo": typed.FamilyPostalNo,
		"namePinyin": typed.NamePinyin, "bloodTypeName": typed.BloodTypeName,
		"height": typed.Height, "weight": typed.Weight, "nativePlace": typed.NativePlace,
		"nickname": typed.Nickname, "avatarUrl": typed.AvatarURL,
	}
	g := make(map[string]string, len(groups))
	for k, v := range groups {
		g[k] = string(v)
	}
	fm := make([]string, 0, len(familyMembers))
	for _, v := range familyMembers {
		fm = append(fm, string(v))
	}
	rs := make([]string, 0, len(resumes))
	for _, v := range resumes {
		rs = append(rs, string(v))
	}
	return XSGZProfileInput{
		StudentNo: studentNo, Fields: fields, Groups: g,
		FamilyMembers: fm, Resumes: rs,
		IDCardSealed: idCardSealed, IDCardSuffix4: idCardSuffix4, Age: typed.Age,
	}
}

// XSGZTypedLite 是 schoolauth.XSGZTyped 的镜像（platform 层不依赖业务域）。
type XSGZTypedLite struct {
	OneCardNo, StudentName, Sex, SexName                                          string
	Nationality, NationalityName, Birthday, Photo                                 string
	CollegeNo, CollegeName, SpecialtyNo, SpecialtyName                            string
	Grade, ClassNo, ClassName, CampusName, DormAddress                            string
	StudyYears, InSchoolDate, StudentStateName, CurrentStateName                  string
	ExamineeNo, HighSchool, SourceLocation                                        string
	Email, MobilePhone, Wechat, QQ                                                string
	EmergencyContact, EmergencyRelationship, EmergencyTelephone, EmergencyAddress string
	FamilyAddress, FamilyDetailAddress, FamilyTelephone, FamilyPostalNo           string
	NamePinyin, BloodTypeName, Height, Weight, NativePlace                        string
	Nickname, AvatarURL                                                           string
	Age                                                                           int
}
