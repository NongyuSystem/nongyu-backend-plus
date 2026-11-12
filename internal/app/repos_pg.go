package app

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/platform/pgstore"
)

// newPGRepoBundle 构造四个域的 PostgreSQL 仓储。
//
// 单独一个文件而不是与 newRepoBundle 同处：内存与 PG 两条路径的依赖
// 完全不同（PG 路径要 pgxpool 与表结构知识），分文件让「切 PG 时
// 需要动什么」一眼可见——事实上这正是本文件存在的意义，
// 它是整条持久化切换路径上**唯一**需要改的地方。
//
// 前置条件由调用方保证：`platform.Migrate` 已执行完（表与索引已建好），
// 且 pool 已 Ping 通过。这里不做存在性探测——那些是装配层早先步骤的职责，
// 在这里重复只会让失败原因变模糊。
func newPGRepoBundle(pool *pgxpool.Pool, now func() time.Time) (repoBundle, error) {
	if pool == nil {
		// 调用方只在 pool 非 nil 时才走到这里；显式报错而不是返回空 bundle，
		// 因为空 bundle 会让业务域拿到 nil 仓储、在第一个请求时空指针。
		return repoBundle{}, errNilPool
	}
	return repoBundle{
		Announcements: pgstore.NewAnnouncementPGRepo(pool, now),
		Posts:         pgstore.NewPostPGRepo(pool, now),
		Messages:      pgstore.NewMessagePGRepo(pool, now),
		Track:         pgstore.NewTrackPGRepo(pool),
		Mode:          storeModePostgres,
	}, nil
}

// errNilPool 是「要求 PG 但没给连接池」的错误。
//
// 单独定义而不是 fmt.Errorf 内联：装配层与测试都要判断这个条件，
// 用一个可比较的哨兵值比比对字符串可靠。
type nilPoolError struct{}

func (nilPoolError) Error() string {
	return "app: 要求 PostgreSQL 仓储但连接池为 nil（检查 DATABASE_URL 与 openPGPool）"
}

var errNilPool = nilPoolError{}
