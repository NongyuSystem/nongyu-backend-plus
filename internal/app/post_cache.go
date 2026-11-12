package app

import (
	"context"
	"time"

	"nongyu-backend-plus/internal/content/post"
	"nongyu-backend-plus/internal/platform"
)

// postCache 把 platform.RedisCache 适配成 post.Cache。
//
// 为什么不直接让帖子域用 RedisCache：帖子域只需要 Get/Set/Delete 三个动作，
// 直接依赖会把「熔断、降级回灌、空值缓存、抖动」这些语义全泄进业务域
// （README 架构约束第 4 条：业务域只声明自己要什么）。
//
// 两处签名差异在此吸收：
//   - RedisCache.Get 返回 error（未命中也是 error），post.Cache 要 (data, ok)；
//   - RedisCache.Delete 一次只删一个 key，post.Cache 支持批量。
type postCache struct{ c *platform.RedisCache }

// Get 实现 post.Cache：命中返回 (data, true)，未命中或任何故障返回 (nil, false)。
//
// **故障也当作未命中**：缓存挂了不该让帖子列表 500，退化直查即可
// （RedisCache 内部已有熔断与降级，这里再兜一层是幂等的）。
func (p postCache) Get(ctx context.Context, key string) ([]byte, bool) {
	v, err := p.c.Get(ctx, key)
	if err != nil {
		return nil, false
	}
	return v, true
}

// Set 实现 post.Cache。
func (p postCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	return p.c.Set(ctx, key, val, ttl)
}

// Delete 实现 post.Cache（批量逐个删）。
func (p postCache) Delete(ctx context.Context, keys ...string) error {
	var firstErr error
	for _, k := range keys {
		if err := p.c.Delete(ctx, k); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// 编译期确认。
var _ post.Cache = postCache{}
