package user

// 请求体字段合法性校验。
//
// patchReq 的两个字段都是 *string（nil 表示「不修改」），之前 handler 只判 nil
// 不查内容，超长 QQ、非 http(s) 的 avatarUrl 会原样落库。这里收成纯函数，
// 风格与 internal/identity/flow/validate.go 一致：独立、可单测、不依赖 gin。

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	maxQQLen        = 32
	maxAvatarURLLen = 2048
)

// ValidateExtras 校验 PATCH /identity/me/profile 的可写扩展字段。
//
// 两个指针都为 nil 表示「没有要改的字段」，视为合法（由 handler 决定后续行为）。
// 非 nil 时：QQ 去空白后须为数字且不超过长度上限；AvatarURL 须是绝对
// http/https URL，避免把 javascript: / data: 之类的形态存进画像。
func ValidateExtras(qq, avatarURL *string) error {
	if qq != nil {
		v := strings.TrimSpace(*qq)
		if v == "" {
			return fmt.Errorf("qq 不能为空白")
		}
		if len(v) > maxQQLen {
			return fmt.Errorf("qq 超过 %d 字符", maxQQLen)
		}
		for _, r := range v {
			if r < '0' || r > '9' {
				return fmt.Errorf("qq 只能是数字")
			}
		}
	}
	if avatarURL != nil {
		v := strings.TrimSpace(*avatarURL)
		if v == "" {
			return fmt.Errorf("avatarUrl 不能为空白")
		}
		if len(v) > maxAvatarURLLen {
			return fmt.Errorf("avatarUrl 超过 %d 字符", maxAvatarURLLen)
		}
		u, err := url.Parse(v)
		if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("avatarUrl 必须是绝对 http/https URL")
		}
	}
	return nil
}
