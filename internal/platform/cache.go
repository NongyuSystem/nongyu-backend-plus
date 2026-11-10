package platform

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ---- from cache.go ----
// 通用缓存层：把「穿透 / 雪崩 / 击穿 / 降级回灌」四类缓存工程问题收在一个实现里，
// 业务域只调 GetOrLoad，不需要各自再写一遍。
//
// 定位说明：本层的值是「可再生」的缓存数据，允许在 Redis 不可用时降级到内存并丢一致性；
// 票据（TicketStore）是需要持久化的业务数据，仍走 RedisTicketStore，不与本层混用。

// RedisClient 是对 redis.Client 的薄封装，暴露给幂等锁等需要原生命令的调用方，
// 避免把 go-redis 类型泄漏到业务域。
type RedisClient struct {
	client *redis.Client
}

// NewRedisClient 构造薄封装客户端。
func NewRedisClient(opts RedisOptions) (*RedisClient, error) {
	if opts.Addr == "" {
		return nil, fmt.Errorf("platform: RedisClient 需要 Addr")
	}
	return &RedisClient{client: redis.NewClient(&redis.Options{
		Addr:         opts.Addr,
		Password:     opts.Password,
		DB:           opts.DB,
		MaxRetries:   1,
		DialTimeout:  500 * time.Millisecond,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
		PoolTimeout:  500 * time.Millisecond,
	})}, nil
}

// Ping 探测连通性。
func (c *RedisClient) Ping(ctx context.Context) error { return c.client.Ping(ctx).Err() }

// Close 释放连接池。
func (c *RedisClient) Close() error { return c.client.Close() }

// LockBackend 是幂等锁依赖的最小后端契约（只用到 SET NX + DEL + Lua 释放）。
type LockBackend interface {
	// SetNX 仅在键不存在时设置，并带上过期时间。
	SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
	// Del 删除键。
	Del(ctx context.Context, key string) error
	// Eval 执行 Lua 脚本（用于「只有持有者才能释放」的原子释放）。
	Eval(ctx context.Context, script string, keys []string, args ...any) (any, error)
}

// SetNX 实现 LockBackend。
func (c *RedisClient) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	return c.client.SetNX(ctx, key, value, ttl).Result()
}

// Del 实现 LockBackend。
func (c *RedisClient) Del(ctx context.Context, key string) error {
	return c.client.Del(ctx, key).Err()
}

// Eval 实现 LockBackend。
func (c *RedisClient) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return c.client.Eval(ctx, script, keys, args...).Result()
}

// CacheOptions 是缓存层的行为参数。
type CacheOptions struct {
	// DefaultTTL 是正常值的默认存活时间。
	DefaultTTL time.Duration
	// JitterRatio 是 TTL 抖动比例（0~1），用于防雪崩；0 表示不抖动。
	JitterRatio float64
	// NotFoundTTL 是「查无此物」空值缓存的存活时间，用于防穿透；必须短于 DefaultTTL。
	NotFoundTTL time.Duration
	// RebuildTimeout 是单次重建（单飞）的最长等待时间，用于防击穿。
	RebuildTimeout time.Duration
	// FallbackEnabled 为真时，Redis 不可用会降级到进程内存而不是直接报错。
	FallbackEnabled bool
	// FlushRatePerSec 是恢复后回灌内存数据到 Redis 的限速（每秒条数），防恢复瞬间压垮。
	FlushRatePerSec int
	// BreakerThreshold 是连续失败多少次后打开熔断（<=0 用默认 5）。
	BreakerThreshold int
	// BreakerCooldown 是熔断打开后的冷却时长，期满转半开探测（<=0 用默认 5s）。
	BreakerCooldown time.Duration
}

const (
	// emptyMarker 是「查无此物」的哨兵值：命中它即代表下游确认为空，
	// 与「键不存在」区分开，避免空值被当成未命中反复查下游。
	emptyMarker = "\x00__nongyu_cache_empty__"

	cacheKeyPrefix = "cache:"
)

// RedisCache 是带穿透/雪崩/击穿/降级防护的缓存实现。
//
// 四类防护对应关系：
//   - 穿透：下游返回「查无此物」时写入 emptyMarker，用 NotFoundTTL 缓存，短期内不再查下游。
//   - 雪崩：所有 TTL 都按 JitterRatio 抖动，避免同批写入同时过期。
//   - 击穿：同一 key 的并发重建只放行一个（进程内 singleflight），其余等结果。
//   - 降级：Redis 出错时读写落到进程内存；恢复后由限速回灌把内存里的待同步条目写回 Redis。
type RedisCache struct {
	client *redis.Client
	prefix string
	opts   CacheOptions

	mu       sync.Mutex
	fallback map[string]fallbackEntry
	pending  map[string]struct{}

	sfMu sync.Mutex
	sf   map[string]*sfCall

	rndMu sync.Mutex
	rnd   *rand.Rand

	breaker *breaker
}

type fallbackEntry struct {
	value     []byte
	expiresAt time.Time
}

type sfCall struct {
	done  chan struct{}
	val   []byte
	found bool
	err   error
}

// NewRedisCache 构造缓存层。
func NewRedisCache(opts RedisOptions, copts CacheOptions) (*RedisCache, error) {
	if opts.Addr == "" {
		return nil, fmt.Errorf("platform: RedisCache 需要 Addr")
	}
	if opts.Prefix == "" {
		opts.Prefix = "nongyu:"
	}
	if copts.DefaultTTL <= 0 {
		copts.DefaultTTL = time.Minute
	}
	if copts.NotFoundTTL <= 0 {
		copts.NotFoundTTL = 10 * time.Second
	}
	if copts.RebuildTimeout <= 0 {
		copts.RebuildTimeout = 2 * time.Second
	}
	if copts.FlushRatePerSec <= 0 {
		copts.FlushRatePerSec = 100
	}
	return &RedisCache{
		client: redis.NewClient(&redis.Options{
			Addr:     opts.Addr,
			Password: opts.Password,
			DB:       opts.DB,
			// 降级要靠「快速失败」：默认会重试 5 次、每次都要等连接超时，
			// 一个坏依赖会把请求拖到秒级。这里收紧为重试 1 次 + 短超时。
			MaxRetries:   1,
			DialTimeout:  500 * time.Millisecond,
			ReadTimeout:  500 * time.Millisecond,
			WriteTimeout: 500 * time.Millisecond,
			PoolTimeout:  500 * time.Millisecond,
		}),
		prefix:   opts.Prefix,
		opts:     copts,
		fallback: make(map[string]fallbackEntry),
		pending:  make(map[string]struct{}),
		sf:       make(map[string]*sfCall),
		rnd:      rand.New(rand.NewSource(time.Now().UnixNano())),
		breaker:  newBreaker(copts.BreakerThreshold, copts.BreakerCooldown),
	}, nil
}

// Ping 探测连通性。
func (c *RedisCache) Ping(ctx context.Context) error { return c.client.Ping(ctx).Err() }

// Close 释放连接池。
func (c *RedisCache) Close() error { return c.client.Close() }

func (c *RedisCache) fullKey(key string) string { return c.prefix + cacheKeyPrefix + key }

// jitteredTTL 给 TTL 加随机抖动（防雪崩）。
func (c *RedisCache) jitteredTTL(base time.Duration) time.Duration {
	if c.opts.JitterRatio <= 0 {
		return base
	}
	c.rndMu.Lock()
	// [-ratio, +ratio] 区间内的均匀抖动
	delta := (c.rnd.Float64()*2 - 1) * c.opts.JitterRatio
	c.rndMu.Unlock()
	ttl := time.Duration(float64(base) * (1 + delta))
	if ttl <= 0 {
		ttl = base
	}
	return ttl
}

// Get 读缓存：Redis 优先，不可用或已熔断时读内存兜底。
func (c *RedisCache) Get(ctx context.Context, key string) ([]byte, error) {
	if !c.breaker.allow() {
		// 熔断打开：直接走降级，不触碰 Redis（快速失败）。
		if c.opts.FallbackEnabled {
			return c.getFromFallback(key)
		}
		return nil, fmt.Errorf("platform: 缓存已熔断（key=%s）", key)
	}

	raw, err := c.client.Get(ctx, c.fullKey(key)).Bytes()
	if err == redis.Nil {
		c.breaker.onSuccess()
		return nil, nil
	}
	if err != nil {
		c.breaker.onFailure()
		if c.opts.FallbackEnabled {
			return c.getFromFallback(key)
		}
		return nil, err
	}
	c.breaker.onSuccess()
	if string(raw) == emptyMarker {
		return nil, nil
	}
	return raw, nil
}

// Set 写缓存：ttl<=0 表示用 DefaultTTL（仍会加抖动）。熔断或失败时按配置降级到内存。
func (c *RedisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = c.opts.DefaultTTL
	}
	ttl = c.jitteredTTL(ttl)

	if !c.breaker.allow() {
		if c.opts.FallbackEnabled {
			c.setFallback(key, value, ttl)
			return nil
		}
		return fmt.Errorf("platform: 缓存已熔断（key=%s）", key)
	}

	if err := c.client.Set(ctx, c.fullKey(key), value, ttl).Err(); err != nil {
		c.breaker.onFailure()
		if c.opts.FallbackEnabled {
			c.setFallback(key, value, ttl)
			return nil
		}
		return err
	}
	c.breaker.onSuccess()
	return nil
}

// TTL 返回剩余存活时间（测试与运维观测用）。
func (c *RedisCache) TTL(ctx context.Context, key string) (time.Duration, error) {
	return c.client.TTL(ctx, c.fullKey(key)).Result()
}

// Delete 删除缓存键。
func (c *RedisCache) Delete(ctx context.Context, key string) error {
	err := c.client.Del(ctx, c.fullKey(key)).Err()
	c.mu.Lock()
	delete(c.fallback, key)
	delete(c.pending, key)
	c.mu.Unlock()
	return err
}

// PendingBackfill 返回待回灌条目数（降级期间写入、尚未同步到 Redis 的数量）。
func (c *RedisCache) PendingBackfill() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

func (c *RedisCache) getFromFallback(key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.fallback[key]
	if !ok {
		return nil, nil
	}
	if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		delete(c.fallback, key)
		return nil, nil
	}
	if string(e.value) == emptyMarker {
		return nil, nil
	}
	return append([]byte(nil), e.value...), nil
}

func (c *RedisCache) setFallback(key string, value []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	c.fallback[key] = fallbackEntry{value: append([]byte(nil), value...), expiresAt: exp}
	c.pending[key] = struct{}{}
}

// Loader 是回源函数：found=false 表示下游明确「查无此物」（会触发空值缓存）。
type Loader func(ctx context.Context) (value []byte, found bool, err error)

// GetOrLoad 是缓存层的主入口，把三类防护一次做到：
//   - 命中正常值 → 直接返回；
//   - 命中空值标记 → 返回 nil（下游确认为空，不重复回源）；
//   - 未命中 → 单飞重建（同 key 并发只放行一个），回源结果按 found 决定写入正常值或空值。
func (c *RedisCache) GetOrLoad(ctx context.Context, key string, loader Loader) ([]byte, error) {
	if v, err := c.Get(ctx, key); err == nil && v != nil {
		return v, nil
	}
	// 空值标记也视为「已知结果」，不再回源（防穿透的关键）。
	if known, err := c.isKnownEmpty(ctx, key); err == nil && known {
		return nil, nil
	}

	call, leader := c.beginSingleflight(key)
	if !leader {
		select {
		case <-call.done:
			return call.val, call.err
		case <-time.After(c.opts.RebuildTimeout):
			return nil, fmt.Errorf("platform: 等待缓存重建超时（key=%s）", key)
		}
	}

	defer c.endSingleflight(key, call)

	val, found, err := loader(ctx)
	if err != nil {
		call.err = err
		return nil, err
	}
	if !found {
		// 穿透防护：把「查无此物」也缓存起来，TTL 取较短的 NotFoundTTL。
		_ = c.setRaw(ctx, key, []byte(emptyMarker), c.opts.NotFoundTTL)
		call.found = false
		return nil, nil
	}
	_ = c.Set(ctx, key, val, 0)
	call.val = val
	call.found = true
	return val, nil
}

// setRaw 写入原始值（不加抖动），用于空值标记这类需要确定性 TTL 的场景。
func (c *RedisCache) setRaw(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := c.client.Set(ctx, c.fullKey(key), value, ttl).Err(); err != nil {
		if c.opts.FallbackEnabled {
			c.setFallback(key, value, ttl)
			return nil
		}
		return err
	}
	return nil
}

func (c *RedisCache) isKnownEmpty(ctx context.Context, key string) (bool, error) {
	raw, err := c.client.Get(ctx, c.fullKey(key)).Bytes()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		if c.opts.FallbackEnabled {
			c.mu.Lock()
			defer c.mu.Unlock()
			e, ok := c.fallback[key]
			return ok && string(e.value) == emptyMarker, nil
		}
		return false, err
	}
	return string(raw) == emptyMarker, nil
}

func (c *RedisCache) beginSingleflight(key string) (*sfCall, bool) {
	c.sfMu.Lock()
	defer c.sfMu.Unlock()
	if call, ok := c.sf[key]; ok {
		return call, false
	}
	call := &sfCall{done: make(chan struct{})}
	c.sf[key] = call
	return call, true
}

func (c *RedisCache) endSingleflight(key string, call *sfCall) {
	c.sfMu.Lock()
	delete(c.sf, key)
	c.sfMu.Unlock()
	close(call.done)
}

// FlushOnce 把降级期间写入的内存条目按限速回灌到 Redis，返回本次成功条数。
// 由装配层在后台周期调用；限速保证恢复瞬间不会把 Redis 打满。
func (c *RedisCache) FlushOnce(ctx context.Context) (int, error) {
	c.mu.Lock()
	keys := make([]string, 0, len(c.pending))
	for k := range c.pending {
		keys = append(keys, k)
	}
	c.mu.Unlock()
	if len(keys) == 0 {
		return 0, nil
	}

	// 每轮最多回灌 FlushRatePerSec 条（配合调用周期即形成「每秒 N 条」的限速）。
	limit := c.opts.FlushRatePerSec
	if limit > len(keys) {
		limit = len(keys)
	}
	flushed := 0
	for _, k := range keys[:limit] {
		c.mu.Lock()
		e, ok := c.fallback[k]
		c.mu.Unlock()
		if !ok {
			continue
		}
		ttl := time.Until(e.expiresAt)
		if ttl <= 0 {
			c.mu.Lock()
			delete(c.fallback, k)
			delete(c.pending, k)
			c.mu.Unlock()
			continue
		}
		if err := c.client.Set(ctx, c.fullKey(k), e.value, ttl).Err(); err != nil {
			// 仍不可用：保留在 pending，下轮再试。
			return flushed, err
		}
		c.mu.Lock()
		delete(c.pending, k)
		c.mu.Unlock()
		flushed++
	}
	return flushed, nil
}

// ── 事务 ──

// CacheTx 是事务内的操作句柄：所有写入在提交时才真正生效。
type CacheTx interface {
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// txOp 是一条待提交的操作。
type txOp struct {
	del   bool
	key   string
	value []byte
	ttl   time.Duration
}

type cacheTx struct {
	c   *RedisCache
	ops []txOp
}

func (t *cacheTx) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	t.ops = append(t.ops, txOp{key: key, value: append([]byte(nil), value...), ttl: ttl})
	return nil
}

func (t *cacheTx) Delete(_ context.Context, key string) error {
	t.ops = append(t.ops, txOp{del: true, key: key})
	return nil
}

// Transaction 执行一组缓存操作：fn 返回错误则整组丢弃（回滚），否则一次性提交。
//
// 实现说明：fn 内不直接打 Redis，而是先累积操作、提交时用 MULTI/EXEC 一次写入。
// 这样「逻辑失败回滚」不需要真的撤销已写入的数据——没提交就等于没发生。
func (c *RedisCache) Transaction(ctx context.Context, fn func(tx CacheTx) error) error {
	tx := &cacheTx{c: c}
	if err := fn(tx); err != nil {
		// 业务失败：丢弃全部操作，Redis 侧零写入。
		return err
	}
	if len(tx.ops) == 0 {
		return nil
	}

	pipe := c.client.TxPipeline()
	for _, op := range tx.ops {
		if op.del {
			pipe.Del(ctx, c.fullKey(op.key))
			continue
		}
		ttl := op.ttl
		if ttl <= 0 {
			ttl = c.opts.DefaultTTL
		}
		pipe.Set(ctx, c.fullKey(op.key), op.value, c.jitteredTTL(ttl))
	}
	_, err := pipe.Exec(ctx)
	return err
}

// ErrCacheLocked 表示幂等锁已被占用。
var ErrCacheLocked = errors.New("platform: 幂等锁已被占用")

// ---- from redis.go ----
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
		// 快速失败：坏依赖不应把请求拖到秒级（否则降级/报错都太慢）。
		MaxRetries:   1,
		DialTimeout:  500 * time.Millisecond,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
		PoolTimeout:  500 * time.Millisecond,
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

// ---- from session_cache.go ----
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

// ---- from breaker.go ----
// 熔断器：Redis 持续不可用时，避免每个请求都去等连接超时。
//
// 三态：
//   - Closed：正常放行，成功即清零失败计数；
//   - Open：连续失败达到阈值后打开，调用方直接走降级，**不再触碰 Redis**（快速失败）；
//   - HalfOpen：冷却期满后放行一次探测；成功回 Closed，失败回 Open。
//
// 与降级（fallback）的分工：降级决定「Redis 挂了怎么办」，熔断决定「什么时候不再尝试」。
// 两者配合才能在 Redis 长时间不可用时既不丢功能、也不被拖慢。

// BreakerState 是熔断器状态。
type BreakerState int

const (
	// BreakerClosed 正常放行。
	BreakerClosed BreakerState = iota
	// BreakerOpen 已熔断，直接降级。
	BreakerOpen
	// BreakerHalfOpen 半开：放行一次探测。
	BreakerHalfOpen
)

func (s BreakerState) String() string {
	switch s {
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// breaker 是并发安全的熔断器实现。
type breaker struct {
	mu        sync.Mutex
	state     BreakerState
	failures  int
	threshold int
	cooldown  time.Duration
	openedAt  time.Time
}

func newBreaker(threshold int, cooldown time.Duration) *breaker {
	if threshold <= 0 {
		threshold = 5
	}
	if cooldown <= 0 {
		cooldown = 5 * time.Second
	}
	return &breaker{state: BreakerClosed, threshold: threshold, cooldown: cooldown}
}

// allow 判断本次是否放行到 Redis；放行时若处于半开，会记录探测。
func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case BreakerOpen:
		if time.Since(b.openedAt) >= b.cooldown {
			b.state = BreakerHalfOpen
			return true // 放行一次探测
		}
		return false
	case BreakerHalfOpen:
		// 已在探测中，其余调用继续降级。
		return false
	default:
		return true
	}
}

// onSuccess 记录一次成功：任何状态下都回到 Closed 并清零。
func (b *breaker) onSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = BreakerClosed
	b.failures = 0
}

// onFailure 记录一次失败：达到阈值则打开熔断。
func (b *breaker) onFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.state == BreakerHalfOpen || b.failures >= b.threshold {
		b.state = BreakerOpen
		b.openedAt = time.Now()
	}
}

func (b *breaker) currentState() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	// 冷却期满后对外报告为半开（下一次调用会真正转入）。
	if b.state == BreakerOpen && time.Since(b.openedAt) >= b.cooldown {
		return BreakerHalfOpen
	}
	return b.state
}

// BreakerState 返回缓存层熔断器的当前状态（运维观测与测试用）。
func (c *RedisCache) BreakerState() BreakerState {
	return c.breaker.currentState()
}

// ---- from lock.go ----
// 幂等锁：用于「同一个业务动作只允许执行一次」的场景（重复下单、重复提交、重复回调）。
//
// 语义与严格互斥锁的区别：
//   - 锁带 TTL，进程崩溃后会自动释放，不会永久死锁；
//   - 释放时必须校验持有者令牌（Lua 原子比较-删除），避免「A 的锁超时后被 B 拿到，
//     然后 A 误删了 B 的锁」这一经典错误。
type IdempotentLock struct {
	backend LockBackend
	prefix  string
}

// releaseScript 只在 value 与自己令牌一致时才删除，保证「只释放自己的锁」。
const releaseScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`

// NewIdempotentLock 构造幂等锁。
func NewIdempotentLock(backend LockBackend, prefix string) *IdempotentLock {
	if prefix == "" {
		prefix = "nongyu:lock:"
	}
	return &IdempotentLock{backend: backend, prefix: prefix}
}

// TryLock 尝试获取锁：成功返回 true；被占用返回 (false, nil)；后端故障返回错误。
func (l *IdempotentLock) TryLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	token, err := randomToken()
	if err != nil {
		return false, err
	}
	ok, err := l.backend.SetNX(ctx, l.prefix+key, []byte(token), ttl)
	if err != nil {
		return false, err
	}
	if ok {
		l.rememberToken(key, token)
	}
	return ok, nil
}

// Unlock 释放锁；只删除属于自己的令牌（原子比较-删除），已被他人持有时不误删。
func (l *IdempotentLock) Unlock(ctx context.Context, key string) error {
	token := l.takeToken(key)
	// 即使本地没有令牌（例如进程重启后），也执行一次比较-删除；ARGV 不匹配就什么都不做。
	_, err := l.backend.Eval(ctx, releaseScript, []string{l.prefix + key}, token)
	return err
}

// tokens 记录本进程持有的令牌，供释放时比较（进程无状态约束下它是本地簿记，不参与判定）。
var lockTokens = struct {
	m map[string]string
}{m: make(map[string]string)}

func (l *IdempotentLock) rememberToken(key, token string) {
	lockTokens.m[l.prefix+key] = token
}

func (l *IdempotentLock) takeToken(key string) string {
	full := l.prefix + key
	tok := lockTokens.m[full]
	delete(lockTokens.m, full)
	return tok
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
