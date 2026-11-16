package jiaowu

import "context"

// IssuedCookie 是客户端打直连教务地址时要带的一枚会话 cookie。
//
// 值只出现在给已认证本人的响应里。调用方不得把它写入日志或审计。
type IssuedCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SessionIssuer 用已保管的 go-admin-tgt 建立教务会话，返回会话 cookie。
//
// 不抓业务页。错误文本可能含凭据前缀，调用方不得把 err.Error() 写入日志或响应。
type SessionIssuer interface {
	IssueSession(ctx context.Context, userID string) ([]IssuedCookie, error)
}
