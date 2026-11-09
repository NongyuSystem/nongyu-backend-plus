package platform

// 测试用 DDL：与 migrations/0002_identity.sql 保持一致。
//
// 为什么不直接读 migrations 文件：测试二进制的工作目录不固定，
// 读文件容易因路径失败而把「测试没跑」伪装成「测试通过」。
// 这里内嵌一份，并在测试里断言它与迁移文件同构（见 testschema_test.go）。

import "context"

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
			updated_at  timestamptz NOT NULL DEFAULT now()
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
			updated_at      timestamptz NOT NULL DEFAULT now()
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
