// Package jiaowu 是「学校」业务域下的教务子域（issue #13 切的第一条）。
//
// 依赖方向：本包只依赖父包 internal/school（逻辑目标名、Resolve、Encode、统一错误码）
// 与 internal/platform 的外部能力接口（目前接住 HTTPDoer 供后续票复用）。
// 不 import internal/app、internal/server、internal/config，也不 import 任何其他业务子域。
// 路由注册由 app 装配层编排：拿到本包的 Service 后调用 Register 挂到 gin.Engine。
//
// Address 是本票唯一的 HTTP 入口：只返回可直连的编码地址，绝不发起任何出站请求
// （出站闸口是 platform.HTTPDoer，留给后续 Fetch 票）。目标集合由 school.Resolve
// 的逻辑名白名单把守；非白名单目标（未知逻辑名、把 URL/host 当名字传）在闸口处
// 即以 CodeTargetDenied 拒绝，不会有任何出站请求产生。
package jiaowu
