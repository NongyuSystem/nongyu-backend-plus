package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/school"
)

// maxClientRequestIDLen 是采信客户端 X-Request-Id 的长度上限。
//
// 这个值会进结构化日志、ES 与审计表（AuditStore.RequestID），超长串既是存储
// 噪声也是日志注入面；长度有界是「审计流水号」这一语义的最低要求。
const maxClientRequestIDLen = 128

// RequestIDMiddleware 给每个请求分配一个追踪号（审计流水号），写入 context 与响应头。
//
// 优先复用调用方传入的 X-Request-Id（便于跨服务串联），但**只在它可信时**复用：
// 该头完全由外部控制，若原样采信，攻击者可伪造流水号把不同用户的动作串到
// 同一条审计链上，或用制表符/控制字符/超长串污染结构化日志与 CSV/ES 字段。
// 因此这里对客户端值做「长度 + 字符集白名单」校验，不合格一律丢弃并自生成；
// 响应头、gin.Context、request.Context 三处都只落规范化后的值，绝不回显原串。
//
// 业务域只从 context 读，靠它把「一次请求里的多个动作」在审计表里串起来。
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqID := c.GetHeader("X-Request-Id")
		if !validClientRequestID(reqID) {
			reqID = generateRequestID()
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

// validClientRequestID 判断客户端给的追踪号能否被采信。
//
// 只接受 1..128 字节的 [A-Za-z0-9._-]：这个闭集不含空白、控制字符、制表符、
// 引号、逗号、斜杠，既挡住日志/CSV/ES 注入，也保证长度有界。
// 空串返回 false —— 让「没有头」与「头非法」统一走自生成兜底，不留分支差异。
func validClientRequestID(s string) bool {
	if s == "" || len(s) > maxClientRequestIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

// generateRequestID 生成服务端自有的追踪号；熵源失败时退化为固定 "unknown"。
// 16 个 hex 字符天然落在 [A-Za-z0-9] 白名单内，无需再过一遍校验。
func generateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}
