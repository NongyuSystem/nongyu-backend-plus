package session

// 会话子域的 HTTP 传输层。
//
// 端点：
//   - POST /identity/logout   吊销当前会话
//
// 签发不在这里（签发由服务端在登录成功后调用 session.Service.Issue 完成，
// 不通过 HTTP 暴露，避免被用来「凭 userID 直接要一张票」）。

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/identity"
)

// CtxSessionIDOf 由装配层注入，取当前请求的 session_id。
type CtxSessionIDOf func(c *gin.Context) (string, bool)

// Register 把会话相关路由挂到 gin.IRouter。
func Register(r gin.IRouter, svc *Service, ctxSessionID CtxSessionIDOf) {
	r.POST("/identity/logout", logoutHandler(svc, ctxSessionID))
}

func logoutHandler(svc *Service, ctxSessionID CtxSessionIDOf) gin.HandlerFunc {
	return func(c *gin.Context) {
		sid, ok := ctxSessionID(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"code": string(identity.CodeUnauthorized)})
			return
		}
		if err := svc.Revoke(c.Request.Context(), sid); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": string(identity.CodeInternal)})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}
