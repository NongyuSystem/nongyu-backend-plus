package platform

// 哨兵错误：让调用方区分「没值」与「读失败」、「唯一约束冲突」与「其它写失败」，
// 而不用比较错误字符串。
//
// 本文件由原 store.go 的「哨兵错误」段落独立而来——那段代码与 PGIdentityStore 无关，
// 只是当初被脚本并进了同一个文件。

import "errors"

// ErrNotFound 表示 key 不存在。
// 用哨兵错误而非返回 nil，让调用方能区分「没值」与「读失败」。
var ErrNotFound = &notFoundError{}

type notFoundError struct{}

func (e *notFoundError) Error() string { return "platform: key not found" }

// IsNotFound 判断错误是否为「key 不存在」（含被 %w 包装的情形）。
func IsNotFound(err error) bool {
	var target *notFoundError
	return errors.As(err, &target)
}

// ErrAlreadyExists 表示唯一约束冲突（如学号已被占用、登录方式已绑定）。
var ErrAlreadyExists = &alreadyExistsError{}

type alreadyExistsError struct{}

func (e *alreadyExistsError) Error() string { return "platform: already exists" }

// IsAlreadyExists 判断错误是否为唯一约束冲突（含被 %w 包装的情形）。
func IsAlreadyExists(err error) bool {
	var target *alreadyExistsError
	return errors.As(err, &target)
}
