// Package secondclass 实现学校二课（PU / 川农定制版）接入的 A 组学分与成绩单读通路。
//
// 本包是 issue #73 的首个可演示垂直切片：12 个受控 dataType + 每个一份 JSON Schema
// （启动期编译）+ GET/POST/DELETE 三条读端点 + 未命中时下发的请求配方。
//
// 依赖方向（README 架构约束）：只依赖父包 internal/school（CtxUserID / CtxRequestID）
// 与 internal/platform 的接口（CacheStore / Clock / AuditStore / AuditEntry）。
// 不 import 教务域 jiaowu、不 import app/server/config、不引入 HTTPDoer 或任何出站能力
// （规格 §2：后端对二课零出站、零凭据）。
package secondclass
