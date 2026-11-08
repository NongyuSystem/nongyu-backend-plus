package platform

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

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
