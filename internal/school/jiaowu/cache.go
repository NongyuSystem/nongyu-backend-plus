// Package jiaowu 的缓存子域：回传校验、命中直返、stale 回落、失效节流。
//
// 对应决议：#40（端点与信封）、#41（schema 校验）、#32（键/TTL/stale/空值）、
// #34（失效与 60 秒间隔）、#39（错误码映射）。
//
// 关于「TTL 与 7 天 stale 上限」的一处实现取舍（必须显式说明，否则两个数字互相抵销）：
//
//   - #32 定了每类数据的 TTL（通知 10 分钟 / 考试 30 分钟 / 其余 1 小时）；
//   - 同时又定了 stale 旧数据的硬上限 7 天。
//
// 如果把这组 TTL 直接当 Redis 的过期时间，缓存在 10 分钟后就物理消失，
// 「7 天上限」永远触发不到，stale 回落也基本没机会用上（放假回来一定读不到旧课表）。
// 因此这里把两个数字拆成两个语义：
//
//	Redis TTL      = staleMaxAge 多留一天缓冲 —— 数据最多留 7 天（承载「再旧就不给」）
//	新鲜度窗口      = 每类的 TTL（dataTypeSpec.ttl）—— 决定正常 GET 会不会判 miss
//
// 于是：
//   - 正常 GET：age ≤ 新鲜度窗口 → 命中（stale=false）；超过 → cache_miss，让客户端去取新数据。
//   - 回传失败后带 fallback=1 再取：age ≤ 7 天 → 下发旧数据并标 stale=true（#32 只允许
//     「客户端上报失败」触发 stale=true，所以正常 GET 不会返回 stale=true）。
//
// 这个拆法让 #32 的两个数字都真正生效，且不改变它对外的语义。
package jiaowu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// CacheStore 是教务缓存对 platform 缓存层的最小依赖面。
//
// 只声明实际用到的四个方法，让业务域不依赖 *platform.RedisCache 这个具体类型：
// 装配层注入真实现，测试注入内存假实现，两边都不用起 Redis。
type CacheStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	TryAcquire(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

const (
	// staleMaxAge 是旧数据下发的硬上限（#32 定 7 天）。
	// 7 天足以覆盖假期，又避免把去年课表当当前数据下发。
	staleMaxAge = 7 * 24 * time.Hour

	// redisRetention 是写进 Redis 的实际存活时间。
	// 比 staleMaxAge 多留一天：RedisCache.Set 会给 TTL 加抖动（默认 ±20%），
	// 若正好写 staleMaxAge，抖动可能把它压到 7 天以下，让最后一段窗口凭空消失。
	// 精确的「7 天」由读路径按 fetchedAt 判定，不依赖 Redis 何时淘汰。
	redisRetention = staleMaxAge + 24*time.Hour

	// refreshInterval 是同一用户同一 dataType 的失效间隔（#34 定 60 秒）。
	refreshInterval = 60 * time.Second

	// maxSubmitBytes 是回传体上限（#40 定 1 MiB）。handler 用 MaxBytesReader 兜住。
	maxSubmitBytes = 1 << 20
)

// 缓存子域的哨兵错误，供 handler 稳定映射 HTTP 状态与错误码。
var (
	// ErrCacheUnavailable 表示本进程没有配置缓存层（REDIS_ADDR 为空）。
	ErrCacheUnavailable = errors.New("jiaowu: 缓存层未配置")
	// ErrCacheMiss 表示没有可下发的数据（从未写过、已过新鲜度窗口，或已超 7 天）。
	ErrCacheMiss = errors.New("jiaowu: 缓存未命中")
	// ErrRefreshTooSoon 表示在刷新间隔内重复请求失效（#34 的 429）。
	ErrRefreshTooSoon = errors.New("jiaowu: 刷新过于频繁")
)

// SchemaRejectedError 表示回传体形状校验未通过（#41 的 schema_rejected）。
//
// 单独成类型是为了让 handler 稳定映射成 400，而不是落进 500 兜底——
// 校验失败是「调用方数据不对」，不是「服务端内部错误」。
type SchemaRejectedError struct{ Reason error }

func (e *SchemaRejectedError) Error() string {
	return "jiaowu: 回传体形状校验未通过: " + e.Reason.Error()
}

func (e *SchemaRejectedError) Unwrap() error { return e.Reason }

// cachedEntry 是真正写进缓存的内容。
//
// FetchedAt 由服务端盖章，不采信客户端时间：否则客户端改系统时间就能绕过 7 天上限。
type cachedEntry struct {
	Payload       json.RawMessage `json:"payload"`
	Success       *bool           `json:"success,omitempty"`
	SchemaVersion int             `json:"schemaVersion"`
	FetchedAt     time.Time       `json:"fetchedAt"`
}

// CacheSnapshot 是一次缓存读取的结果，直接对应 GET 的响应信封。
type CacheSnapshot struct {
	Payload       json.RawMessage
	Success       *bool
	SchemaVersion int
	FetchedAt     time.Time
	Stale         bool
}

// cacheDataKey 返回 dataType 数据在缓存层的键（不含 platform 的全局前缀与 cache: 段）。
//
// 最终键为 <prefix>cache:<userID>:<dataType>，即 nongyu:cache:<userID>:<dataType>（#32）。
// 键里一律带 userID，公开数据也不例外（#37 N5：不引入全局共享键）。
func cacheDataKey(userID string, dt DataType) string {
	return userID + ":" + string(dt)
}

// cacheInvalidateKey 是失效节流标记的键，与数据键同域但不会碰撞。
func cacheInvalidateKey(userID string, dt DataType) string {
	return userID + ":invalidate:" + string(dt)
}

// BindCache 注入缓存能力。
//
// 为 nil 时缓存端点返回 503（ErrCacheUnavailable），而不是启动失败——
// 缓存层是可选依赖，与 platform.RedisCache「未配置即为 nil」的既有约定一致。
func (s *Service) BindCache(store CacheStore) {
	s.cache = store
}

// BindSchemaValidator 注入已编译的 schema 校验器。
func (s *Service) BindSchemaValidator(v *SchemaValidator) {
	s.schema = v
}

// cacheReady 检查缓存写入/读取所需的两件能力是否就位。
func (s *Service) cacheReady() error {
	if s.cache == nil {
		return ErrCacheUnavailable
	}
	if s.schema == nil {
		return fmt.Errorf("jiaowu: schema 校验器未注入")
	}
	return nil
}

// CacheGet 读一次缓存。
//
// fallback=true 表示「客户端刚刚直连教务失败，请把旧数据给我」——只有这种调用会返回
// Stale=true（#32 把 stale 的触发方限定为「客户端上报失败」）。fallback=false 时，
// 数据一旦超过该类的新鲜度窗口就当未命中，让客户端去取新数据。
func (s *Service) CacheGet(ctx context.Context, userID string, dt DataType, fallback bool) (CacheSnapshot, error) {
	if s.cache == nil {
		return CacheSnapshot{}, ErrCacheUnavailable
	}

	raw, err := s.cache.Get(ctx, cacheDataKey(userID, dt))
	if err != nil {
		return CacheSnapshot{}, err
	}
	if len(raw) == 0 {
		return CacheSnapshot{}, ErrCacheMiss
	}

	var entry cachedEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		// 缓存里躺着解不开的东西：当未命中，并顺手清掉，避免后续每次都读坏值。
		s.logger.Warn("缓存内容无法解析，按未命中处理并清除", "dataType", string(dt), "err", err)
		if derr := s.cache.Delete(ctx, cacheDataKey(userID, dt)); derr != nil {
			s.logger.Warn("清除坏缓存失败", "dataType", string(dt), "err", derr)
		}
		return CacheSnapshot{}, ErrCacheMiss
	}

	age := s.clock.Now().Sub(entry.FetchedAt)
	if age < 0 {
		// fetchedAt 在未来（时钟回拨或外部写入）：当成刚写入，避免负年龄绕过上限。
		age = 0
	}

	if fallback {
		// 回落路径：只受 7 天硬上限约束。
		if age > staleMaxAge {
			return CacheSnapshot{}, ErrCacheMiss
		}
		return snapshotOf(entry, true), nil
	}

	// 正常路径：超过该类的新鲜度窗口就判未命中（数据仍在，供 fallback 用）。
	if age > dt.TTL() {
		return CacheSnapshot{}, ErrCacheMiss
	}
	return snapshotOf(entry, false), nil
}

// CacheSubmit 校验并写入一次回传。
//
// 顺序是「先校验再写」：校验不过就回 schema_rejected，且**一个字节都不写**，
// 保证坏数据永远不会顶掉能读的旧数据（#41 Q5）。
func (s *Service) CacheSubmit(ctx context.Context, userID string, dt DataType, body []byte) (CacheSnapshot, error) {
	if err := s.cacheReady(); err != nil {
		return CacheSnapshot{}, err
	}
	if len(body) > maxSubmitBytes {
		return CacheSnapshot{}, &SchemaRejectedError{Reason: fmt.Errorf("回传体超过 %d 字节", maxSubmitBytes)}
	}

	// 1) 形状校验（含 schemaVersion 必须为 1）。
	if err := s.schema.Validate(dt, body); err != nil {
		s.audit(ctx, userID, "cache_submit", "schema_rejected", string(dt))
		return CacheSnapshot{}, &SchemaRejectedError{Reason: err}
	}

	// 2) 抽出要缓存的字段。校验已通过，这里的字段一定存在且类型正确。
	var in struct {
		Payload       json.RawMessage `json:"payload"`
		Success       *bool           `json:"success"`
		SchemaVersion int             `json:"schemaVersion"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		// 理论上走不到：schema 已保证是合法 JSON 对象。
		s.audit(ctx, userID, "cache_submit", "schema_rejected", string(dt))
		return CacheSnapshot{}, &SchemaRejectedError{Reason: err}
	}

	entry := cachedEntry{
		Payload:       in.Payload,
		Success:       in.Success,
		SchemaVersion: in.SchemaVersion,
		FetchedAt:     s.clock.Now(), // 服务端盖章
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return CacheSnapshot{}, fmt.Errorf("jiaowu: 序列化缓存条目失败: %w", err)
	}

	if err := s.cache.Set(ctx, cacheDataKey(userID, dt), raw, redisRetention); err != nil {
		s.audit(ctx, userID, "cache_submit", "store_failed", string(dt))
		return CacheSnapshot{}, fmt.Errorf("jiaowu: 写缓存失败: %w", err)
	}

	s.audit(ctx, userID, "cache_submit", "ok", string(dt))
	return snapshotOf(entry, false), nil
}

// CacheInvalidate 让该用户该类型的缓存失效（#34）。
//
// 只删本用户该 dataType 的键：零出站、不级联。同一用户同一类型 60 秒内只允许一次，
// 用缓存层的 SET NX 做间隔标记——不释放，到期自动放开，正好是「节流」而非「互斥」。
func (s *Service) CacheInvalidate(ctx context.Context, userID string, dt DataType) error {
	if s.cache == nil {
		return ErrCacheUnavailable
	}

	acquired, err := s.cache.TryAcquire(ctx, cacheInvalidateKey(userID, dt), refreshInterval)
	if err != nil {
		return fmt.Errorf("jiaowu: 刷新节流判定失败: %w", err)
	}
	if !acquired {
		// 两种情况都会落到这里：真在窗口内重复请求，或缓存层熔断。
		// 熔断时 TryAcquire 返回 (false, nil)，此处同样回 429——保守选择：
		// 宁可让客户端等一会儿，也不在缓存层故障时放开失效洪峰。
		s.audit(ctx, userID, "cache_invalidate", "too_soon", string(dt))
		return ErrRefreshTooSoon
	}

	if err := s.cache.Delete(ctx, cacheDataKey(userID, dt)); err != nil {
		s.audit(ctx, userID, "cache_invalidate", "delete_failed", string(dt))
		return fmt.Errorf("jiaowu: 删除缓存失败: %w", err)
	}

	s.audit(ctx, userID, "cache_invalidate", "ok", string(dt))
	return nil
}

// RefreshRetryAfter 返回「刷新过于频繁」时建议客户端等待的秒数。
func RefreshRetryAfter() int { return int(refreshInterval / time.Second) }

// snapshotOf 把缓存的内部条目转成对外快照。
func snapshotOf(entry cachedEntry, stale bool) CacheSnapshot {
	return CacheSnapshot{
		Payload:       entry.Payload,
		Success:       entry.Success,
		SchemaVersion: entry.SchemaVersion,
		FetchedAt:     entry.FetchedAt,
		Stale:         stale,
	}
}
