// Package notify 是通知派生的**装配层薄编排单元**。
//
// 为什么在 internal/app 下而不是新建 internal/notification 业务域
// （map #43 子票 #48 Q2 的决议）：派生逻辑只是「算收件人 → 调消息域写入口」，
// 没有独立状态与存储，做成域会与「装配层负责跨域协作」的既定分工重复。
//
// 为什么定义自己的 Sink 接口而不直接 import message.Service：
//   - 编排层不该绑定某个域的具体方法签名（那边还在演进）；
//   - 测试可以注入假 Sink，不必起消息域；
//   - 装配层只需写一个十几行的 adapter 把 message 的写入口适配成本接口。
//
// 语义（#48 Q1/Q3/Q4/Q5/Q6）：
//   - 5 个触发点：公告发布 1 + 帖子 4（被回复/被点赞/评论被点赞/评论被回复）；
//   - **异步旁路**：有界队列 + 后台 goroutine，队列满/写入失败只丢并计数，绝不阻塞主业务；
//   - **幂等键**由 SourceDomain+SourceObjectID 承担，SourceObjectID 含动作与靶子；
//   - **发给自己过滤**在编排层先做一遍（消息域再做一遍，双保险）；
//   - 派生动作记审计 notification.emit / notification.dropped，只记标识性字段。
package notify

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// deliverTimeout 是单条通知写 Sink 的超时上界。
//
// 旁路不能因为下游慢而无限占用后台协程；超时即按丢弃处理。
const deliverTimeout = 3 * time.Second

// Event 是一次「该给谁发一条通知」的编排层事件。
//
// 刻意不含消息正文之外的敏感信息（姓名、邮箱、画像）——编排层只搬运标识。
type Event struct {
	// RecipientID 是收件人。
	RecipientID int64
	// ActorID 是触发动作的人。用于在编排层过滤「发给自己」。
	ActorID int64
	// SourceDomain 是来源域：post / announcement。
	SourceDomain string
	// SourceObjectID 是幂等键的核心，含动作与靶子：
	//   "reply:<replyID>"、"like:post:<postID>"、"like:comment:<replyID>"、
	//   "publish:<announcementID>:rev<N>"。
	SourceObjectID string
	// Title / Body 是触发方渲染好的**快照**文案（消息域不做读时渲染）。
	Title string
	Body  string
}

// Sink 是消息域写入口在编排层的投影。
//
// 装配层把 message.Service.Emit 适配成它即可；返回 error 时编排层按「丢弃」处理，
// 绝不把错误回抛给主业务（旁路语义）。
type Sink interface {
	Emit(ctx context.Context, ev Event) error
}

// SinkFunc 让普通函数也能当 Sink（测试与简单适配用）。
type SinkFunc func(ctx context.Context, ev Event) error

// Emit 实现 Sink。
func (f SinkFunc) Emit(ctx context.Context, ev Event) error { return f(ctx, ev) }

// AuditFunc 是审计记录的注入口（避免编排层 import platform.AuditStore）。
//
// action 取 notification.emit / notification.dropped；target 是
// "<sourceDomain>:<sourceObjectID>"。只记标识性字段，不记正文/收件人。
type AuditFunc func(ctx context.Context, action, target string)

// Options 是构造 Notifier 的依赖。
type Options struct {
	Sink Sink
	// Capacity 是有界队列容量（施工期可定，默认 1024）。
	Capacity int
	// Audit 可为 nil（不记审计）。
	Audit AuditFunc
	// Logger 可为 nil（丢弃日志）。
	Logger *slog.Logger
}

// Notifier 持有有界队列与后台消费协程。
type Notifier struct {
	sink    Sink
	queue   chan Event
	audit   AuditFunc
	logger  *slog.Logger
	dropped atomic.Int64
	emitted atomic.Int64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// New 构造并启动 Notifier 的后台消费协程。
func New(opts Options) *Notifier {
	if opts.Capacity <= 0 {
		opts.Capacity = 1024
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(ioDiscard{}, nil))
	}
	ctx, cancel := context.WithCancel(context.Background())
	n := &Notifier{
		sink:   opts.Sink,
		queue:  make(chan Event, opts.Capacity),
		audit:  opts.Audit,
		logger: opts.Logger,
		ctx:    ctx,
		cancel: cancel,
	}
	n.wg.Add(1)
	go n.run()
	return n
}

// Enqueue 把一次通知投递进队列，**永不阻塞主业务**。
//
// 三条丢弃路径都计入 dropped 并记 notification.dropped 审计：
//  1. 收件人为 0；
//  2. ActorID == RecipientID（发给自己，编排层第一层过滤）；
//  3. 队列满（非阻塞 select 的 default 分支）。
func (n *Notifier) Enqueue(ev Event) {
	if ev.RecipientID == 0 {
		return
	}
	if ev.ActorID != 0 && ev.ActorID == ev.RecipientID {
		return // #48 Q5 第一层：自赞/自回不产生通知
	}
	select {
	case n.queue <- ev:
		// 入队成功，交给后台协程。
	default:
		n.markDropped(ev, "queue_full")
	}
}

// markDropped 统一处理丢弃：计数 + 审计 + 日志。
func (n *Notifier) markDropped(ev Event, reason string) {
	n.dropped.Add(1)
	if n.audit != nil {
		n.audit(context.Background(), "notification.dropped", ev.SourceDomain+":"+ev.SourceObjectID)
	}
	n.logger.Warn("通知派生丢弃", "reason", reason,
		"sourceDomain", ev.SourceDomain, "sourceObjectID", ev.SourceObjectID)
}

func (n *Notifier) run() {
	defer n.wg.Done()
	for {
		select {
		case <-n.ctx.Done():
			// 关停：把队列里剩的排空（不新接 Enqueue 之外的活）。
			n.drain()
			return
		case ev := <-n.queue:
			n.deliver(ev)
		}
	}
}

// deliver 调 Sink 写一条；失败按丢弃处理，绝不回抛。
func (n *Notifier) deliver(ev Event) {
	ctx, cancel := context.WithTimeout(context.Background(), deliverTimeout)
	defer cancel()
	if n.sink == nil {
		n.markDropped(ev, "no_sink")
		return
	}
	if err := n.sink.Emit(ctx, ev); err != nil {
		n.markDropped(ev, "sink_error")
		n.logger.Warn("通知写入失败", "err", err,
			"sourceDomain", ev.SourceDomain, "sourceObjectID", ev.SourceObjectID)
		return
	}
	n.emitted.Add(1)
	if n.audit != nil {
		n.audit(context.Background(), "notification.emit", ev.SourceDomain+":"+ev.SourceObjectID)
	}
}

// drain 排空队列中剩余事件（关停前 flush）。
func (n *Notifier) drain() {
	for {
		select {
		case ev := <-n.queue:
			n.deliver(ev)
		default:
			return
		}
	}
}

// Flush 等待队列排空并停止后台协程。
//
// 它是幂等的（sync.Once）；main 的优雅关停路径调用它，保证
// 「关停前 flush」这条 Spec 要求落地。
func (n *Notifier) Flush() {
	n.once.Do(func() {
		n.cancel()
		n.wg.Wait()
	})
}

// Dropped 返回累计丢弃数（供 /status 或指标观测）。
func (n *Notifier) Dropped() int64 { return n.dropped.Load() }

// Emitted 返回累计成功投递数。
func (n *Notifier) Emitted() int64 { return n.emitted.Load() }

// ioDiscard 是 slog 的零输出 handler 目标，避免为「无 logger」引入 os import。
type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
