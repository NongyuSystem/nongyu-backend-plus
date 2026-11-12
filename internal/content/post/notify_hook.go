package post

import "context"

// Actor 是一次「动作已发生」的窄事件，供装配层派生通知。
//
// 为什么由帖子域定义、而不是直接调 app/notify：
// README 架构约束第 1 条禁止业务域之间互相 import，装配层 orchestrates；
// 帖子域若 import app/notify 就成了「域依赖装配层」的反向依赖。
// 所以这里只声明「发生了什么」，由装配层把它翻译成 notify.Event。
//
// 这个接口刻意只有三个字段：动作、靶子、动作人。任何与通知文案、
// 收件人计算相关的东西都**不进**业务域——那是编排层与消息域的职责
// （决策-通知派生规则.md Q2）。
type Actor struct {
	// Action 是动作标识，取值与幂等键一致：
	//   "reply"         → 帖子被回复
	//   "like:post"     → 帖子被点赞
	//   "like:comment"  → 留言被点赞
	//   "reply:comment" → 留言被回复（无嵌套模型下表现为「同帖再来一条留言」）
	Action string
	// ActorID 是触发动作的人；编排层据此过滤「发给自己」。
	ActorID int64
	// PostID 与 ReplyID 至少一个非零，标识靶子。
	PostID  int64
	ReplyID int64
}

// NotifyHook 由装配层注入：接收「动作已发生」的事实。
//
// **必须是非阻塞的**：实现里只应做 Enqueue 这类 O(1) 操作。
// 业务域在事务提交后同步调用它，所以钩子如果阻塞（等 IO、等锁），
// 就会把通知的失败传导成发帖失败——这违反「通知是旁路」的红线
// （决策-通知派生规则.md Q3）。
//
// 允许为 nil（未装配通知能力时），调用方必须先判空。
type NotifyHook func(ctx context.Context, ev Actor)

// SetNotifyHook 注入通知钩子。
//
// 用 setter 而不是塞进 Options：钩子依赖 Service 自身（装配层要先建 Service
// 才能建 notifier 的 Sink），构造期循环无法解开。装配层在两者都建好后调用一次。
func (s *Service) SetNotifyHook(h NotifyHook) { s.notify = h }

// fire 是内部统一入口：nil 安全，且吞掉钩子里的 panic。
//
// 为什么还要 recover：钩子是同进程代码，它 panic 会连带把发帖请求打挂。
// 通知不值得让主业务失败，所以这里兜一层——真正的实现不该 panic，
// 但「不该」不等于「不会」。
func (s *Service) fire(ctx context.Context, ev Actor) {
	if s.notify == nil {
		return
	}
	defer func() { _ = recover() }()
	s.notify(ctx, ev)
}
