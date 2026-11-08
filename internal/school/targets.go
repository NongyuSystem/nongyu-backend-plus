package school

import "net/url"

// CtxUserID 是认证中间件与业务域共享的身份上下文键。
// 放在父包作契约，避免业务子域反向 import 框架适配层（server）。
// 认证中间件写入、业务 handler 只读；请求体/query 里的 userID 一律不生效。
const CtxUserID = "authenticatedUserID"

// CtxRequestID 是一次请求链路的追踪号（即「审计流水号」）的上下文键。
// 由框架层中间件写入，业务域只读；审计记录靠它把一次请求的多个动作串起来。
const CtxRequestID = "requestID"

// ── 逻辑目标名 ────────────────────────────────────────────────────────────
//
// 调用方只能传 TargetName，不能传 URL —— 这是 ADR-0001 第 1 条的结构性保证：
// 白名单 targetHosts 只认这三个逻辑名，任何 URL 形态的字符串都不在白名单里，
// 会被 Resolve 以 CodeTargetDenied 拒绝。

// TargetName 逻辑目标名。
type TargetName string

const (
	TargetJiaowu TargetName = "jiaowu"
	TargetAuth   TargetName = "auth"
	TargetWebvpn TargetName = "webvpn"
)

// targetHosts 是唯一的内置白名单：三个 host，对应三个逻辑目标名。
// 契约清单 §3 的 REWRITE_HOSTS 只有两个（那是「改写范围」）；本票按 issue #12
// 把「网关域自身」也列为一个可解析的逻辑目标，便于统一表达两种地址形态。
var targetHosts = map[TargetName]string{
	TargetJiaowu: "jiaowu.sicau.edu.cn",
	TargetAuth:   "auth.sicau.edu.cn",
	TargetWebvpn: "webvpn.sicau.edu.cn",
}

// ── 跨子域统一错误码 ──────────────────────────────────────────────────────
// 取自 docs/tech/架构落点草图-WebVPN业务域.md §4，跨子域共用一套，避免各子域自造。

// Code 跨子域统一错误码。
type Code string

const (
	CodeTicketExpired     Code = "ticket_expired"
	CodeSessionExpired    Code = "session_expired"
	CodeCampusUnreachable Code = "campus_unreachable"
	CodeBlockedRedirect   Code = "blocked_redirect"
	CodeTargetDenied      Code = "target_denied"
)

// Error 带统一错误码的错误；用 errors.As(err, *Error) 可取出 Code。
type Error struct {
	Code Code
	msg  string
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.msg }

// ── 目标描述 ──────────────────────────────────────────────────────────────

// Target 一份已校验过的目标描述，同时给出直连形态与经 WebVPN 网关的形态。
type Target struct {
	Name   TargetName
	Host   string
	Direct *url.URL // 直连形态：https://<host>
	Vpn    *url.URL // 经网关形态：由 Encode 纯函数产出
}

// Resolve 只认逻辑目标名。未知名（含把 URL 当名字传入）返回
// *Error{Code: CodeTargetDenied}。两张 URL 形状都在这里一次性算好。
func Resolve(name TargetName) (Target, error) {
	host, ok := targetHosts[name]
	if !ok {
		return Target{}, &Error{
			Code: CodeTargetDenied,
			msg:  "未知逻辑目标名 " + string(name) + "：调用方只能传逻辑名，不能传 URL",
		}
	}

	direct, err := url.Parse("https://" + host)
	if err != nil {
		return Target{}, &Error{Code: CodeTargetDenied, msg: "构造直连形态失败：" + err.Error()}
	}
	// 经网关形态复用同一个纯函数；webvpn 目标命中 Encode 的「已是网关域」
	// 分支，原样返回，故 Direct 与 Vpn 一致（不重复编码）。
	vpnStr, err := Encode(direct.String())
	if err != nil {
		return Target{}, err
	}
	vpn, err := url.Parse(vpnStr)
	if err != nil {
		return Target{}, &Error{Code: CodeTargetDenied, msg: "构造经网关形态失败：" + err.Error()}
	}

	return Target{Name: name, Host: host, Direct: direct, Vpn: vpn}, nil
}
