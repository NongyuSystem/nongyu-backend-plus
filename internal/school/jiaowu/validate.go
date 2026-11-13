package jiaowu

// 请求体字段合法性校验。
//
// loginRequest 之前绑定后直接透传给 svc.Login，code / uuid 为空也会一路走到
// 学校侧换票。这里补最小必填校验：两个字段都必须非空、且去掉前后空白后仍有值，
// 并给长度上限，避免超长串进上游请求。风格与其它子域的 Validate* 一致。

import (
	"fmt"
	"strings"
)

const (
	maxLoginCodeLen = 32
	maxLoginUUIDLen = 128
)

// ValidateLoginRequest 校验 POST /school/login 的请求体。
func ValidateLoginRequest(in loginRequest) error {
	code := strings.TrimSpace(in.Code)
	if code == "" {
		return fmt.Errorf("缺少验证码 code")
	}
	if len(code) > maxLoginCodeLen {
		return fmt.Errorf("code 超过 %d 字符", maxLoginCodeLen)
	}
	uuid := strings.TrimSpace(in.UUID)
	if uuid == "" {
		return fmt.Errorf("缺少验证码标识 uuid")
	}
	if len(uuid) > maxLoginUUIDLen {
		return fmt.Errorf("uuid 超过 %d 字符", maxLoginUUIDLen)
	}
	return nil
}
