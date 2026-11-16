package app

import (
	"context"

	"nongyu-backend-plus/internal/school/jiaowu"
	"nongyu-backend-plus/internal/school/schoolauth"
)

// jiaowuSessionBridge 把 schoolauth 的建会话能力接成教务缓存 miss 要的接口。
// 装配层做翻译，jiaowu 不 import schoolauth。
type jiaowuSessionBridge struct {
	src *schoolauth.JiaowuSource
}

func (b jiaowuSessionBridge) IssueSession(ctx context.Context, userID string) ([]jiaowu.IssuedCookie, error) {
	raw, err := b.src.IssueSessionCookies(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]jiaowu.IssuedCookie, 0, len(raw))
	for _, c := range raw {
		out = append(out, jiaowu.IssuedCookie{Name: c.Name, Value: c.Value})
	}
	return out, nil
}
