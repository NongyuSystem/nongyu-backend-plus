// Package platform 是「外部能力适配层」的接缝。
//
// 设计约束（长期不可破）：
//  1. 业务域只依赖本包定义的接口，绝不直接 import 数据库驱动、S3 SDK 等具体实现。
//  2. 具体实现放在本包的子目录或独立实现包里，由 app 装配层在启动时注入。
//  3. 骨架期无数据库：先用内存/Mock 实现注入；数据库层就绪后只替换实现，业务域零改动。
//
// 这正是「没有数据库就先用 mock，后期数据库层做好直接用数据库」这条顺序能成立的代码保证。
package platform

import "context"

// Store 是业务域可见的最小存储契约。
//
// 骨架期故意只定义一个方法：它表达的是「按 key 取一份配置/资源描述」这一抽象能力，
// 而不是任何具体数据库的形状。新增能力时在此扩展方法，不要另起一套平行接口。
type Store interface {
	// Get 按 key 读取一份值；key 不存在时返回 ErrNotFound。
	Get(ctx context.Context, key string) ([]byte, error)
}

// ErrNotFound 表示 key 不存在。
// 用哨兵错误而非返回 nil，让调用方能区分「没值」与「读失败」。
var ErrNotFound = &notFoundError{}

type notFoundError struct{}

func (e *notFoundError) Error() string { return "platform: key not found" }

// IsNotFound 判断错误是否为「key 不存在」。
func IsNotFound(err error) bool {
	_, ok := err.(*notFoundError)
	return ok
}

// ErrAlreadyExists 表示唯一约束冲突（如学号已被占用、登录方式已绑定）。
var ErrAlreadyExists = &alreadyExistsError{}

type alreadyExistsError struct{}

func (e *alreadyExistsError) Error() string { return "platform: already exists" }

// IsAlreadyExists 判断错误是否为唯一约束冲突。
func IsAlreadyExists(err error) bool {
	_, ok := err.(*alreadyExistsError)
	return ok
}
