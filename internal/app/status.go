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
}

// StatusReport 是 /status 的响应体。
type StatusReport struct {
	Process  string `json:"process"`
	Postgres string `json:"postgres"`
	Redis    string `json:"redis"`
	Breaker  string `json:"breaker"`
	Storage  string `json:"storage"`
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
		Postgres: probe(pgPing),
		Redis:    probe(redisPing),
		Breaker:  breaker,
		Storage:  func() string { return storage },
	}
}
