package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/school"
)

// RequestIDMiddleware 给每个请求分配一个追踪号（审计流水号），写入 context 与响应头。
//
// 优先复用调用方传入的 X-Request-Id（便于跨服务串联），没有则生成一个。
// 业务域只从 context 读，靠它把「一次请求里的多个动作」在审计表里串起来。
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqID := c.GetHeader("X-Request-Id")
		if reqID == "" {
			b := make([]byte, 8)
			if _, err := rand.Read(b); err == nil {
				reqID = hex.EncodeToString(b)
			} else {
				reqID = "unknown"
			}
		}
		// 写入 gin.Context（供 handler 用）与 request.Context（供业务域通过 ctx 读）。
		c.Set(school.CtxRequestID, reqID)
		c.Request = c.Request.WithContext(
			context.WithValue(c.Request.Context(), school.CtxRequestID, reqID),
		)
		c.Header("X-Request-Id", reqID)
		c.Next()
	}
}
