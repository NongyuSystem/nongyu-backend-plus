// Package presence 是「学校」业务域下的校内/校外状态子域（issue #16 切的第四条）。
//
// 两条来源、一个模型：后端启动时读配置得到「后端侧模式」（进程内不变，不做周期性
// 自探测），客户端每次进入 / 网络切换时上报一次「客户端侧模式」。Decide 把二者合并：
// 有新鲜上报就用上报的模式；上报超过陈旧阈值未更新视为不可信、退回后端配置；无上报
// 也用后端配置。
//
// 依赖方向：本包只依赖 internal/platform 的外部能力接口（TicketStore / Clock）。
// 不 import internal/app、internal/server、internal/config，也不 import 任何其他
// 业务子域（含 jiaowu）。上报状态全部外置到 TicketStore（进程无状态），上报时刻由
// Clock 盖章，陈旧判定同样由 Clock 驱动，便于用 MockClock.Set 推进时间做可测验证。
//
// 路由注册由 app 装配层编排：拿到本包的 Service 后调用 Register 挂到 gin.Engine。
// Service 不持有任何出站能力（构造参数里没有 HTTPDoer），因此后端侧判定在结构上
// 不可能发起出站请求或周期性自探测。
package presence
