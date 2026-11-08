package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

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
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
