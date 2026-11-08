# Redis 基础能力：缓存防护、降级回灌、幂等锁与事务

面向运维与后续开发者。本文件说明这些能力**怎么用、边界在哪、哪些是可选的**。

## 一、两套东西，别混用

项目里 Redis 承担两种截然不同的职责，代码上刻意分开：

| 用途 | 类型 | 能否丢 | 实现 |
| --- | --- | --- | --- |
| 票据（登录态） | **持久数据** | **不能丢** | `RedisTicketStore` |
| 通用缓存 | **可再生缓存** | 可以丢 | `RedisCache` |

票据走 `RedisTicketStore`，没有穿透/雪崩保护——因为「空值缓存」和「TTL 抖动」用在票据上会让票据意外消失、造成全员重登。缓存该有的防护只在 `RedisCache` 上。

## 二、通用缓存层（`internal/platform/cache.go`）

入口是 `GetOrLoad(ctx, key, loader)`，四类问题一次解决：

| 问题 | 症状 | 本实现的解法 |
| --- | --- | --- |
| **缓存穿透** | 查一个不存在的 key，每次都打到下游 | 下游返回「查无此物」时写入空值标记，用 `NotFoundTTL`（默认 15s）短缓存，短期内不再回源 |
| **缓存雪崩** | 同一批 key 同时过期，请求瞬间全打下游 | 所有 TTL 按 `CacheJitterRatio`（默认 ±20%）抖动，打散过期时刻 |
| **缓存击穿** | 单个热点 key 失效瞬间，大量并发同时回源 | 进程内 singleflight：同 key 并发只放行一个重建，其余等结果（有 `RebuildTimeout` 上限） |
| **Redis 不可用** | 直接报错，功能全挂 | 读写降级到进程内存，对调用方透明；同时熔断避免每次请求都等连接超时 |

### 降级与回灌

- Redis 写失败或已熔断 → 写入内存并从调用方视角「成功」，同时记入待回灌队列。
- 后台循环每秒调一次 `FlushOnce`，每次最多 `FlushRatePerSec` 条 → **限速回灌**，恢复瞬间不会把 Redis 打满。
- 关停前会再冲一次积压，尽量不丢降级期间写入。
- `PendingBackfill()` 可观测积压量。

### 熔断

三态（Closed / Open / HalfOpen）：连续失败 `BreakerThreshold` 次（默认 5）后打开，直接降级**不再触碰 Redis**；冷却 `BreakerCooldown`（默认 5s）后转半开放行一次探测，成功回 Closed。
`BreakerState()` 可观测当前状态。

实测效果：Redis 不可达时，单次写操作从「等连接超时」的数百毫秒降到熔断后的近乎立即返回。

## 三、幂等锁（`internal/platform/lock.go`）

用于「同一业务动作只执行一次」：重复下单、重复提交、重复回调。

- `TryLock(ctx, key, ttl)`：`SET NX` + TTL，进程崩溃会自动释放，不会永久死锁。
- `Unlock`：**Lua 原子比较-删除**，只删自己的令牌，避免「A 的锁超时后被 B 拿到、A 误删 B 的锁」。
- 语义提醒：这是**幂等锁**不是分布式互斥锁，TTL 是它的安全阀也是它的破绽——业务执行时间必须显著短于 TTL。

## 四、事务（`RedisCache.Transaction`）

`fn` 内不直接打 Redis，而是先累积操作，提交时用 `MULTI/EXEC` 一次性写入。
因此 `fn` 返回错误时**Redis 侧零写入**——「回滚」不需要撤销，没提交就等于没发生。

边界：这是「要么全写要么全不写」，不是数据库意义上的隔离事务；`fn` 里读不到自己未提交的写入。

## 五、配置项

| 环境变量 | 默认 | 说明 |
| --- | --- | --- |
| `REDIS_ADDR` | 空 | 为空则用进程内存（仅本地）；**prod 强校验要求必填** |
| `REDIS_PASSWORD` / `REDIS_DB` / `REDIS_PREFIX` | 空 / 0 / `nongyu:` | 连接与命名空间 |
| `CACHE_DEFAULT_TTL_SEC` | 300 | 正常值默认存活 |
| `CACHE_JITTER_RATIO` | 0.2 | TTL 抖动比例 |
| `CACHE_NOT_FOUND_TTL_SEC` | 15 | 空值缓存存活（防穿透） |
| `CACHE_FLUSH_RATE_PER_SEC` | 100 | 恢复后回灌限速 |
| `CACHE_BREAKER_THRESHOLD` | 5 | 连续失败多少次打开熔断 |
| `CACHE_BREAKER_COOLDOWN_SEC` | 5 | 熔断冷却时长 |

所有 Redis 连接都设了 `MaxRetries=1` 与 500ms 超时：坏依赖不该把请求拖到秒级。

## 六、没做的（明确边界）

- **闲时定时备份到数据库：未实现。** 票据本身已持久化在 Redis；若还要防「Redis 整体丢失」，更该用 Redis 自带的 RDB/AOF 与主从，而不是在应用层导出到数据库。如果你确有「审计级留存」需求，再单独提。
- **热 key / 大 key 探测**：当前没有内置监控，建议接 `redis_exporter` + 告警，而不是在应用里做。
- **分布式互斥锁（Redlock）**：没做。本项目的幂等锁不承担强互斥职责。
- **缓存与数据库的双写一致性**：本项目缓存的值都可再生，没有 DB 作为真相源，所以不涉及。

## 七、验证方式

所有 Redis 相关测试用 `REDIS_ADDR`/`REDIS_PASSWORD` 环境变量连真实 Redis，未配置时自动跳过，保证 `go test ./...` 不依赖环境：

```powershell
$env:REDIS_ADDR="127.0.0.1:6379"; $env:REDIS_PASSWORD="<你的密码>"
go test -count=1 ./internal/platform/...
```
