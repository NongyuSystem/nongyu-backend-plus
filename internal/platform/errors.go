package platform

// 哨兵错误：转发到 model 包。
//
// 为什么下沉：pg / redis / cache 等子包都要用 ErrNotFound，若它留在 platform 根，
// 子包 import 根、根又 import 子包（RedisOptions 别名）就会成环。把共享值类型与
// 哨兵错误放进零依赖的叶子包 model/，根再用别名转发，环就断了，而外部调用方
// `platform.ErrNotFound` / `platform.IsNotFound(...)` 完全不用改。

import "nongyu-backend-plus/internal/platform/model"

// ErrNotFound 表示 key 不存在（用哨兵错误而非返回 nil，区分「没值」与「读失败」）。
var ErrNotFound = model.ErrNotFound

// ErrAlreadyExists 表示唯一约束冲突（如学号已被占用、登录方式已绑定）。
var ErrAlreadyExists = model.ErrAlreadyExists

// IsNotFound 判断错误是否为「key 不存在」（含被 %w 包装的情形）。
func IsNotFound(err error) bool { return model.IsNotFound(err) }

// IsAlreadyExists 判断错误是否为唯一约束冲突（含被 %w 包装的情形）。
func IsAlreadyExists(err error) bool { return model.IsAlreadyExists(err) }
