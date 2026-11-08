package platform

import (
	"context"
	"time"
)

// AuditEntry 是一条审计流水。
//
// 铁律（ADR-0001 第 4 条）：这里**只允许**放标识性字段。
// 绝不添加票据、密文、验证码、上游响应体等字段——审计表不能成为泄漏面。
type AuditEntry struct {
	// RequestID 是一次请求链路的追踪号（审计流水号）。
	RequestID string
	// UserID 是已认证身份（school.CtxUserID）；未认证场景为空串。
	UserID string
	// Action 是业务动作：login / logout / address / fetch / captcha / login_accel / renew
	Action string
	// Target 是逻辑目标名：jiaowu / auth / webvpn
	Target string
	// Result 是结果码：ok / target_denied / outbound_error / captcha_error / ...
	Result string
	// OccurredAt 是事件发生时刻（由应用时钟给出）。
	OccurredAt time.Time
}

// AuditStore 是审计流水的持久化能力。
//
// 审计是**旁路**：写入失败绝不能影响业务主流程，所以实现应当异步批量写入，
// 并把错误通过 LastError 之类的观察点暴露，而不是让调用方处理。
type AuditStore interface {
	// Append 投递一条审计（实现应为非阻塞；满缓冲后可丢弃并计数）。
	Append(ctx context.Context, entry AuditEntry) error
	// Flush 把缓冲区内的记录同步落库（关停前调用）。
	Flush(ctx context.Context) error
	// Dropped 返回因缓冲满而被丢弃的条数（可观测性）。
	Dropped() int64
	// Close 释放连接。
	Close() error
}
