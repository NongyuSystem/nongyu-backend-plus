package presence

// 本文件是「校内/校外状态」子域的 HTTP 传输层适配，只做「翻译请求 → 调 Service →
// 写响应」，不含任何业务判断（业务判断全在 presence.go 的 Report / Decide）。
//
// 端点形状（选最简且可测）：
//   - POST /school/presence   JSON 体 {"mode":"direct"|"vpn"}
//     客户端每次进入与网络切换时调用；身份来自认证上下文，不接受 body 里的 userID。
//   - GET  /school/presence
//     返回当前判定 {"mode":"direct"|"vpn","source":"client"|"backend"}。
//     source 让调用方与测试能直接观测「上报生效 / 陈旧回落」。
//
// 与 jiaowu.Register 保持同构：Register(r gin.IRouter, svc *Service)，由 app
// 装配层编排注册，本包不 import app / server / config。

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/school"
)

const (
	presenceRoute = "/school/presence"
	codeInvalid   = "invalid_presence"
)

// Register 把校内/校外状态子域的 HTTP 路由挂到给定的 gin.IRouter 上。
// 由 app 装配层在拿到 Service 后调用；本包不 import app/server/config。
func Register(r gin.IRouter, svc *Service) {
	r.POST(presenceRoute, reportHandler(svc))
	r.GET(presenceRoute, decideHandler(svc))
}

// reportRequest 是上报请求体：只收用户与模式，上报时刻由服务端 Clock 盖章，
// 不信任客户端自带的时间（避免时钟回拨/伪造把陈旧判定绕过）。
type reportRequest struct {
	Mode Mode `json:"mode"`
}

// reportResponse 是成功上报的回执。
type reportResponse struct {
	UserID string `json:"userID"`
	Mode   Mode   `json:"mode"`
}

// errorResponse 是错误响应：统一错误码 + 可读信息。形态与 jiaowu 子域一致。
type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// decideResponse 是判定响应：模式 + 来源（client=新鲜上报 / backend=无上报或陈旧回落）。
type decideResponse struct {
	Mode   Mode   `json:"mode"`
	Source string `json:"source"`
}

// reportHandler 把上报请求翻译为 Service.Report 调用。
// 身份只来自认证中间件写入的上下文（school.CtxUserID），请求体/query 的 userID 一律不生效。
func reportHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(school.CtxUserID)
		if userID == "" {
			writeError(c, http.StatusUnauthorized, "缺少已认证身份")
			return
		}
		var req reportRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "请求体不是合法 JSON 或缺少必填字段")
			return
		}
		if err := svc.Report(c.Request.Context(), ClientPresence{UserID: userID, Mode: req.Mode}); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, reportResponse{UserID: userID, Mode: req.Mode})
	}
}

// decideHandler 把判定请求翻译为 Service.DecideDetailed 调用。
// 身份同样只来自认证上下文，不接受 query 里的 userID 覆盖。
func decideHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(school.CtxUserID)
		if userID == "" {
			writeError(c, http.StatusUnauthorized, "缺少已认证身份")
			return
		}
		d, _ := svc.DecideDetailed(c.Request.Context(), userID)
		c.JSON(http.StatusOK, decideResponse{Mode: d.Mode, Source: d.Source})
	}
}

// writeError 输出统一形态的错误响应。本子域所有错误都是「调用方入参不被接受」，
// 统一 400 + invalid_presence，不泄漏内部错误细节。
func writeError(c *gin.Context, status int, message string) {
	c.JSON(status, errorResponse{Code: codeInvalid, Message: message})
}
