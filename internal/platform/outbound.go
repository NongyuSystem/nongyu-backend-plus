package platform

// ⚠️ 本文件已拆分完毕，内容分布如下：
//
//	http_doer.go    RealHTTPDoer（真实出站：白名单 / 不跟随 3xx / 超时 / 响应体上限 / SSRF 闸门）
//	capabilities.go TicketStore、TicketRef、Cipher、HTTPDoer、Clock 契约 + 出站哨兵错误
//
// 本文件由 `// ---- from http_doer.go ----` / `// ---- from capabilities.go ----` 两段拼接产物
// 还原而来。保留空声明只为让拆分这一步自洽可回滚；**下一次 commit 必须删除本文件**。
