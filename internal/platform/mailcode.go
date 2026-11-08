package platform

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"github.com/redis/go-redis/v9"
)

// 邮箱验证码存储（Redis）。
//
// 决策依据：docs/tech/架构落点-身份仓.md 第六节 —— 验证码存 Redis，
// **TTL 既是有效期也是发送冷却锁**；只存哈希，不存明文；一次性消费。
//
// 为什么适合 Redis：验证码天然短命，丢了让用户重新获取即可，不值得为它建表。

// maxMailCodeAttempts 是单条验证码允许的错误尝试次数，超限即作废。
const maxMailCodeAttempts = 5

// RedisMailCodeStore 是验证码的 Redis 实现。
type RedisMailCodeStore struct {
	client *redis.Client
	prefix string
	// secret 用于对验证码做 HMAC 后再存；避免库里存明文码。
	secret []byte
}

// NewRedisMailCodeStore 构造验证码存储。
func NewRedisMailCodeStore(opts RedisOptions) (*RedisMailCodeStore, error) {
	if opts.Addr == "" {
		return nil, fmt.Errorf("platform: RedisMailCodeStore 需要 Addr")
	}
	if opts.Prefix == "" {
		opts.Prefix = "nongyu:"
	}
	// 哈希密钥独立派生：即使 Redis 泄露，也无法还原出 6 位数字码。
	sum := sha256.Sum256([]byte("mailcode:" + opts.Password + ":" + opts.Prefix))
	return &RedisMailCodeStore{
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
		prefix: opts.Prefix + "mailcode:",
		secret: sum[:],
	}, nil
}

// Ping 探测连通性。
func (s *RedisMailCodeStore) Ping(ctx context.Context) error { return s.client.Ping(ctx).Err() }

// Close 释放连接池。
func (s *RedisMailCodeStore) Close() error { return s.client.Close() }

func (s *RedisMailCodeStore) codeKey(email string) string     { return s.prefix + "code:" + email }
func (s *RedisMailCodeStore) attemptsKey(email string) string { return s.prefix + "try:" + email }

// hash 对验证码做 HMAC，避免明文落 Redis。
func (s *RedisMailCodeStore) hash(code string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(code))
	return hex.EncodeToString(m.Sum(nil))
}

// GenerateCode 生成指定位数的数字验证码（默认 6 位），使用 crypto/rand。
func GenerateMailCode(digits int) (string, error) {
	if digits <= 0 {
		digits = 6
	}
	max := big.NewInt(1)
	for i := 0; i < digits; i++ {
		max.Mul(max, big.NewInt(10))
	}
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", digits, n.Int64()), nil
}

// Issue 写入验证码。TTL 既是有效期也是冷却窗口：
// 调用方应先查 Cooldown，若仍处于冷却则**拒绝发送**（不覆盖旧码，避免绕过冷却）。
//
// 返回 ErrCooldown 表示 TTL 内已发过，调用方应拒绝本次请求。
func (s *RedisMailCodeStore) Issue(ctx context.Context, email, code string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	// SET NX：只有在没有未过期码时才写入 → 天然实现「TTL 内不可重复发送」。
	ok, err := s.client.SetNX(ctx, s.codeKey(email), s.hash(code), ttl).Result()
	if err != nil {
		return err
	}
	if !ok {
		return ErrCooldown
	}
	// 尝试计数与新码同寿命。
	_ = s.client.Del(ctx, s.attemptsKey(email)).Err()
	return nil
}

// Cooldown 返回该邮箱还剩多久可以再次申请；0 表示可以立即发。
func (s *RedisMailCodeStore) Cooldown(ctx context.Context, email string) (time.Duration, error) {
	ttl, err := s.client.TTL(ctx, s.codeKey(email)).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if ttl <= 0 {
		return 0, nil
	}
	return ttl, nil
}

// Verify 校验验证码。成功即删除（一次性）；失败累计次数，超限作废。
func (s *RedisMailCodeStore) Verify(ctx context.Context, email, code string) (bool, error) {
	stored, err := s.client.Get(ctx, s.codeKey(email)).Result()
	if err == redis.Nil {
		return false, nil // 没有码 → 不通过
	}
	if err != nil {
		return false, err
	}

	// 常量时间比较，避免计时侧信道。
	if subtle.ConstantTimeCompare([]byte(stored), []byte(s.hash(code))) != 1 {
		// 记一次失败；达到上限则把码删掉（作废，需重新获取）。
		n, err := s.client.Incr(ctx, s.attemptsKey(email)).Result()
		if err != nil {
			return false, err
		}
		if n >= maxMailCodeAttempts {
			_ = s.client.Del(ctx, s.codeKey(email)).Err()
			_ = s.client.Del(ctx, s.attemptsKey(email)).Err()
		}
		return false, nil
	}

	// 成功：一次性消费。
	_ = s.client.Del(ctx, s.codeKey(email)).Err()
	_ = s.client.Del(ctx, s.attemptsKey(email)).Err()
	return true, nil
}

// Clear 删除该邮箱的验证码与计数（测试与「重新获取」用）。
func (s *RedisMailCodeStore) Clear(ctx context.Context, email string) error {
	pipe := s.client.TxPipeline()
	pipe.Del(ctx, s.codeKey(email))
	pipe.Del(ctx, s.attemptsKey(email))
	_, err := pipe.Exec(ctx)
	return err
}

// ErrCooldown 表示验证码仍在有效期内，不允许重复发送。
var ErrCooldown = &cooldownError{}

type cooldownError struct{}

func (e *cooldownError) Error() string { return "platform: 验证码仍有效，请稍后再发" }

// IsCooldown 判断错误是否为「仍在冷却中」。
func IsCooldown(err error) bool {
	_, ok := err.(*cooldownError)
	return ok
}
