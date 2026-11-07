package jiaowu

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/school"
)

// Register 把教务子域的 HTTP 路由挂到给定的 gin.IRouter 上。
//
// 由 app 装配层在拿到 Service 后调用；本包不 import app/server/config，
// 路由注册是装配层编排的结果（架构落点草图 §4 / §8，README 架构约束第 1、4 条）。
// 本票简化掉草图里的 authMiddleware 参数：#13 只切「取地址端到端可用」，
// 鉴权属于后续票；保留 Register 作为唯一注册入口即可，签名不牵扯其他包。
//
// 形态选择（回报里会说明）：GET /school/address?target=<逻辑名>&path=/xxx
// 用 GET + query 是最简且可测的形态 —— 无请求体、httptest 一行构造、返回纯 JSON。
func Register(r gin.IRouter, svc *Service) {
	r.GET("/school/address", addressHandler(svc))
	r.GET("/school/fetch", fetchHandler(svc))
}

// RegisterWithAuth 在 Register 的两条入口之外，额外挂载 #15 换票主路径的三条路由。
// 装配层与测试若需要票据能力就用它；Register 保持 #13/#14 的原样形态不变。
func RegisterWithAuth(r gin.IRouter, svc *Service) {
	Register(r, svc)
	r.GET("/school/captcha", captchaHandler(svc))
	r.POST("/school/login", loginHandler(svc))
	r.DELETE("/school/logout", logoutHandler(svc))
}

// addressResponse 是成功响应：地址 + 逻辑目标名。
type addressResponse struct {
	Target  string `json:"target"`
	Address string `json:"address"`
}

// errorResponse 是错误响应：统一错误码 + 可读信息。
type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// addressHandler 把 HTTP 请求翻译为 Service.Address 调用，再把统一错误码映射为
// 明确的 HTTP 状态。它只做传输层适配，不含业务判断。
func addressHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := school.TargetName(c.Query("target"))
		path := c.Query("path")

		result, err := svc.Address(c.Request.Context(), name, path)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, addressResponse{
			Target:  result.Target,
			Address: result.Address,
		})
	}
}

// fetchHandler 把 HTTP 请求翻译为 Service.Fetch 调用：成功时透出上游状态码与响应体，
// 失败时把统一错误码映射为明确 HTTP 状态。它只做传输层适配，不含业务判断——
// 302 / Set-Cookie / 白名单全在 GuardedDoer（实现层）处理。
func fetchHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := school.TargetName(c.Query("target"))
		path := c.Query("path")

		result, err := svc.Fetch(c.Request.Context(), name, path)
		if err != nil {
			writeError(c, err)
			return
		}
		c.Data(result.Status, "application/octet-stream", result.Body)
	}
}

// writeError 把子域错误映射为 HTTP 响应。跨子域统一的 *school.Error 用其 Code
// 作为响应码；闸门拒绝（含 CodeTargetDenied）用 400 表达「入参不被接受」。
// 任何非 *school.Error 的错误都不泄漏内部细节，统一 500。
func writeError(c *gin.Context, err error) {
	var se *school.Error
	if errors.As(err, &se) {
		status := http.StatusInternalServerError
		// 目标不在白名单属于调用方入参错误；两类失效/禁止跟随用 4xx 明确区分，
		// 便于客户端判断是该重新登录还是该停下（ADR-0001 第 5 条）。
		switch se.Code {
		case school.CodeTargetDenied:
			status = http.StatusBadRequest
		case school.CodeBlockedRedirect:
			status = http.StatusBadGateway
		case school.CodeSessionExpired:
			status = http.StatusUnauthorized
		}
		c.JSON(status, errorResponse{Code: string(se.Code), Message: se.Error()})
		return
	}
	c.JSON(http.StatusInternalServerError, errorResponse{Code: "internal_error", Message: "内部错误"})
}

// ── #15 换票主路径的 HTTP 适配层 ──────────────────────────────────────────

func captchaHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := school.TargetName(c.Query("target"))
		if name == "" {
			name = school.TargetAuth
		}
		result, err := svc.Captcha(c.Request.Context(), name)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

type loginRequest struct {
	UserID string `json:"userID"`
	Code   string `json:"code"`
	UUID   string `json:"uuid"`
}

func loginHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in loginRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Code: "bad_request", Message: "请求体非法"})
			return
		}
		result, err := svc.Login(c.Request.Context(), in.UserID, in.Code, in.UUID)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

func logoutHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.Query("userID")
		if userID == "" {
			c.JSON(http.StatusBadRequest, errorResponse{Code: "bad_request", Message: "缺少 userID"})
			return
		}
		if err := svc.Logout(c.Request.Context(), userID); err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}
