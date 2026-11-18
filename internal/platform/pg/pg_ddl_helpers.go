package pg

import (
	"context"
)

// PGIdentityStore 的 DDL 执行辅助（原属 testschema_ddl.go，搬迁时因
// **Go 不允许为其它包的类型定义方法**而留在本包）。
//
// 这些方法只服务测试隔离与运维批处理，业务代码不应调用。

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
