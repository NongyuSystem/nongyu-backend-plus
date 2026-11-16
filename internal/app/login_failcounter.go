package app

// 登录失败计数的跨实例存储适配（装配层职责）。
//
// 业务域（internal/identity/login）只声明一个窄接口 Counter：
//
//	IncrBy(ctx, key, delta, ttl) (int64, error)
//	Get(ctx, key) (int64, error)
//
// 这样 login 包不 import platform / redis，符合仓库「业务域只依赖 platform
// 定义的接口，不直接 import 具体实现」的架构约束；把 RedisCache 适配成
// Counter 是装配层的事。
//
// 为什么需要跨实例：失败计数若只存在进程内 map，多副本部署时攻击者可以
// 把尝试次数打散到不同实例，每个实例都从 0 开始数，等于限流失效。
// 用 Redis 的原子自增才能全局一致。

import (
	"context"
	"time"

	"nongyu-backend-plus/internal/identity/login"
	"nongyu-backend-plus/internal/platform"
)

// loginFailCounter 把 *RedisCache 适配成 login.Counter；cache 为 nil 时返回
// 真正的 nil 接口（让 login 侧走进程内兜底）。
//
// 注意 typed-nil 陷阱：必须显式返回 nil 接口而不是 (*redisLoginCounter)(nil)，
// 否则 login 侧 `if t.counter != nil` 判断会失效，走到一个空指针上。
func loginFailCounter(cache *platform.RedisCache) login.Counter {
	if cache == nil {
		return nil
	}
	return redisLoginCounter{cache: cache}
}

// redisLoginCounter 是 login.Counter 的 Redis 实现。
type redisLoginCounter struct {
	cache *platform.RedisCache
}

// IncrBy 原子自增登录失败计数；首次创建时设 TTL（固定滑窗，非"一直尝试就不过期"）。
func (r redisLoginCounter) IncrBy(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error) {
	return r.cache.IncrBy(ctx, key, delta, ttl)
}

// Get 读当前计数（键不存在 = 0）。
func (r redisLoginCounter) Get(ctx context.Context, key string) (int64, error) {
	return r.cache.IncrGet(ctx, key)
}

// 编译期断言：适配器必须满足业务域声明的窄接口。
var _ login.Counter = redisLoginCounter{}
