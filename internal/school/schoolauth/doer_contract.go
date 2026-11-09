package schoolauth

// 本文件记录 Cheng 教务取数对 HTTPDoer 的**行为契约**。
//
// 为什么单独写一份：这条约束踩过一次，症状极其误导。
//
// 实测（2026-11-09，真实网络）：教务建立会话是 3 跳重定向
//
//	caslogin.asp → check_cas.asp?sign=.. → note.asp → index1.asp
//
// 而 `ASPSESSIONID*` 这个会话 cookie 是在**中间跳**的 302 上下发的。
//
// 如果 HTTPDoer 的实现启用了自动跟随（`http.Client` 的默认行为，
// 即 `CheckRedirect` 返回 nil），且没有配置 `cookiejar`，则：
//   - 客户端自己跟完了所有跳转；
//   - 中间跳的 Set-Cookie 被丢弃（没有 jar 就没处存）；
//   - 调用方拿到的是**最终响应**（200、无 Set-Cookie）。
//
// JiaowuSource 据此判断「教务未下发会话 cookie」，报错退出——
// 而真实原因跟 cookie 毫无关系，是「跳转被代理跟完了」。
//
// 因此 HTTPDoer 的实现必须满足：
//
//  1. **不自动跟随 3xx**：返回跳转响应本身，由调用方决定是否跟随。
//     这也是出站闸口（ADR-0001）的既定要求：3xx 要能被看见与拦截，
//     而不是被传输层悄悄吃掉。
//  2. **不自行管理 cookie**：Cookie 由调用方显式传入请求头，
//     响应里的 Set-Cookie 由调用方显式收集（resp.Cookies()）。
//     闸口本来就会剥离 Set-Cookie（ADR-0001 第 5 条）——
//     因此本条在闸口下表现为「实现方需要把 cookie 交回给调用方」。
//
// 若后续给 HTTPDoer 换成真实实现，务必在构造 http.Client 时设置：
//
//	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
//
// 否则本包会在第一步就误判失败。

// HTTPDoerContract 仅作文档占位，说明 JiaowuSource 对 HTTPDoer 的行为要求。
// 详见本文件顶部说明。
const HTTPDoerContract = "must-not-follow-redirects"
