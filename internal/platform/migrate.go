package platform

// 迁移执行能力（issue #23 / Spec M4 / 验收第 3 条）。
//
// 落点理由：迁移是「把 Postgres 建成业务域期望的形状」，与 PGIdentityStore /
// PGAuditStore 同属 platform 这一「外部能力适配层」；而 app 装配层只负责
// **编排**（决定何时调用），不该持有 pgxpool 构造细节。把 Migrate 放在 platform，
// 装配层、cmd 工具、测试都能直接调用同一个公共 seam，无需各自拼连接池。
//
// 幂等来源：migrations/*.sql 里所有语句都是 CREATE TABLE IF NOT EXISTS /
// CREATE INDEX IF NOT EXISTS（票里已核查确认）。因此每次调用把全部脚本按
// 文件名升序重放一遍是安全的——不引入版本表，避免「版本表与 IF NOT EXISTS
// 两套真相」以及测试隔离 schema 里版本表残留导致的假通过。
// 幂等性由 migrate_test.go 在空库上连跑两次并校验数据不丢来守护。

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/migrations"
)

// Migrate 在 dsn 指向的库（及其 search_path 指定的 schema）上执行全部迁移。
//
// 行为契约：
//   - 按 migrations/*.sql 的**文件名升序**逐份执行；一份脚本作为一个整体提交到
//     一次 Exec（pgx v5 支持多语句 + 注释，已实测），保证 0002 的外键能引用 0001/0002
//     已建好的表；
//   - 空库执行建表成功；重复执行不报错，且不删改已有数据（幂等）；
//   - dsn 里的 search_path 会被 pgxpool.ParseConfig 透传，故集成测试的独立
//     schema 隔离对本能力同样生效——迁移只落在调用方限定的 schema 里；
//   - 自带连接池，调用结束即关闭，不与业务连接池争用。
//
// 供 app.New（配了 DATABASE_URL 时启动即跑）、测试、以及运维单独调用。
func Migrate(ctx context.Context, dsn string) error {
	if dsn == "" {
		return fmt.Errorf("platform: Migrate 需要 DSN")
	}

	files, err := migrations.Files()
	if err != nil {
		return fmt.Errorf("platform: 读取迁移脚本失败：%w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("platform: 没有可执行的迁移脚本")
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("platform: 解析迁移 DSN 失败：%w", err)
	}
	// 迁移是启动期/运维期动作，用小池 + 快速失败，坏依赖不拖启动。
	cfg.MaxConns = 2
	cfg.ConnConfig.ConnectTimeout = 3 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("platform: 建迁移连接池失败：%w", err)
	}
	defer pool.Close()

	for _, f := range files {
		if _, err := pool.Exec(ctx, f.SQL); err != nil {
			return fmt.Errorf("platform: 执行迁移 %s 失败：%w", f.Name, err)
		}
	}
	return nil
}
