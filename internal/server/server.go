// Package server 负责 HTTP 框架的装配：路由注册、中间件、健康检查。
// 这里是「框架适配层」——业务逻辑不应写在本包，而应放进各自的 internal/<业务域>。
package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// New 构造并返回一个已注册好基础路由的 *gin.Engine。
// 骨架阶段只挂 /healthz；业务路由由 app 装配层在拿到各自 handler 后注册。
func New() *gin.Engine {
	r := gin.New()
	// 恢复 panic + 结构化日志，是一切后续中间件的基础。
	r.Use(gin.Recovery(), gin.Logger())

	r.GET("/healthz", healthz)
	return r
}

// healthz 是存活探针：只报告进程可达，不检查下游依赖。
// 就绪探针（readyz）等接入数据库后再单独添加，避免下游故障导致进程被误杀。
func healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
	})
}
