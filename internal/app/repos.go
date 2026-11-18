package app

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/content/announcement"
	"nongyu-backend-plus/internal/content/post"
	"nongyu-backend-plus/internal/message"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/skin"
	"nongyu-backend-plus/internal/track"

	announcementstore "nongyu-backend-plus/internal/content/announcement/store"
	poststore "nongyu-backend-plus/internal/content/post/store"
	messagestore "nongyu-backend-plus/internal/message/store"
	skinstore "nongyu-backend-plus/internal/skin/store"
	trackstore "nongyu-backend-plus/internal/track/store"
)

// 本文件是「存储实现选择」的唯一决策点。
//
// 为什么要有它：五个域各有内存与 PG 两套仓储，若在 app.New 里逐处写
// `if pgPool != nil { ... } else { ... }`，切换条件会散落五处，
// 极易出现「一个域切了、另一个忘了」。集中成工厂后，
// 「配了 DATABASE_URL 就全切 PG」是一条可验证的不变式。
//
// 一条硬约束：**PG 与内存是二选一，不允许混用**。
// 混用会产生极难排查的现象——帖子落了库、留言留在内存，重启后
// 「帖子还在但回复没了」。所以这里按 storeMode 一次决定全部五个域。
//
// 各域的仓储实现已搬到 `<域>/store/` 子包（2026-11 结构整改）。
// 它们都叫 store，故此处按域名起别名——这也是「域内固定分层」的直观体现。

// storeMode 是运行期的存储模式。
type storeMode string

const (
	// storeModeMemory 是进程内存实现：重启即丢，仅限本地开发与测试。
	storeModeMemory storeMode = "memory"
	// storeModePostgres 是真实持久化。
	storeModePostgres storeMode = "postgres"
)

// repoBundle 汇总五个域的仓储实例。
//
// 用结构体而不是五个独立返回值：装配层要一次性拿全，
// 且将来新增域时只需在这里加一个字段，调用方签名不变。
type repoBundle struct {
	Announcements announcement.Repo
	Posts         post.Repo
	Messages      message.Repo
	Track         track.Repo
	Skins         skin.Repo
	// Mode 供日志与 /status 报告「现在到底用的哪套」。
	// 这个值必须如实反映实现，否则运维会误以为数据在库里。
	Mode storeMode
}

// newRepoBundle 按配置构造五个域的仓储。
//
// pool 为 nil 表示未配 DATABASE_URL，五个域全部走内存实现。
// 只要 pool 非 nil 就全部走 PG——不允许单个域降级，
// 理由见文件头「不允许混用」。
func newRepoBundle(pool *pgxpool.Pool, now func() time.Time) (repoBundle, error) {
	if pool == nil {
		return repoBundle{
			Announcements: announcementstore.NewAnnouncementMemoryRepo(now),
			Posts:         poststore.NewPostMemoryRepo(now),
			Messages:      messagestore.NewMessageMemoryRepo(now),
			Track:         trackstore.NewTrackMemoryRepo(),
			Skins:         skinstore.NewSkinMemoryRepo(now),
			Mode:          storeModeMemory,
		}, nil
	}

	// PG 路径：构造失败直接返回错误（装配期失败优于带着坏依赖上线）。
	b, err := newPGRepoBundle(pool, now)
	if err != nil {
		return repoBundle{}, err
	}
	return b, nil
}

// openPGPool 在配了 DATABASE_URL 时开连接池并探测连通性。
//
// 返回 (nil, nil) 表示未配置——调用方据此走内存实现。
// 配了但连不上则返回错误：**不能静默降级成内存**，
// 那会让「生产环境看起来正常、实际一条数据都没落库」。
func openPGPool(ctx context.Context, cfgDSN string) (*pgxpool.Pool, error) {
	if cfgDSN == "" {
		return nil, nil
	}
	pool, err := pgxpool.New(ctx, cfgDSN)
	if err != nil {
		return nil, fmt.Errorf("app: Postgres 连接池创建失败：%w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("app: Postgres 不可达：%w", err)
	}
	return pool, nil
}

// 编译期确认：内存实现确实满足各域接口（防止接口漂移后工厂悄悄返回错类型）。
var (
	_ announcement.Repo = (*announcementstore.AnnouncementMemoryRepo)(nil)
	_ post.Repo         = (*poststore.PostMemoryRepo)(nil)
	_ message.Repo      = (*messagestore.MessageMemoryRepo)(nil)
	_ track.Repo        = (*trackstore.TrackMemoryRepo)(nil)
	_ skin.Repo         = (*skinstore.SkinMemoryRepo)(nil)
	_                   = platform.MemoryObjectStore{}
)
