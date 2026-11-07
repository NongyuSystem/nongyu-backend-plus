package jiaowu

import (
	"errors"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
)

// 出站闸口的错误转译（ADR-0001 / 架构落点草图 §4）。
//
// 闸口的强制逻辑统一落在 platform 的 HTTPDoer 实现层（全系统唯一强制点），
// 由装配层注入同一个守卫实例给所有业务子域。本文件只做一件事：把 platform 的
// 哨兵错误映射为跨子域统一的 school.Error 错误码，业务域不重复判断任何策略。

// mapOutboundError 把 platform 实现层产出的哨兵错误翻译成 school 统一错误码。
// 非哨兵错误原样返回（交由上层按内部错误处理）。
func mapOutboundError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, platform.ErrTargetDenied):
		return &school.Error{Code: school.CodeTargetDenied}
	case errors.Is(err, platform.ErrBlockedRedirect):
		return &school.Error{Code: school.CodeBlockedRedirect}
	case errors.Is(err, platform.ErrSessionExpired):
		return &school.Error{Code: school.CodeSessionExpired}
	default:
		return err
	}
}
