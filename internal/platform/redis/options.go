package redis

// RedisOptions 是连接 Redis 的通用参数（原属 platform 根包）。
//
// 为什么单独成叶子包：platform 内的 mail / cache / session 等子包都需要这套参数，
// 若放在 platform 根包，子包 import 根包、根包又 import 子包，立刻成环。
// 本包只含这一个结构体，零依赖，任何层都能安全引用。

// RedisOptions 是 Redis 的连接与命名空间配置。
type RedisOptions struct {
	Addr     string // 形如 127.0.0.1:6379
	Password string
	DB       int
	Prefix   string // 键名前缀，便于多环境隔离；空值用默认 "nongyu:"
}
