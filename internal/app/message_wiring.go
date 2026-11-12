package app

import (
	"context"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/app/notify"
	"nongyu-backend-plus/internal/message"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/server"
)

// 本文件把「站内消息域 + 通知派生编排层」接进装配。
//
// 为什么单独一个文件而不是继续塞进 app.go：app.go 已经 700+ 行且承担了
// 装配根的全部职责；新增能力的接线细节放独立文件，便于按能力阅读，
// 也避免每次新增域都在同一个巨型函数末尾追加一段。

// messageUserExists 把身份域的「用户是否存在」适配成消息域需要的窄接口。
//
// 存在的按 true；platform.IsNotFound → false（收件人不存在，fail-closed 拒收）；
// 其它错误原样上抛——数据库故障不能被当成「用户不存在」，
// 否则一次 DB 抖动会让所有内部信都被误判为收件人无效。
func messageUserExists(repo platform.UserRepo) message.UserExistsFunc {
	return func(ctx context.Context, userID int64) (bool, error) {
		if userID <= 0 {
			return false, nil
		}
		_, err := repo.ByID(ctx, userID)
		if platform.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return true, nil
	}
}

// messageLimiter 把既有的 server.RateLimiter 适配成消息域的 Limiter。
//
// 复用既有实现而不是新写一套滑动窗口：限流口径只有一处，
// 配额行为与其它端点一致。
func messageLimiter(rl *server.RateLimiter) message.Limiter {
	if rl == nil {
		return nil
	}
	return rateLimiterAdapter{rl: rl}
}

type rateLimiterAdapter struct{ rl *server.RateLimiter }

// Allow 实现 message.Limiter。既有 RateLimiter 只有「允许/不允许」，
// 无后端故障概念，因此 error 恒为 nil。
func (a rateLimiterAdapter) Allow(_ context.Context, key string) (bool, error) {
	return a.rl.Allow(key), nil
}

// notifySink 把 message.Service.Emit 适配成编排层的 notify.Sink。
//
// 这是装配层「跨域编排」职责的落点：两个域互不 import，
// 由这里把「消息域的写入口」投影成「编排层认识的 Sink」。
type notifySink struct{ svc *message.Service }

// Emit 实现 notify.Sink。
func (s notifySink) Emit(ctx context.Context, ev notify.Event) error {
	return s.svc.Emit(ctx, message.EmitInput{
		RecipientID:    ev.RecipientID,
		ActorID:        ev.ActorID,
		SourceDomain:   ev.SourceDomain,
		SourceObjectID: ev.SourceObjectID,
		Title:          ev.Title,
		Body:           ev.Body,
	})
}

// notifyAudit 把审计能力适配成编排层的 AuditFunc。
//
// 只记标识性字段（action + target），绝不写消息正文与收件人——这是
// ADR-0001 的红线，也是 #48 Q6 的明确要求。
func notifyAudit(store platform.AuditStore, clock platform.Clock) notify.AuditFunc {
	if store == nil {
		return nil
	}
	return func(ctx context.Context, action, target string) {
		_ = store.Append(ctx, platform.AuditEntry{
			Action:     action,
			Target:     target,
			Result:     "ok",
			OccurredAt: clock.Now(),
		})
	}
}

// currentUserIDOf 从 gin.Context 取当前会话用户 id。
//
// 供消息域与公告域共用：两者都只需要「谁」，不需要角色等其它事实。
// 业务域不 import server，由装配层注入。
func currentUserIDOf(c *gin.Context) (int64, bool) {
	return server.CurrentUserID(c)
}
