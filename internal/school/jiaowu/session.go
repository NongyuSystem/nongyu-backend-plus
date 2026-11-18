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

// GateWayTicketNotNeeded 说明：
//
// 曾经有过一个 GatewayTicketIssuer 接口（后端用 tgt 走门户 appid 换 wengine_vpn_ticket），
// 2026-11-17 实测后删除，理由是它拿到的是**游客票**：回投链停在 /wui/#/ 的 fragment 上，
// 会话在网关侧从未登记为已登录，拿这张票打任何编码地址都 302 回 /login。
//
// 现在校外链路的网关票**只能由客户端交上来**（客户端真实登录网关产生）。
// 后端不再尝试自己换票——那不是「换错了」，是这条路在纯 HTTP 下根本走不通。

// TGTRefresher 让 jiaowu 子域能把续期委托给持有整套凭据的实现，而不必自己拼请求。
type TGTRefresher interface {
	RefreshTGT(ctx context.Context, userID string) (string, error)
}

// JiaowuSTIssuer 用已保管的 go-admin-tgt 换出教务 service ticket。
//
// 经 WebVPN 编码域取需登录页时，需要先拿 st 回投编码后的 caslogin.asp
// 三跳，在网关服务端建立教务登录态（客户端零 cookie，见 FetchViaWebVPN）。
// tgt 由 POST /identity/login 保管，必然存在；取不到即真实故障。
//
// 错误文本可能带凭据前缀，调用方不得写入日志或 HTTP 响应。
type JiaowuSTIssuer interface {
	IssueJiaowuST(ctx context.Context, userID string) (string, error)
}

// FetchAccountSTIssuer 用「取数账号」的整套 auth 域 cookie 换出教务 st。
// 与 JiaowuSTIssuer 的区别：不按用户保管、不读写 TGTManager，凭据来自池。
// 错误文本可能带凭据前缀，调用方不得写入日志或 HTTP 响应。
type FetchAccountSTIssuer interface {
	IssueSTWithCredential(ctx context.Context, cookieHeader string) (string, error)
}
