package login

// 登录子域的 HTTP 传输层：只做「翻译请求 → 调 Service → 写响应」，
// 不含业务判断（全部在 service.go）。
//
// 端点（与 docs/tech/架构落点-身份仓.md 第七节一致）：
//   - POST /identity/login/school     学校统一身份认证
//   - POST /identity/login/password   农屿账密
//   - POST /identity/login/mailcode   邮箱验证码
//   - POST /identity/login/idcard     学号 + 身份证后4位 + 姓名首字母
//   - POST /identity/mailcode/send    发送邮箱验证码
//
// 注意：本包不 import app/server/config；身份写入 context 的动作由装配层挂的
// 中间件完成，这里只从 context 读（登录端点本身不需要已认证身份）。

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/identity"
)

// 说明：登录端点本身不签发会话——签发属于 session 子域，
// 由装配层在登录成功后编排（login 不 import session，保持子域隔离）。

// Register 把登录相关路由挂到 gin.IRouter。
func Register(r gin.IRouter, svc *Service) {
	r.POST("/identity/login/school", schoolHandler(svc))
	r.POST("/identity/login/password", passwordHandler(svc))
	r.POST("/identity/login/mailcode", mailCodeHandler(svc))
	r.POST("/identity/login/idcard", idCardHandler(svc))
	r.POST("/identity/mailcode/send", sendMailCodeHandler(svc))
}

type schoolReq struct {
	Method     string `json:"method"` // password / wechat / phone
	StudentNo  string `json:"studentNo"`
	Password   string `json:"password"`
	Phone      string `json:"phone"`
	Code       string `json:"code"`
	WechatCode string `json:"wechatCode"`
	DeviceID   string `json:"deviceId"`
}

type loginResp struct {
	UserID    int64  `json:"userId"`
	StudentNo string `json:"studentNo"`
	Name      string `json:"name"`
	IsNew     bool   `json:"isNew"`
}

func schoolHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req schoolReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_request"})
			return
		}
		res, err := svc.LoginBySchool(c.Request.Context(), SchoolCredential{
			Method: req.Method, StudentNo: req.StudentNo, Password: req.Password,
			Phone: req.Phone, Code: req.Code, WechatCode: req.WechatCode,
		})
		if err != nil {
			writeIdentityError(c, err)
			return
		}
		c.JSON(http.StatusOK, loginResp{
			UserID: res.User.ID, StudentNo: res.User.StudentNo,
			Name: res.Profile.Name, IsNew: res.IsNew,
		})
	}
}

type passwordReq struct {
	StudentNo string `json:"studentNo"`
	Password  string `json:"password"`
}

func passwordHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req passwordReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_request"})
			return
		}
		res, err := svc.LoginByPassword(c.Request.Context(), req.StudentNo, req.Password)
		if err != nil {
			writeIdentityError(c, err)
			return
		}
		c.JSON(http.StatusOK, loginResp{UserID: res.User.ID, StudentNo: res.User.StudentNo, Name: res.Profile.Name})
	}
}

type mailCodeReq struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

func mailCodeHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req mailCodeReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_request"})
			return
		}
		res, err := svc.LoginByMailCode(c.Request.Context(), req.Email, req.Code)
		if err != nil {
			writeIdentityError(c, err)
			return
		}
		c.JSON(http.StatusOK, loginResp{UserID: res.User.ID, StudentNo: res.User.StudentNo, Name: res.Profile.Name})
	}
}

type idCardReq struct {
	StudentNo    string `json:"studentNo"`
	Suffix4      string `json:"idCardSuffix4"`
	NameInitials string `json:"nameInitials"`
}

func idCardHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req idCardReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_request"})
			return
		}
		res, err := svc.LoginByIDCard(c.Request.Context(), req.StudentNo, req.Suffix4, req.NameInitials)
		if err != nil {
			writeIdentityError(c, err)
			return
		}
		c.JSON(http.StatusOK, loginResp{UserID: res.User.ID, StudentNo: res.User.StudentNo, Name: res.Profile.Name})
	}
}

type sendMailReq struct {
	Email string `json:"email"`
}

func sendMailCodeHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req sendMailReq
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

// writeIdentityError 把统一错误码映射成 HTTP 状态。
// 注意：错误响应里**绝不包含**凭据、密文或底层错误细节。
func writeIdentityError(c *gin.Context, err error) {
	code := identity.CodeOf(err)
	status := http.StatusInternalServerError
	switch code {
	case identity.CodeUnauthorized, identity.CodeSessionExpired, identity.CodeBadCredentials, identity.CodeNeedSchoolAuth:
		status = http.StatusUnauthorized
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
