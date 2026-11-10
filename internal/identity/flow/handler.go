package flow

// 编排层的 HTTP 传输层：只做「翻译请求 → 调编排 → 写响应」。
//
// 端点（统一挂在 /identity 下）：
//   - POST /identity/login            四种登录方式，成功后签发 App 会话
//   - POST /identity/login/admin      管理台登录，签发 admin 会话
//   - POST /identity/logout           吊销当前会话
//   - POST /identity/logout/all       吊销该用户 App 域的全部会话（退全部设备）
//   - POST /identity/mailcode/send    发送邮箱验证码
//
// 响应里返回**令牌**：令牌只携带 session_id，由调用方放进 Authorization: Bearer。

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/identity"
	"nongyu-backend-plus/internal/identity/login"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/server"
)

// TokenIssuer 把 session_id 变成客户端可用的令牌（由装配层注入 server.TokenVerifier）。
type TokenIssuer interface {
	IssueSession(sessionID string, expiresAt time.Time) string
}

// Register 把编排层的路由挂到 gin.IRouter。
//
// ctxSessionID 用于登出时取当前会话；ctxUserID 目前未用但保留形状以便扩展。
func Register(r gin.IRouter, svc *Service, issuer TokenIssuer) {
	r.POST("/identity/login", loginHandler(svc, issuer))
	r.POST("/identity/login/admin", adminLoginHandler(svc, issuer))
	r.POST("/identity/logout", logoutHandler(svc, server.CurrentSessionID))
	r.POST("/identity/logout/all", logoutAllHandler(svc, server.CurrentUserID))
	r.POST("/identity/mailcode/send", sendMailCodeHandler(svc))
}

type loginReq struct {
	Method   string `json:"method"` // school / password / mailcode / idcard
	DeviceID string `json:"deviceId"`

	// school
	SchoolMethod string `json:"schoolMethod"` // password / wechat / phone
	StudentNo    string `json:"studentNo"`
	Password     string `json:"password"`
	Phone        string `json:"phone"`
	Code         string `json:"code"`
	WechatCode   string `json:"wechatCode"`
	// school：客户端已完成学校登录，交上来的凭据
	Ticket string `json:"ticket"`
	Cookie string `json:"cookie"`
	// GoAdminTgt 是统一身份认证的全局登录态 cookie；教务取数（专业/班级/年级）需要它。
	GoAdminTgt string `json:"goAdminTgt"`
	// XSGZToken 是学工系统的 access token；唯一数据源取全量档案需要它。
	XSGZToken string `json:"xsgzToken"`

	// mailcode
	Email string `json:"email"`

	// idcard
	IDCardSuffix4 string `json:"idCardSuffix4"`
	NameInitials  string `json:"nameInitials"`
}

type loginResp struct {
	Token string `json:"token"`
	// ExpiresAt 用 Unix 秒，避免时区歧义。
	ExpiresAt int64 `json:"expiresAt"`
	IsNew     bool  `json:"isNew"`
	// ProfileIncomplete 为真表示专业/班级/年级暂缺（教务源没抓到），登录本身成功。
	ProfileIncomplete bool `json:"profileIncomplete"`
	User              struct {
		ID        int64  `json:"id"`
		StudentNo string `json:"studentNo"`
		Name      string `json:"name"`
		College   string `json:"college"`
		Major     string `json:"major"`
		ClassName string `json:"className"`
		Grade     string `json:"grade"`
	} `json:"user"`
}

func loginHandler(svc *Service, issuer TokenIssuer) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req loginReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_request"})
			return
		}
		signed, err := svc.Login(c.Request.Context(), Params{
			Method:   req.Method,
			DeviceID: req.DeviceID,
			// 登录审计（issue #29）：IP 取自请求头。gin 的 ClientIP 会按
			// 受信任代理设置解析 X-Forwarded-For；默认只信直连地址。
			ClientIP: c.ClientIP(),
			SchoolCred: login.SchoolCredential{
				Method: req.SchoolMethod, StudentNo: req.StudentNo, Password: req.Password,
				Phone: req.Phone, Code: req.Code, WechatCode: req.WechatCode,
				CookieHeader: req.Cookie, GoAdminTgt: req.GoAdminTgt, XSGZToken: req.XSGZToken,
			},
			StudentNo:     req.StudentNo,
			Password:      req.Password,
			Email:         req.Email,
			Code:          req.Code,
			IDCardSuffix4: req.IDCardSuffix4,
			NameInitials:  req.NameInitials,
		})
		if err != nil {
			writeIdentityError(c, err)
			return
		}
		writeSignedIn(c, issuer, signed)
	}
}

func adminLoginHandler(svc *Service, issuer TokenIssuer) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req loginReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_request"})
			return
		}
		signed, err := svc.LoginAdmin(c.Request.Context(), req.StudentNo, req.Password)
		if err != nil {
			writeIdentityError(c, err)
			return
		}
		writeSignedIn(c, issuer, signed)
	}
}

func logoutHandler(svc *Service, ctxSessionID func(*gin.Context) (string, bool)) gin.HandlerFunc {
	return func(c *gin.Context) {
		sid, ok := ctxSessionID(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"code": string(identity.CodeUnauthorized)})
			return
		}
		if err := svc.Logout(c.Request.Context(), sid); err != nil {
			writeIdentityError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

// logoutAllHandler 吊销当前已认证用户在 App 域的全部会话。
// 身份只从 context 取（由可吊销鉴权中间件注入），**不接受请求体里的 userID**，
// 避免调用方借这个端点把别人的会话全踢掉。
func logoutAllHandler(svc *Service, ctxUserID func(*gin.Context) (int64, bool)) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, ok := ctxUserID(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"code": string(identity.CodeUnauthorized)})
			return
		}
		n, err := svc.LogoutAll(c.Request.Context(), uid, platform.ScopeApp)
		if err != nil {
			writeIdentityError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "revoked": n})
	}
}

func sendMailCodeHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Email string `json:"email"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_request"})
			return
		}
		if err := svc.SendMailCode(c.Request.Context(), req.Email); err != nil {
			writeIdentityError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"sent": true})
	}
}

func writeSignedIn(c *gin.Context, issuer TokenIssuer, s SignedIn) {
	var resp loginResp
	resp.Token = issuer.IssueSession(s.SessionID, s.ExpiresAt)
	resp.ExpiresAt = s.ExpiresAt.Unix()
	resp.IsNew = s.IsNew
	resp.ProfileIncomplete = s.ProfileIncomplete
	resp.User.ID = s.User.ID
	resp.User.StudentNo = s.User.StudentNo
	resp.User.Name = s.Profile.Name
	resp.User.College = s.Profile.College
	resp.User.Major = s.Profile.Major
	resp.User.ClassName = s.Profile.ClassName
	resp.User.Grade = s.Profile.Grade
	c.JSON(http.StatusOK, resp)
}

// writeIdentityError 把统一错误码映射成 HTTP 状态。
// 响应体只带错误码，**绝不含凭据、密文或底层细节**。
func writeIdentityError(c *gin.Context, err error) {
	code := identity.CodeOf(err)
	status := http.StatusInternalServerError
	switch code {
	case identity.CodeUnauthorized, identity.CodeSessionExpired, identity.CodeBadCredentials:
		status = http.StatusUnauthorized
	case identity.CodeNeedSchoolAuth:
		// 400 而不是 401：凭据本身没错，是「还没有账号，需要先走学校认证」。
		status = http.StatusBadRequest
	case identity.CodeForbidden, identity.CodeUserDisabled:
		status = http.StatusForbidden
	case identity.CodeLoginMethodUnbound:
		status = http.StatusBadRequest
	case identity.CodeTooManyAttempts:
		status = http.StatusTooManyRequests
	case identity.CodeConflict:
		status = http.StatusConflict
	}
	c.JSON(status, gin.H{"code": string(code)})
}
