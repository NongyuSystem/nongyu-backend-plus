// Package httpserver 放 internal/app 的 **HTTP 外壳**相关接线。
//
// 内容与边界：
//   - http_server.go：#21 M7 的超时配置（Read/Write/Idle 三档），构造 *http.Server；
//   - status.go：/status 观测出口（探针组装与报告结构）；
//   - appsign.go：安卓请求签名（NY1）的三处接线——启动前强校验、中间件装配、启动后告警。
//
// 为什么从 app 根拆出来：这三块都是「围绕 HTTP 的横切关注点」，
// 与「按域装配」（assemble_*.go）是两件不同的事。放在一个子包里，
// 想确认「签名到底开没开」「超时配了多少」不必翻整个装配层。
//
// 注意包名是 httpserver 而不是 http：后者会与标准库 net/http 撞名，
// 导致同一文件里既要用标准库 http.Server、又要用本包时无法区分。
package httpserver
