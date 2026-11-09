package user

// 用户子域的 HTTP 传输层：只做「翻译请求 → 调 Service → 写响应」。
//
// 端点：
//   - GET   /identity/me           读自己的画像
//   - PATCH /identity/me/profile   改自己的农屿扩展字段（QQ、头像）
//   - GET   /identity/users/:studentNo  管理台按学号读画像（需管理员，鉴权由装配层中间件保证）
//
// 身份一律来自 context（由鉴权中间件写入），**不读请求体或 query 里的 userID**。
// 这条规则在旧农屿被违反过：登录时用请求体整体覆盖画像，任何人报个学号就能改他人档案。

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/identity"
	"nongyu-backend-plus/internal/platform"
)

// CtxUserIDOf 由装配层注入，取当前调用者 userID（int64）。
// 为避免 user 子域 import server，这里用函数类型注入。
type CtxUserIDOf func(c *gin.Context) (int64, bool)

// Register 把用户相关路由挂到 gin.IRouter。
// ctxUserID 由装配层传入（通常是 server 包提供的读取器）。
func Register(r gin.IRouter, svc *Service, ctxUserID CtxUserIDOf, profiles platform.ProfileRepo) {
	r.GET("/identity/me", meHandler(svc, ctxUserID))
	r.PATCH("/identity/me/profile", patchProfileHandler(svc, ctxUserID))
	r.GET("/identity/users/:studentNo", adminProfileHandler(svc, profiles))
}

// profileResp 是画像的对外形状。
//
// **默认不含**身份证号与密文；只有管理台详情端点显式加 idCardSuffix4。
// 「允许返回身份证」是 ADR-0002 的决定，但只对显式授权的端点开放，不是默认字段。
type profileResp struct {
	StudentNo string `json:"studentNo,omitempty"`
	Name      string `json:"name"`
	College   string `json:"college"`
	Major     string `json:"major"`
	ClassName string `json:"className"`
	Grade     string `json:"grade"`
	QQ        string `json:"qq"`
	AvatarURL string `json:"avatarUrl"`
	// IDCardSuffix4 仅在管理台详情端点填充。
	IDCardSuffix4 string `json:"idCardSuffix4,omitempty"`
}

func meHandler(svc *Service, ctxUserID CtxUserIDOf) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := ctxUserID(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"code": string(identity.CodeUnauthorized)})
			return
		}
		p, err := svc.Profile(c.Request.Context(), userID)
		if err != nil {
			writeUserError(c, err)
			return
		}
		c.JSON(http.StatusOK, profileResp{
			Name: p.Name, College: p.College, Major: p.Major,
			ClassName: p.ClassName, Grade: p.Grade, QQ: p.QQ, AvatarURL: p.AvatarURL,
		})
	}
}

type patchReq struct {
	QQ        *string `json:"qq"`
	AvatarURL *string `json:"avatarUrl"`
	// 注意：这里**刻意不接受**姓名、学院、专业等学校档案字段。
	// 客户端提交学校字段一律无效——这是画像分权的落点，不是遗漏。
}

func patchProfileHandler(svc *Service, ctxUserID CtxUserIDOf) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := ctxUserID(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"code": string(identity.CodeUnauthorized)})
			return
		}
		var req patchReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_request"})
			return
		}
		cur, err := svc.Profile(c.Request.Context(), userID)
		if err != nil {
			writeUserError(c, err)
			return
		}
		ex := Extras{QQ: cur.QQ, AvatarURL: cur.AvatarURL}
		if req.QQ != nil {
			ex.QQ = *req.QQ
		}
		if req.AvatarURL != nil {
			ex.AvatarURL = *req.AvatarURL
		}
		p, err := svc.UpdateExtras(c.Request.Context(), userID, ex)
		if err != nil {
			writeUserError(c, err)
			return
		}
		c.JSON(http.StatusOK, profileResp{
			Name: p.Name, College: p.College, Major: p.Major,
			ClassName: p.ClassName, Grade: p.Grade, QQ: p.QQ, AvatarURL: p.AvatarURL,
		})
	}
}

// adminProfileHandler 是管理台按学号读画像。
// 鉴权（必须是管理员）由装配层挂的中间件负责，本 handler 只做取数。
func adminProfileHandler(svc *Service, _ platform.ProfileRepo) gin.HandlerFunc {
	return func(c *gin.Context) {
		studentNo := c.Param("studentNo")
		if studentNo == "" {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_request"})
			return
		}
		brief, err := svc.Brief(c.Request.Context(), studentNo)
		if err != nil {
			if platform.IsNotFound(err) {
				c.JSON(http.StatusNotFound, gin.H{"code": "not_found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"code": string(identity.CodeInternal)})
			return
		}
		c.JSON(http.StatusOK, profileResp{
			StudentNo: brief.StudentNo, Name: brief.Name, College: brief.College,
			Major: brief.Major, ClassName: brief.ClassName, Grade: brief.Grade,
		})
	}
}

func writeUserError(c *gin.Context, err error) {
	code := identity.CodeOf(err)
	status := http.StatusInternalServerError
	switch code {
	case identity.CodeUnauthorized, identity.CodeSessionExpired:
		status = http.StatusUnauthorized
	case identity.CodeForbidden, identity.CodeUserDisabled:
		status = http.StatusForbidden
	}
	c.JSON(status, gin.H{"code": string(code)})
}
