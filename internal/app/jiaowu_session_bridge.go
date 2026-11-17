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
	return toIssued(raw), nil
}

// RefreshTGT 让续期回调走真正的 tgt 续期，而不是自己拼假请求再覆盖存储。
func (b jiaowuSessionBridge) RefreshTGT(ctx context.Context, userID string) (string, error) {
	return b.src.RefreshTGT(ctx, userID)
}

// ClearCredential 登出时清掉该用户的 tgt（ADR-0001 第 3 条「退出登录即清除」）。
//
// userID 是农屿用户 ID 的十进制串；TGTManager 的保管键就是它，与登录成功时
// tgtSaverBridge 写入时用的键完全一致。
func (b jiaowuSessionBridge) ClearCredential(ctx context.Context, userID string) error {
	return b.src.ClearTGT(ctx, userID)
}

func toIssued(raw []schoolauth.SessionCookie) []jiaowu.IssuedCookie {
	out := make([]jiaowu.IssuedCookie, 0, len(raw))
	for _, c := range raw {
		out = append(out, jiaowu.IssuedCookie{Name: c.Name, Value: c.Value})
	}
	return out
}
