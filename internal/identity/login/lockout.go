package login

// 登录失败次数限制（按账号维度）。
//
// 为什么需要它（业务逻辑漏洞修复）：
//
// 登录方式④「学号 + 身份证后四位 + 姓名拼音首字母」的凭据是**半公开**的——
// 学号 9 位有规律可枚举；身份证后四位只有 10^4 种可能，且同地区同龄人前 14 位
// 常相同；拼音首字母可由姓名推出（姓名在同班/群里常见）。
//
// 早期实现只用 constantTimeEqual 比较两个因子——那防的是**时序侧信道**，
// 不防爆破。外层唯一的防护是「按 IP 的全局限流」（RATE_LIMIT_PER_MINUTE），
// 它不是按账号计的：攻击者换 IP / 分布式即可对同一学号无限重试。
//
// 对照：邮箱验证码登录有 maxMailCodeAttempts=5 的尝试上限，而身份证登录
// 一个都没有——这不是设计取舍，是明确的遗漏。
//
// 本文件补上**按账号**的失败计数与临时锁定，覆盖两种"半公开凭据"登录：
//   - idcard  ：学号 + 身份证后四位 + 拼音首字母
//   - password：学号 + 农屿账密（同样可被穷举，只是空间更大）
//
// 连续失败达阈值 → 锁定 lockoutWindow；锁定期内一律返回 CodeTooManyAttempts，
// 不泄露"还剩几次"或"是否已注册"。登录成功清除计数。
//
// 存储实现（两级）：
//  1. 注入 Counter 时用外置原子计数（生产走 platform.RedisCache.IncrBy）：
//     多实例共享，攻击者无法靠打散到不同实例来分摊尝试次数；
//  2. 未注入（本地开发 / 无 Redis）时退回进程内 map。
//     此时多实例不共享是**已知残余面**，但已把成本从"零"抬到"需协调多实例"。
//
// 解耦：本文件只依赖 NarrowCounter 接口，不 import platform / redis，
// 由装配层把具体实现适配进来（与仓库「业务域只依赖自己声明的窄接口」一致）。

import (
	"context"
	"strings"
	"sync"
	"time"
)

const (
	// maxLoginFailures 是同一账号连续失败多少次后触发临时锁定。
	// 取 5 与邮箱验证码的 maxMailCodeAttempts 对齐，全仓口径一致。
	maxLoginFailures = 5
	// lockoutWindow 是锁定时长，也是失败计数的滑动窗口。
	lockoutWindow = 15 * time.Minute
	// failEntryTTL 是进程内失败记录的最长存活时间，防止 map 无限增长。
	failEntryTTL = 1 * time.Hour
)

// 失败计数的作用域（同一账号的不同登录方式分开计）。
const (
	ScopeIDCard   = "idcard"
	ScopePassword = "password"
)

// Counter 是「原子自增 + 读取一个短命计数器」的窄接口。
//
// 由装配层注入 platform.RedisCache 的适配实现。
// IncrBy 返回自增后的值；Get 返回当前值（不存在时返回 0）。
// err != nil 表示存储不可用，调用方必须自行决定退路。
type Counter interface {
	IncrBy(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error)
	Get(ctx context.Context, key string) (int64, error)
}

// failureTracker 是按 (scope, 账号标识) 计的失败计数器。
type failureTracker struct {
	// counter 非 nil 时优先用它（多实例共享）。
	counter Counter
	// 进程内兜底（counter 为 nil，或 counter 报错时用）。
	mu      sync.Mutex
	entries map[string]*failEntry
	now     func() time.Time
}

type failEntry struct {
	count     int
	lockedTil time.Time
	lastSeen  time.Time
}

func newFailureTracker(counter Counter, now func() time.Time) *failureTracker {
	if now == nil {
		now = time.Now
	}
	return &failureTracker{counter: counter, entries: make(map[string]*failEntry), now: now}
}

// lockKey 组装计数器键：<scope>:<账号标识>。
func lockKey(scope, id string) string {
	return scope + ":" + strings.TrimSpace(id)
}

// Locked 判断该 key 当前是否处于锁定中。
//
// 有 Counter 时读**外置**值（跨实例一致）；计数器 TTL 到期即自动解锁。
// 读失败时退回本地判断——宁可少锁一次，也不要因为存储抖动把所有人挡在门外。
func (t *failureTracker) Locked(ctx context.Context, scope, id string) bool {
	if t == nil {
		return false
	}
	key := lockKey(scope, id)
	if t.counter != nil {
		if n, err := t.counter.Get(ctx, key); err == nil {
			return n >= maxLoginFailures
		}
	}
	return t.lockedLocal(key)
}

func (t *failureTracker) lockedLocal(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[key]
	if !ok {
		return false
	}
	return t.now().Before(e.lockedTil)
}

// Fail 记一次失败；返回是否已达锁定阈值。
//
// 有 Counter 时以**外置自增结果**为准（跨实例一致）；Counter 报错则退回本地计数。
func (t *failureTracker) Fail(ctx context.Context, scope, id string) bool {
	if t == nil {
		return false
	}
	key := lockKey(scope, id)

	if t.counter != nil {
		if n, err := t.counter.IncrBy(ctx, key, 1, lockoutWindow); err == nil {
			locked := n >= maxLoginFailures
			// 同步到本地，供 Counter 故障时的兜底判断与观测。
			t.mu.Lock()
			e, ok := t.entries[key]
			if !ok {
				e = &failEntry{}
				t.entries[key] = e
			}
			e.count = int(n)
			e.lastSeen = t.now()
			if locked {
				e.lockedTil = t.now().Add(lockoutWindow)
			}
			t.mu.Unlock()
			return locked
		}
		// Counter 故障：退回本地计数，仍然尽力阻挡（fail-soft，不是 fail-open）。
	}
	return t.failLocal(key)
}

func (t *failureTracker) failLocal(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.sweepLocked(now)

	e, ok := t.entries[key]
	if !ok {
		e = &failEntry{}
		t.entries[key] = e
	}
	// 距上次失败已超过锁定时长：视为新一轮，计数归零重来。
	if !e.lastSeen.IsZero() && now.Sub(e.lastSeen) > lockoutWindow {
		e.count = 0
	}
	e.count++
	e.lastSeen = now
	if e.count >= maxLoginFailures {
		e.lockedTil = now.Add(lockoutWindow)
		return true
	}
	return false
}

// Reset 在登录成功后清除该 key 的失败记录。
//
// 外置计数器无法直接删除（Counter 只有自增），因此这里清本地记录；
// 外置键会在 lockoutWindow 后自然过期。下次失败会从 1 开始累计。
func (t *failureTracker) Reset(scope, id string) {
	if t == nil {
		return
	}
	key := lockKey(scope, id)
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

// sweepLocked 清理过旧条目，避免 map 无限增长。调用方必须已持锁。
func (t *failureTracker) sweepLocked(now time.Time) {
	if len(t.entries) < 1024 {
		return // 小表不必每次扫
	}
	for k, e := range t.entries {
		if now.Sub(e.lastSeen) > failEntryTTL && now.After(e.lockedTil) {
			delete(t.entries, k)
		}
	}
}
