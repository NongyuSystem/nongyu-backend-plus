package pg

// 测试基建用的 DDL 执行辅助。
//
// 这些函数原先是 *PGIdentityStore 的方法，但那会让 platform/pg 依赖身份域仓储。
// 改为接受 *pgxpool.Pool 的独立函数后，测试基建与业务仓储彻底解耦：
// platform/pg 只管「怎么连库、怎么建 schema」，不管「谁是身份仓储」。
//
// 这些方法只服务测试隔离与运维批处理，业务代码不应调用。

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ExecDDL 执行一条 DDL（调用方负责拼好标识符）。
func ExecDDL(ctx context.Context, pool *pgxpool.Pool, stmt string) error {
	_, err := pool.Exec(ctx, stmt)
	return err
}

// ExecDDLInSchema 在指定 schema 下执行一组 DDL。
//
// 通过临时设置连接的 search_path 实现；同一个连接内执行，避免影响池里其它连接。
func ExecDDLInSchema(ctx context.Context, pool *pgxpool.Pool, schema string, stmts []string) error {
	conn, err := pool.Acquire(ctx)
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
func DeleteUser(ctx context.Context, pool *pgxpool.Pool, id int64) error {
	_, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	return err
}
