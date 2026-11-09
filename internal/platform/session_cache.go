package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// 会话的 Redis 副本。
//
// 定位（见 docs/tech/架构落点-身份仓.md 第六节）：
//   - **Postgres 是权威**：可审计、可追溯、重启可重建；
//   - **Redis 是热路径副本**：鉴权每次请求都要读会话，走 Redis 比每次打 PG 快得多；
//   - 吊销时两边都写：先写 PG（权威），再 MarkRevoked 到 Redis；
//   - Redis 丢数据只会让缓存 miss，回落到 PG 读取即可重建，**不会造成越权**。
//
// 关键安全点：吊销后**不能**直接把 key 删掉，因为那样和「缓存未命中」无法区分，
// 一旦回落到 PG 读到一个「尚未写成吊销」的旧值就可能放行。
// 因此吊销是写入带 RevokedAt 的记录，而不是删除。

// RedisSessionCache 是会话的 Redis 副本实现。
type RedisSessionCache struct {
	client *redis.Client
	prefix string
}

// NewRedisSessionCache 构造会话缓存。
func NewRedisSessionCache(opts RedisOptions) (*RedisSessionCache, error) {
	if opts.Addr == "" {
		return nil, fmt.Errorf("platform: RedisSessionCache 需要 Addr")
	}
	if opts.Prefix == "" {
		opts.Prefix = "nongyu:"
	}
	return &RedisSessionCache{
		client: redis.NewClient(&redis.Options{
			Addr:         opts.Addr,
			Password:     opts.Password,
			DB:           opts.DB,
			MaxRetries:   1,
			DialTimeout:  500 * time.Millisecond,
			ReadTimeout:  500 * time.Millisecond,
			WriteTimeout: 500 * time.Millisecond,
			PoolTimeout:  500 * time.Millisecond,
		}),
		prefix: opts.Prefix + "session:",
	}, nil
}

// Ping 探测连通性。
func (c *RedisSessionCache) Ping(ctx context.Context) error { return c.client.Ping(ctx).Err() }

// Close 释放连接池。
func (c *RedisSessionCache) Close() error { return c.client.Close() }

func (c *RedisSessionCache) key(id string) string { return c.prefix + id }

// sessionJSON 是缓存里的存储形状（用整数时间戳，便于跨语言与紧凑存储）。
type sessionJSON struct {
	ID        string `json:"id"`
	UserID    int64  `json:"uid"`
	Scope     string `json:"scope"`
	DeviceID  string `json:"dev"`
	ExpiresAt int64  `json:"exp"`
	RevokedAt int64  `json:"rev,omitempty"`
	CreatedAt int64  `json:"created"`
}

// PutSession 写入/刷新会话缓存。TTL 与会话过期一致（过期即自然消失）。
func (c *RedisSessionCache) PutSession(ctx context.Context, s Session) error {
	rec := sessionJSON{
		ID:        s.ID,
		UserID:    s.UserID,
		Scope:     string(s.Scope),
		DeviceID:  s.DeviceID,
		ExpiresAt: s.ExpiresAt.Unix(),
		CreatedAt: s.CreatedAt.Unix(),
	}
	if s.RevokedAt != nil {
		rec.RevokedAt = s.RevokedAt.Unix()
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	ttl := time.Until(s.ExpiresAt)
	if ttl <= 0 {
		ttl = time.Minute // 已过期会话短暂留存，便于判定
	}
	return c.client.Set(ctx, c.key(s.ID), raw, ttl).Err()
}

// CachedSession 读会话缓存。未命中返回 (nil, nil)——调用方应回落到 PG。
func (c *RedisSessionCache) CachedSession(ctx context.Context, id string) (*Session, error) {
	raw, err := c.client.Get(ctx, c.key(id)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec sessionJSON
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	s := &Session{
		ID:        rec.ID,
		UserID:    rec.UserID,
		Scope:     SessionScope(rec.Scope),
		DeviceID:  rec.DeviceID,
		ExpiresAt: time.Unix(rec.ExpiresAt, 0),
		CreatedAt: time.Unix(rec.CreatedAt, 0),
	}
	if rec.RevokedAt > 0 {
		t := time.Unix(rec.RevokedAt, 0)
		s.RevokedAt = &t
	}
	return s, nil
}

// MarkRevoked 在缓存里打吊销标记（**不删除 key**，避免与「未命中」混淆）。
func (c *RedisSessionCache) MarkRevoked(ctx context.Context, id string, at time.Time) error {
	raw, err := c.client.Get(ctx, c.key(id)).Bytes()
	if err == redis.Nil {
		// 缓存里本来就没有：写一条最小的吊销记录，保证后续读到的也是「已吊销」。
		ttl := 10 * time.Minute
		rec := sessionJSON{ID: id, RevokedAt: at.Unix(), ExpiresAt: at.Add(ttl).Unix()}
		b, _ := json.Marshal(rec)
		return c.client.Set(ctx, c.key(id), b, ttl).Err()
	}
	if err != nil {
		return err
	}
	var rec sessionJSON
	if err := json.Unmarshal(raw, &rec); err != nil {
		return err
	}
	rec.RevokedAt = at.Unix()
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	// 保留原 TTL（不延长会话寿命）。
	ttl, terr := c.client.TTL(ctx, c.key(id)).Result()
	if terr != nil || ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return c.client.Set(ctx, c.key(id), b, ttl).Err()
}

// Delete 彻底删除会话缓存条目（仅用于测试或运维清理；
// 业务上的「吊销」必须用 MarkRevoked，不能删 key）。
func (c *RedisSessionCache) Delete(ctx context.Context, id string) error {
	return c.client.Del(ctx, c.key(id)).Err()
}
