// 兼容性转发层：把「已按能力拆到子包」的实现符号重新暴露在 platform 根。
//
// 背景（2026-11 结构整改）：pg/redis/cache 实现已分别搬进 platform/pg、platform/redis、
// platform/cache。但外部有上百处 `platform.NewRedisCache(...)` / `platform.NewPGIdentityStore(...)`
// 等调用，逐一改写风险大且毫无收益。
//
// 这里用**类型别名 + 变量/函数转发**保持 platform.Xxx 全部可用，且不产生 import 环：
// 根 import 子包，子包只 import 零依赖的 model/ 叶子包，不 import 根。
//
// 新代码建议直接用子包（cache.NewRedisCache / pg.NewPGIdentityStore / redis.NewRedisTicketStore），
// 让依赖关系更显式；本文件只为兼容既有调用而存在。
package platform

import (
	"nongyu-backend-plus/internal/platform/cache"
	"nongyu-backend-plus/internal/platform/mail"
	"nongyu-backend-plus/internal/platform/pg"
	redisx "nongyu-backend-plus/internal/platform/redis"
)

// ── cache 子包 ──
type (
	RedisClient    = cache.RedisClient
	LockBackend    = cache.LockBackend
	CacheOptions   = cache.CacheOptions
	RedisCache     = cache.RedisCache
	Loader         = cache.Loader
	CacheTx        = cache.CacheTx
	CacheStore     = cache.CacheStore
	BreakerState   = cache.BreakerState
	IdempotentLock = cache.IdempotentLock
)

const (
	BreakerClosed   = cache.BreakerClosed
	BreakerOpen     = cache.BreakerOpen
	BreakerHalfOpen = cache.BreakerHalfOpen
)

var ErrCacheLocked = cache.ErrCacheLocked

var (
	NewRedisClient    = cache.NewRedisClient
	NewRedisCache     = cache.NewRedisCache
	NewIdempotentLock = cache.NewIdempotentLock
)

// ── redis 子包 ──
type (
	RedisTicketStore  = redisx.RedisTicketStore
	RedisSessionCache = redisx.RedisSessionCache
)

var (
	NewRedisTicketStore  = redisx.NewRedisTicketStore
	NewRedisSessionCache = redisx.NewRedisSessionCache
)

// ── 迁移与测试基建（platform/pg）──
// 注：身份仓储（PGIdentityStore / PGOptions 等）已搬到 internal/identity/store，
// **不在本文件转发**——那会让 platform 反向依赖业务域，违反架构约束。
// 调用方请直接用 identitystore.NewPGIdentityStore(...)。
var (
	Migrate               = pg.Migrate
	TestSchemaName        = pg.TestSchemaName
	WithSearchPath        = pg.WithSearchPath
	EnsureTestSchema      = pg.EnsureTestSchema
	QueryAuditByRequestID = mail.QueryAuditByRequestID
)
