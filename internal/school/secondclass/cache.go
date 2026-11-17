// Package secondclass 的缓存子域：回传校验、命中直返、stale 回落、失效节流。
//
// 形状与教务 cache.go 同构（自己实现、不 import 教务域），但两处语义按二课规格调整：
//   - 错误码 / 响应形状按规格 §3.2 / §6.2（不是教务的 schema_rejected / cacheMissResponse）；
//   - 新鲜度窗口是 A 组四组 TTL（12m / 45m / 8h / 24h），不是教务的 10/30/60min。
//
// 两套时间语义沿用教务的区分（规格 §6.10 明确要求）：
//
//	Redis TTL    = staleMaxAge 多留一天缓冲     —— 数据最多留 7 天（承载「再旧就不给」）
//	新鲜度窗口   = dt.TTL()（按 dataType 分组）—— 决定正常 GET 判不判 miss
//
// 正常 GET：age ≤ 新鲜度窗口 → 命中（stale=false）；超过 → cache_miss（让客户端去取新数据）。
// fallback=1：age ≤ 7 天 → 下发旧数据并标 stale=true（二课副本可能一直是空表，回落尤其重要）。
package secondclass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	// staleMaxAge 是旧数据下发的硬上限（规格 §6.12 定 7 天）。
	staleMaxAge = 7 * 24 * time.Hour
	// redisRetention 是写进 Redis 的实际存活时间：比 staleMaxAge 多留一天，
	// 抵消 RedisCache.Set 的 TTL 抖动，精确的 7 天由读路径按 fetchedAt 判定。
	redisRetention = staleMaxAge + 24*time.Hour
	// refreshInterval 是同一用户同一 dataType 的失效节流间隔（规格 §6.12 定 60 秒）。
	refreshInterval = 60 * time.Second
	// maxSubmitBytes 是回传体上限（规格 §6.12 定 1 MiB），handler 用 MaxBytesReader 兜住。
	maxSubmitBytes = 1 << 20
)

// 缓存子域的哨兵错误，供 handler 稳定映射 HTTP 状态与错误码。
var (
	ErrCacheUnavailable = errors.New("secondclass: 缓存层未配置")
	ErrCacheMiss        = errors.New("secondclass: 缓存未命中")
	ErrRefreshTooSoon   = errors.New("secondclass: 刷新过于频繁")
)

// InvalidPayloadError 表示回传体超限或 schema 不合格（规格 §3.2 的 invalid_payload）。
// 单独成类型是为了让 handler 稳定映射成 400，而不是落进 500 兜底。
type InvalidPayloadError struct{ Reason error }

func (e *InvalidPayloadError) Error() string {
	return "secondclass: 回传体不合格: " + e.Reason.Error()
}
func (e *InvalidPayloadError) Unwrap() error { return e.Reason }

// cachedEntry 是真正写进缓存的内容。
// FetchedAt 由服务端 Clock.Now() 盖章，不采信客户端时间（否则能绕过 7 天上限）。
type cachedEntry struct {
	Payload       json.RawMessage `json:"payload"`
	SchemaVersion int             `json:"schemaVersion"`
	FetchedAt     time.Time       `json:"fetchedAt"`
}

// CacheSnapshot 是一次缓存读取的结果，直接对应 GET 的响应信封。
type CacheSnapshot struct {
	Payload       json.RawMessage
	SchemaVersion int
	FetchedAt     time.Time
	Stale         bool
}

// cacheDataKey 返回 dataType 数据在缓存层的键（不含 platform 的全局前缀与 cache: 段）。
//
// 最终键为 nongyu:cache:<userID>:<dataType>（规格 §6.9）。键里一律带 userID，
// 绝不引入全局共享键：实测 ActDetailVo / ActivityMainVo 含用户态字段，共享键必然串数据。
func cacheDataKey(userID string, dt DataType) string {
	return userID + ":" + string(dt)
}

// cacheInvalidateKey 是失效节流标记的键，与数据键同域但不会碰撞。
func cacheInvalidateKey(userID string, dt DataType) string {
	return userID + ":invalidate:" + string(dt)
}

// cacheReady 检查缓存写入/读取所需的两件能力是否就位。
func (s *Service) cacheReady() error {
	if s.cache == nil {
		return ErrCacheUnavailable
	}
	if s.schema == nil {
		return fmt.Errorf("secondclass: schema 校验器未注入")
	}
	return nil
}

// CacheGet 读一次缓存。
//
// fallback=true 表示「客户端刚刚直连二课失败，请把旧数据给我」——只有这种调用会返回
// Stale=true。fallback=false 时数据一旦超过该类的新鲜度窗口就当未命中。
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
		if age > staleMaxAge {
			return CacheSnapshot{}, ErrCacheMiss
		}
		return snapshotOf(entry, true), nil
	}
	if age > dt.TTL() {
		return CacheSnapshot{}, ErrCacheMiss
	}
	return snapshotOf(entry, false), nil
}

// CacheSubmit 校验并写入一次回传。
//
// 顺序是「先校验再写」：校验不过就回 InvalidPayloadError，且**一个字节都不写**，
// 保证坏数据永远不会顶掉能读的旧数据（规格 §6.12 / 验收标准第 4 条）。
func (s *Service) CacheSubmit(ctx context.Context, userID string, dt DataType, body []byte) (CacheSnapshot, error) {
	if err := s.cacheReady(); err != nil {
		return CacheSnapshot{}, err
	}
	if len(body) > maxSubmitBytes {
		s.audit(ctx, userID, "secondclass.record", "invalid_payload", string(dt))
		return CacheSnapshot{}, &InvalidPayloadError{Reason: fmt.Errorf("回传体超过 %d 字节", maxSubmitBytes)}
	}

	// 1) 形状校验（含 schemaVersion 必须为 1）。
	if err := s.schema.Validate(dt, body); err != nil {
		s.audit(ctx, userID, "secondclass.record", "schema_rejected", string(dt))
		return CacheSnapshot{}, &InvalidPayloadError{Reason: err}
	}

	// 2) 抽出要缓存的字段。校验已通过，这里的字段一定存在且类型正确。
	var in struct {
		Payload       json.RawMessage `json:"payload"`
		SchemaVersion int             `json:"schemaVersion"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		s.audit(ctx, userID, "secondclass.record", "schema_rejected", string(dt))
		return CacheSnapshot{}, &InvalidPayloadError{Reason: err}
	}

	entry := cachedEntry{
		Payload:       in.Payload,
		SchemaVersion: in.SchemaVersion,
		FetchedAt:     s.clock.Now(), // 服务端盖章
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return CacheSnapshot{}, fmt.Errorf("secondclass: 序列化缓存条目失败: %w", err)
	}
	if err := s.cache.Set(ctx, cacheDataKey(userID, dt), raw, redisRetention); err != nil {
		s.audit(ctx, userID, "secondclass.record", "store_failed", string(dt))
		return CacheSnapshot{}, fmt.Errorf("secondclass: 写缓存失败: %w", err)
	}
	s.audit(ctx, userID, "secondclass.record", "ok", string(dt))
	return snapshotOf(entry, false), nil
}

// CacheInvalidate 让该用户该类型的缓存失效（规格 §6.12：60 秒节流）。
//
// 用 TryAcquire 做间隔标记：它是「熔断返回 (false,nil)」的节流语义，
// **不是** fail-closed 互斥（探索笔记「不要照抄」第 6 条）。熔断/不可判定时同样回
// 429，宁可让客户端等一会儿，也不在缓存层故障时放开失效洪峰。
func (s *Service) CacheInvalidate(ctx context.Context, userID string, dt DataType) error {
	if s.cache == nil {
		return ErrCacheUnavailable
	}
	acquired, err := s.cache.TryAcquire(ctx, cacheInvalidateKey(userID, dt), refreshInterval)
	if err != nil {
		return fmt.Errorf("secondclass: 刷新节流判定失败: %w", err)
	}
	if !acquired {
		s.audit(ctx, userID, "secondclass.invalidate", "too_soon", string(dt))
		return ErrRefreshTooSoon
	}
	if err := s.cache.Delete(ctx, cacheDataKey(userID, dt)); err != nil {
		s.audit(ctx, userID, "secondclass.invalidate", "delete_failed", string(dt))
		return fmt.Errorf("secondclass: 删除缓存失败: %w", err)
	}
	s.audit(ctx, userID, "secondclass.invalidate", "ok", string(dt))
	return nil
}

// RefreshRetryAfter 返回「刷新过于频繁」时建议客户端等待的秒数。
func RefreshRetryAfter() int { return int(refreshInterval / time.Second) }

// snapshotOf 把缓存的内部条目转成对外快照。
func snapshotOf(entry cachedEntry, stale bool) CacheSnapshot {
	return CacheSnapshot{
		Payload:       entry.Payload,
		SchemaVersion: entry.SchemaVersion,
		FetchedAt:     entry.FetchedAt,
		Stale:         stale,
	}
}
