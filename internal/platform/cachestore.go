package platform

import (
	"context"
	"time"
)

// CacheStore 是平台层对外暴露的缓存存储契约（issue #72）。
//
// 它把「缓存层应提供哪四个操作」这份契约放在平台层，业务域（教务、二课等）
// 只依赖本接口而不绑定具体类型：装配层注入 *RedisCache 等真实现，
// 测试与上层域可注入任意内存实现，彼此互不 import。
//
// 方法集与教务域原先内部声明的 CacheStore 完全一致，本次只做接口搬家，
// 不新增、不删减、不改签名。
type CacheStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	TryAcquire(ctx context.Context, key string, ttl time.Duration) (bool, error)
}
