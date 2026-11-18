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

// IssueJiaowuST 用保管 tgt 换教务 st，供 /fetch 经 WebVPN 建教务会话。
//
// 与 IssueSession 的区别：IssueSession 走直连教务域建会话（要 ASPSESSIONID），
// 这条只换 st，不建会话——会话由 jiaowu 包按编码地址回投 caslogin 三跳完成。
func (b jiaowuSessionBridge) IssueJiaowuST(ctx context.Context, userID string) (string, error) {
	return b.src.IssueJiaowuST(ctx, userID)
}

// RefreshTGT 让续期回调走真正的 tgt 续期，而不是自己拼假请求再覆盖存储。
func (b jiaowuSessionBridge) RefreshTGT(ctx context.Context, userID string) (string, error) {
	return b.src.RefreshTGT(ctx, userID)
}

// fetchAccountSTBridge 让公共类取数账号用「整套 auth cookie」换教务 st。
// 不经过 TGTManager 的用户级保管——取数账号不对应任何 app 用户。
type fetchAccountSTBridge struct {
	src *schoolauth.JiaowuSource
}

func (b fetchAccountSTBridge) IssueSTWithCredential(ctx context.Context, cookieHeader string) (string, error) {
	return b.src.IssueSTWithCredential(ctx, cookieHeader)
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
