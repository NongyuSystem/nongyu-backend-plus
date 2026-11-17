// Package secondclass 实现学校二课（PU / 川农定制版）整个读/写域的接入。
//
// 受控词表共 36 个 dataType（A 组 12 由 #73 落地 + B 组 24 由 #74 落地），每个一份
// JSON Schema（启动期编译，共 36 份）；读子域有 GET/POST/DELETE 三条缓存端点 +
// 未命中时下发的请求配方；写子域有 14 条写类配方（#75）。
//
// 依赖方向（README 架构约束）：只依赖父包 internal/school（CtxUserID / CtxRequestID）
// 与 internal/platform 的接口（CacheStore / Clock / AuditStore / AuditEntry）。
// 不 import 教务域 jiaowu、不 import app/server/config、不引入 HTTPDoer 或任何出站能力
// （规格 §2：后端对二课零出站、零凭据）。
// #75 追加写类配方端点：GET /school/secondclass/recipe/:recipeId 按稳定 id 下发 14 个写操作配方，只给参数槽位不给值。
//
// 盲区（规格 §3.4「须原样进实现与文档」，逐字保留、不得弱化）：
// 后端对二课全部请求无观测能力，不代发、不转发、不缓存其响应，无法限流、无法记审计、无法判定失败原因；不得声称已限流或已审计。
package secondclass
