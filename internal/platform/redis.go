package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisTicketStore 是 TicketStore 的外置持久实现：进程重启不丢票、支持多副本共享。
//
// 存储布局（prefix 可配，默认 "nongyu:"）：
//   - <prefix>ticket:<userID>   → JSON{sealed, expiresAt}，会话级记录不设 Redis TTL
//   - <prefix>expiries          → 有序集合，score = expiresAt.Unix()，member = userID
//
// 把过期时刻单独放有序集合，是为了让 ListExpiringSoon 用 ZRANGEBYSCORE 取候选，
// 避免扫描全部 key（键空间可能很大）。
type RedisTicketStore struct {
	client *redis.Client
	prefix string
}

// RedisOptions 是 Redis 票据存储的连接与命名空间配置。
type RedisOptions struct {
	Addr     string // 形如 127.0.0.1:6379
	Password string
	DB       int
	Prefix   string // 键名前缀，便于多环境隔离；空值用默认 "nongyu:"
}

type redisTicketRecord struct {
	Sealed    []byte `json:"sealed"`
	ExpiresAt int64  `json:"expiresAt"` // Unix 秒；0 表示会话级（无明确过期）
}

// NewRedisTicketStore 构造外置票据存储并验证连通性由调用方决定（用 Ping）。
func NewRedisTicketStore(opts RedisOptions) (*RedisTicketStore, error) {
	if opts.Addr == "" {
		return nil, fmt.Errorf("platform: RedisTicketStore 需要 Addr")
	}
	if opts.Prefix == "" {
		opts.Prefix = "nongyu:"
	}
	client := redis.NewClient(&redis.Options{
		Addr:     opts.Addr,
		Password: opts.Password,
		DB:       opts.DB,
	})
	return &RedisTicketStore{client: client, prefix: opts.Prefix}, nil
}

// Ping 验证连通性，供装配层在启动期探测（失败即拒绝启动，避免带着坏依赖上线）。
func (s *RedisTicketStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

// Close 释放连接池。
func (s *RedisTicketStore) Close() error { return s.client.Close() }

func (s *RedisTicketStore) recordKey(userID string) string {
	return s.prefix + "ticket:" + userID
}

func (s *RedisTicketStore) expiryKey() string {
	return s.prefix + "expiries"
}

// Put 写入/覆盖一条记录。expiresAt 为零值表示会话级（不写过期索引、不设 TTL）。
func (s *RedisTicketStore) Put(ctx context.Context, userID string, sealed []byte, expiresAt time.Time) error {
	rec := redisTicketRecord{Sealed: sealed}
	if !expiresAt.IsZero() {
		rec.ExpiresAt = expiresAt.Unix()
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}

	pipe := s.client.TxPipeline()
	// 记录本身按「略长于过期时刻」留存，避免索引还在而记录已消失；
	// 会话级记录不设 TTL（由 Logout 显式删除）。
	if rec.ExpiresAt > 0 {
		ttl := time.Until(expiresAt)
		if ttl < time.Minute {
			ttl = time.Minute
		}
		pipe.Set(ctx, s.recordKey(userID), raw, ttl)
		pipe.ZAdd(ctx, s.expiryKey(), redis.Z{
			Score:  float64(rec.ExpiresAt),
			Member: userID,
		})
	} else {
		pipe.Set(ctx, s.recordKey(userID), raw, 0)
		pipe.ZRem(ctx, s.expiryKey(), userID)
	}
	_, err = pipe.Exec(ctx)
	return err
}

// Get 读取一条记录；不存在时返回 ErrNotFound。
func (s *RedisTicketStore) Get(ctx context.Context, userID string) ([]byte, time.Time, error) {
	raw, err := s.client.Get(ctx, s.recordKey(userID)).Bytes()
	if err == redis.Nil {
		return nil, time.Time{}, ErrNotFound
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	var rec redisTicketRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, time.Time{}, err
	}
	var exp time.Time
	if rec.ExpiresAt > 0 {
		exp = time.Unix(rec.ExpiresAt, 0)
	}
	return rec.Sealed, exp, nil
}

// Delete 删除记录与索引；不存在时幂等返回 nil。
func (s *RedisTicketStore) Delete(ctx context.Context, userID string) error {
	pipe := s.client.TxPipeline()
	pipe.Del(ctx, s.recordKey(userID))
	pipe.ZRem(ctx, s.expiryKey(), userID)
	_, err := pipe.Exec(ctx)
	return err
}

// ListExpiringSoon 用有序集合按 score 取「过期时刻早于 before」的候选，升序、至多 limit 条。
// 顺手清理「索引里有、记录已不存在」的悬垂成员，避免候选越积越多。
func (s *RedisTicketStore) ListExpiringSoon(ctx context.Context, before time.Time, limit int) ([]TicketRef, error) {
	if limit <= 0 {
		return nil, nil
	}
	// 多取一些用于过滤悬垂成员。
	ids, err := s.client.ZRangeByScore(ctx, s.expiryKey(), &redis.ZRangeBy{
		Min:   "-inf",
		Max:   fmt.Sprintf("%d", before.Unix()),
		Count: int64(limit * 2),
	}).Result()
	if err != nil {
		return nil, err
	}

	refs := make([]TicketRef, 0, limit)
	for _, id := range ids {
		if len(refs) >= limit {
			break
		}
		score, err := s.client.ZScore(ctx, s.expiryKey(), id).Result()
		if err != nil {
			continue
		}
		// 记录已被删除（悬垂索引）→ 清掉成员并跳过。
		exists, err := s.client.Exists(ctx, s.recordKey(id)).Result()
		if err != nil || exists == 0 {
			_ = s.client.ZRem(ctx, s.expiryKey(), id).Err()
			continue
		}
		refs = append(refs, TicketRef{
			UserID:    id,
			ExpiresAt: time.Unix(int64(score), 0),
		})
	}
	return refs, nil
}
