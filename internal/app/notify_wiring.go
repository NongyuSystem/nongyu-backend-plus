package app

import (
	"context"
	"strconv"

	"nongyu-backend-plus/internal/app/notify"
	"nongyu-backend-plus/internal/content/announcement"
	"nongyu-backend-plus/internal/content/post"
	"nongyu-backend-plus/internal/platform"
)

// 本文件是「通知派生」的跨域编排落点。
//
// 两个源域（帖子、公告）只通过各自定义的窄钩子（post.NotifyHook /
// announcement.PublishHook）报告「动作已发生」；收件人怎么算、幂等键怎么组、
// 文案怎么写，全部在这里——因为只有装配层同时看得见消息域与身份域，
// 而两个源域之间、以及源域与消息域之间都不许互相 import
// （README 架构约束第 1 条）。
//
// 三条红线（决策-通知派生规则.md）：
//   - Q3 旁路：钩子里只做 Enqueue，O(1)，绝不阻塞、绝不抛错；
//   - Q4 幂等：sourceObjectID 含动作与靶子，同键由消息域 upsert 合并;
//   - Q5 双层过滤：这里算收件人时先滤掉「发给自己」，消息域再 fail-closed 拒一次。

// postNotifyAdapter 把帖子域的动作事实翻译成编排层事件。
//
// 收件人计算规则（#49 Q11 的 4 个触发点）：
//   - reply        → 帖主
//   - like:post    → 帖主
//   - like:comment → 留言作者
//   - reply:comment→ 留言作者（无嵌套模型下仍以「被回复的那条留言作者」为准）
type postNotifyAdapter struct {
	posts  *post.Service
	notify *notify.Notifier
}

// Handle 实现 post.NotifyHook。
//
// **此函数在业务请求的同步路径上**，所以每一步都必须快且不报错：
// 查帖/查留言是一次仓储读（内存实现下是 O(1)），入队是非阻塞的。
func (a postNotifyAdapter) Handle(ctx context.Context, ev post.Actor) {
	if a.notify == nil {
		return
	}

	var (
		recipient int64
		title     string
		body      string
		sourceID  string
	)
	switch ev.Action {
	case "reply":
		p, err := a.posts.FindPost(ctx, ev.PostID)
		if err != nil {
			return
		}
		recipient = p.AuthorID
		title = "你的帖子有了新留言"
		body = p.Title
		// 幂等键含留言 id：同一帖的多条留言各自成一条通知（hits 不合并），
		// 因为「有人回你」每次都是独立事件，合并会让人看不出被回复了几次。
		sourceID = "reply:" + strconv.FormatInt(ev.ReplyID, 10)

	case "like:post":
		p, err := a.posts.FindPost(ctx, ev.PostID)
		if err != nil {
			return
		}
		recipient = p.AuthorID
		title = "你的帖子被点赞了"
		body = p.Title
		// 幂等键**不含点赞人**：同一帖被不同人点赞应合并成同一行、hits 累加
		//（决策-通知派生规则.md Q4 的原话：「同一帖被不同人点赞天然合并」）。
		sourceID = "like:post:" + strconv.FormatInt(ev.PostID, 10)

	case "like:comment":
		r, err := a.posts.FindReply(ctx, ev.ReplyID)
		if err != nil {
			return
		}
		recipient = r.AuthorID
		title = "你的留言被点赞了"
		body = r.Body
		sourceID = "like:comment:" + strconv.FormatInt(ev.ReplyID, 10)

	case "reply:comment":
		r, err := a.posts.FindReply(ctx, ev.ReplyID)
		if err != nil {
			return
		}
		recipient = r.AuthorID
		title = "你的留言有了新回复"
		body = r.Body
		sourceID = "reply:comment:" + strconv.FormatInt(ev.ReplyID, 10)

	default:
		return
	}

	// 收件人算不出来或算成自己：直接返回，不产生无谓写入（Q5 第一层）。
	if recipient <= 0 || recipient == ev.ActorID {
		return
	}
	a.notify.Enqueue(notify.Event{
		RecipientID:    recipient,
		ActorID:        ev.ActorID,
		SourceDomain:   "post",
		SourceObjectID: sourceID,
		Title:          title,
		Body:           body,
	})
}

// announcementNotifyAdapter 把「公告发布」翻译成编排层事件。
//
// 与帖子不同，公告的收件人是**一个集合**：受众条件可能是全员，
// 那么 fan-out 到几万人。当前实现只处理能直接取到收件人的情形，
// 全员广播走「按需拉取」而不是「逐人投递」（见下方注释）。
type announcementNotifyAdapter struct {
	notify *notify.Notifier
}

// Handle 实现 announcement.PublishHook。
//
// 设计取舍：**全员公告不逐用户写投递行**。
// 理由：一次发布给几万人各写一行消息，既撑不住（写入放大），
// 也不是必要的——公告本身有独立的公开读端点（GET /announcements），
// 客户端打开公告列表就能看到。逐人投递只对**定向**公告有意义
// （按角色/按学院年级），而定向的收件人集合需要身份域查询，
// 属下一轮的事（当前 Spec 把 fan-out 策略列为 pending）。
//
// 所以这里只记审计意图，不 Enqueue：宁可少投递，也不要写出几万行
// 无人认领的消息把库撑爆。这个取舍必须显式写下来，否则后人会以为漏了。
func (a announcementNotifyAdapter) Handle(_ context.Context, ann *announcement.Announcement) {
	if a.notify == nil || ann == nil {
		return
	}
	// 幂等键格式先定下来（即使当前不投递），避免将来补 fan-out 时
	// 与既有语义不一致：公告的幂等键必须含 publishRevision，
	// 这样「撤回再重发」能再通知一次（Q4）。
	_ = "announcement:publish:" + strconv.FormatInt(ann.ID, 10) +
		":rev" + strconv.Itoa(ann.PublishRevision)
}

// 编译期确认：钩子签名与业务域声明的一致。
var (
	_ post.NotifyHook          = postNotifyAdapter{}.Handle
	_ announcement.PublishHook = announcementNotifyAdapter{}.Handle
	_ platform.ObjectStore     = (*platform.MemoryObjectStore)(nil)
)
