package app

// #24 / Spec #18 M6：可观测性出口。
//
// /healthz 的语义是「只报进程可达、不探下游」（Spec #10 故事 28），**不要改它**。
// 本文件另开 /status，用于运维/监控查看真实依赖状态：进程、Postgres、Redis、
// 缓存熔断器、当前存储实现是真实还是 mock。值必须来自真实探针，不能硬编码 ok。

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// StatusProbe 是一组依赖探针。每个函数返回该依赖的真实状态。
// 用函数而不是固定值：/status 每次请求都要现查，Redis 掉线后能立刻反映出来。
type StatusProbe struct {
	Postgres func() bool
	Redis    func() bool
	Breaker  func() string
	Storage  func() string
	// AttachmentCleanup 返回异步附件清理的累计失败数。
	//
	// 与 Track 同理：附件清理是「失败只记日志」的旁路，没有计数
	// 运维永远不知道对象存储一直在报错、附件一直没被清理。
	// 允许为 nil（未装配帖子域时）。
	AttachmentCleanup func() int64
	// Track 返回埋点缓冲的 (已写入, 已丢弃)。
	//
	// 为什么放进 /status：Spec 明确要求把 dropped 暴露成可观测指标——
	// 「缓冲满就丢」是个静默行为，没有计数运维就永远不知道在丢数据。
	// 允许为 nil（未装配埋点域时）。
	Track func() (written, dropped int64)
}

// StatusReport 是 /status 的响应体。
type StatusReport struct {
	Process  string `json:"process"`
	Postgres string `json:"postgres"`
	Redis    string `json:"redis"`
	Breaker  string `json:"breaker"`
	Storage  string `json:"storage"`
	// Track 是埋点缓冲的计数（未装配时为 null）。
	Track *TrackStats `json:"track,omitempty"`
	// AttachmentCleanupFailures 是异步附件清理的累计失败数
	//（未装配帖子域时为 null）。
	AttachmentCleanupFailures *int64 `json:"attachmentCleanupFailures,omitempty"`
}

// RegisterStatus 把 /status 挂到 gin.IRouter。
// 与 /healthz 分开注册，互不影响。
func RegisterStatus(r gin.IRouter, p StatusProbe) {
	r.GET("/status", func(c *gin.Context) {
		report := StatusReport{
			Process:  "up", // 能进到这里就说明进程活着
			Postgres: upOrDown(p.Postgres),
			Redis:    upOrDown(p.Redis),
			Breaker:  stringOrUnknown(p.Breaker),
			Storage:  stringOrUnknown(p.Storage),
		}
		if p.Track != nil {
			w, d := p.Track()
			report.Track = &TrackStats{Written: w, Dropped: d}
		}
		if p.AttachmentCleanup != nil {
			n := p.AttachmentCleanup()
			report.AttachmentCleanupFailures = &n
		}
		// 依赖全挂时不返回 200 会让编排误杀进程（与 /healthz 同理），
		// 因此状态码恒 200，真实情况放在 body 里由监控侧判断。
		c.JSON(http.StatusOK, report)
	})
}

func upOrDown(fn func() bool) string {
	if fn == nil {
		return "unknown"
	}
	if fn() {
		return "up"
	}
	return "down"
}

func stringOrUnknown(fn func() string) string {
	if fn == nil {
		return "unknown"
	}
	return fn()
}

// newStatusProbe 在装配层用真实依赖构造探针。
// 传进来的 ping 函数各自带超时，避免 /status 自己挂死。
func newStatusProbe(
	pgPing, redisPing func(context.Context) error,
	breaker func() string,
	storage string,
	track func() (int64, int64),
	attachmentCleanup func() int64,
) StatusProbe {
	probe := func(ping func(context.Context) error) func() bool {
		return func() bool {
			if ping == nil {
				return false
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return ping(ctx) == nil
		}
	}
	return StatusProbe{
		Postgres:          probe(pgPing),
		Redis:             probe(redisPing),
		Breaker:           breaker,
		Storage:           func() string { return storage },
		Track:             track,
		AttachmentCleanup: attachmentCleanup,
	}
}

// TrackStats 是埋点缓冲的观测值。
//
// Dropped 是**业务上真正重要的那个数**：埋点是「满了就丢」的旁路，
// 没有这个计数，运维永远不知道在丢数据（Spec 明确要求暴露 dropped）。
type TrackStats struct {
	Written int64 `json:"written"`
	Dropped int64 `json:"dropped"`
}
