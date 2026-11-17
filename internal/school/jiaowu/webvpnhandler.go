package jiaowu

// WebVPN 网关票的 HTTP 层：提交票、查询状态、代取页面。
//
//	POST   /school/webvpn-session        客户端交上登录网关后拿到的 wengine_vpn_ticket
//	GET    /school/webvpn-session        查询是否已保管（永不回显票值）
//	DELETE /school/webvpn-session        清掉票（登出或客户端发现失效时）
//	GET    /school/jiaowu/cache/:dataType/fetch 用已保管的票代取页面（校外形态）
//
// 为什么票值只在提交方向流动：票据是活凭证，能读出来就等于能冒用。
// 查询端点只回 present/expiresAt，绝不含 value。

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/school"
)

// RegisterWebVPN 挂载网关票相关路由。由装配层在 BindWebVPNTicketStore 之后调用。
func RegisterWebVPN(r gin.IRouter, svc *Service) {
	r.POST("/school/webvpn-session", submitWebVPNTicketHandler(svc))
	r.GET("/school/webvpn-session", statusWebVPNTicketHandler(svc))
	r.DELETE("/school/webvpn-session", clearWebVPNTicketHandler(svc))
	r.GET("/school/jiaowu/cache/:dataType/fetch", fetchViaWebVPNHandler(svc))
}

// webvpnSubmitRequest 是提交票的请求体。
//
// 只收 ticket 一个字段：身份永远来自认证中间件（school.CtxUserID），
// 绝不接受请求体里的 userID——否则任何人都能给别人的账户写票。
type webvpnSubmitRequest struct {
	Ticket string `json:"ticket"`
}

type webvpnSubmitResponse struct {
	Present   bool   `json:"present"`
	ExpiresAt string `json:"expiresAt"`
}

func submitWebVPNTicketHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := requireUserID(c)
		if !ok {
			return
		}
		var in webvpnSubmitRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Code: "bad_request", Message: "请求体非法"})
			return
		}
		exp, err := svc.SubmitWebVPNTicket(c.Request.Context(), userID, in.Ticket)
		if err != nil {
			writeWebVPNError(c, err)
			return
		}
		c.JSON(http.StatusOK, webvpnSubmitResponse{
			Present:   true,
			ExpiresAt: exp.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
}

func statusWebVPNTicketHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := requireUserID(c)
		if !ok {
			return
		}
		st := svc.WebVPNTicket(c.Request.Context(), userID)
		resp := gin.H{"present": st.Present}
		if st.Present {
			resp["expiresAt"] = st.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		c.JSON(http.StatusOK, resp)
	}
}

func clearWebVPNTicketHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := requireUserID(c)
		if !ok {
			return
		}
		if err := svc.ClearWebVPNTicket(c.Request.Context(), userID); err != nil {
			writeWebVPNError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"cleared": true})
	}
}

// fetchViaWebVPNHandler 用已保管的网关票代取教务页面。
//
// 成功时与 /school/fetch 同形：原样透出上游状态码与响应体（不解码、不解析）。
// 失败时给**客户端能据此行动**的错误码，见 writeWebVPNError。
func fetchViaWebVPNHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		dt, ok := dataTypeFromPath(c)
		if !ok {
			return
		}
		userID, ok := requireUserID(c)
		if !ok {
			return
		}
		result, err := svc.FetchViaWebVPN(c.Request.Context(), userID, dt)
		if err != nil {
			writeWebVPNError(c, err)
			return
		}
		for k, values := range result.Headers {
			if k == "Set-Cookie" {
				continue // 绝不把上游 Set-Cookie 透给调用方
			}
			for _, v := range values {
				c.Writer.Header().Add(k, v)
			}
		}
		c.Data(result.Status, "application/octet-stream", result.Body)
	}
}

// writeWebVPNError 把网关票相关错误映射为客户端可据以行动的 HTTP 响应。
//
// 三个码的语义必须分清，客户端动作完全不同：
//   - webvpn_ticket_missing  (409)：没交过票 → 去登录网关并 POST 交票；
//   - webvpn_ticket_expired  (401)：票失效   → **重新登录网关**后重交；
//   - cache_unavailable      (503)：功能未启用（没配存储）。
func writeWebVPNError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrWebVPNTicketMissing):
		c.JSON(http.StatusConflict, errorResponse{
			Code:    "webvpn_ticket_missing",
			Message: "尚未提交 WebVPN 网关票，请先登录网关并提交",
		})
	case errors.Is(err, ErrWebVPNTicketExpired):
		c.JSON(http.StatusUnauthorized, errorResponse{
			Code:    "webvpn_ticket_expired",
			Message: "WebVPN 会话已失效，请重新登录网关后重新提交",
		})
	case errors.Is(err, ErrCacheUnavailable):
		c.JSON(http.StatusServiceUnavailable, errorResponse{
			Code:    "cache_unavailable",
			Message: "网关票保管未启用",
		})
	default:
		var se *school.Error
		if errors.As(err, &se) {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{
			Code:    "internal_error",
			Message: "内部错误",
		})
	}
}
