package announcement

import "context"

// PublishHook 由装配层注入：公告成功发布后告知「该通知谁」。
//
// 签名与帖子域的 NotifyHook 不同，因为公告的收件人**不是单一用户**，
// 而是由受众条件（全员 / 按角色 / 按学院年级）算出的集合；集合的计算
// 依赖身份域数据，属装配层职责，公告域算不出来也不该算
// （决策-通知派生规则.md Q1：收件人计算全归 #48）。
//
// **必须是非阻塞的**：装配层实现里只做入队。公告发布是管理端同步动作，
// 钩子若阻塞会把 fan-out 的耗时算进发布请求。
type PublishHook func(ctx context.Context, a *Announcement)

// SetPublishHook 注入发布钩子（可为 nil）。
func (s *Service) SetPublishHook(h PublishHook) { s.publishHook = h }

// firePublished 在发布成功后触发；nil 安全且吞掉 panic（同帖子域的理由：
// 通知不值得让公告发布失败）。
func (s *Service) firePublished(ctx context.Context, a *Announcement) {
	if s.publishHook == nil {
		return
	}
	defer func() { _ = recover() }()
	s.publishHook(ctx, a)
}
