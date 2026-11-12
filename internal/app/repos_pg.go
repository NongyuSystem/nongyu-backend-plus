package app

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newPGRepoBundle 构造四个域的 PostgreSQL 仓储。
//
// 单独一个文件而不是与 newRepoBundle 同处：内存与 PG 两条路径的依赖
// 完全不同（PG 路径要 pgxpool 与表结构知识），分文件让「切 PG 时
// 需要动什么」一眼可见。
func newPGRepoBundle(pool *pgxpool.Pool, now func() time.Time) (repoBundle, error) {
	b := repoBundle{Mode: storeModePostgres}
	_ = fmt.Sprint
	_ = now
	_ = pool
	return b, fmt.Errorf("app: PostgreSQL 仓储尚未实现（待 internal/platform/pgstore 的 PG 实现落地）")
}
