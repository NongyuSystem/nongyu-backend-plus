package apikey

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/apperr"
)

// RegisterPublic 挂载「取用密钥」端点。
//
// **它需要会话**（由装配层挂鉴权组），不能公开：否则任何人都能无限取 key，
// 「不硬编码」就退化成「公开分发」。
func RegisterPublic(r gin.IRouter, svc *Service) {
	r.POST("/keys/amap/acquire", acquireHandler(svc))
}

// RegisterAdmin 挂载管理端 CRUD。
func RegisterAdmin(r gin.IRouter, svc *Service) {
	r.GET("/admin/keys/amap", adminListHandler(svc))
	r.POST("/admin/keys/amap", adminCreateHandler(svc))
	r.GET("/admin/keys/amap/:id", adminGetHandler(svc))
	r.PATCH("/admin/keys/amap/:id", adminUpdateHandler(svc))
	r.DELETE("/admin/keys/amap/:id", adminDeleteHandler(svc))
}

// keyView 是管理端看到的密钥形状。
//
// **刻意不含明文与密文**，只有后四位提示。管理员也不需要看到完整 key：
// 要换就直接覆盖，要看就去高德控制台看。
type keyView struct {
	ID         int64      `json:"id"`
	Platform   string     `json:"platform"`
	Hint       string     `json:"hint"`
	QPSLimit   int        `json:"qpsLimit"`
	Enabled    bool       `json:"enabled"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	UsedCount  int64      `json:"usedCount"`
	CreatedAt  time.Time  `json:"createdAt"`
}

func toKeyView(k *Key) keyView {
	return keyView{
		ID: k.ID, Platform: string(k.Platform), Hint: k.Hint,
		QPSLimit: k.QPSLimit, Enabled: k.Enabled,
		LastUsedAt: k.LastUsedAt, UsedCount: k.UsedCount, CreatedAt: k.CreatedAt,
	}
}

// acquireHandler 为客户端取一把 key。
//
// 响应里只给明文 key 与后四位提示；不给池的内部结构（ID/密文）。
func acquireHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in struct {
			Platform string `json:"platform"`
		}
		// 允许用 query 传平台（GET 语义更自然，但这里保留 POST 以免 key
		// 出现在 URL/日志里——虽然 key 在响应体里，平台在 URL 也无妨，
		// 统一走 POST 更简单）。
		if err := c.ShouldBindJSON(&in); err != nil && err.Error() != "EOF" {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "请求体非法"))
			return
		}
		if in.Platform == "" {
			in.Platform = c.Query("platform")
		}
		res, err := svc.Acquire(c.Request.Context(), in.Platform)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"platform": string(res.Platform),
			"key":      res.Key,
			"hint":     res.Hint,
		})
	}
}

type keyRequest struct {
	Platform string `json:"platform"`
	// Key 是明文。创建必填；更新留空表示不改。
	Key      string `json:"key"`
	QPSLimit int    `json:"qpsLimit"`
	Enabled  *bool  `json:"enabled"`
}

func adminListHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		list, err := svc.AdminList(c.Request.Context(), c.Query("platform"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		out := make([]keyView, 0, len(list))
		for _, k := range list {
			out = append(out, toKeyView(k))
		}
		c.JSON(http.StatusOK, gin.H{"items": out})
	}
}

func adminCreateHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in keyRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "请求体非法"))
			return
		}
		p, ok := ParsePlatform(in.Platform)
		if !ok {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "平台取值非法"))
			return
		}
		k, err := svc.Create(c.Request.Context(), KeyInput{
			Platform: p, Plain: in.Key, QPSLimit: in.QPSLimit, Enabled: in.Enabled,
		})
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": toKeyView(k)})
	}
}

func adminGetHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		k, err := svc.AdminGet(c.Request.Context(), id)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toKeyView(k)})
	}
}

func adminUpdateHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		var in keyRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "请求体非法"))
			return
		}
		k, err := svc.Update(c.Request.Context(), id, KeyInput{
			Plain: in.Key, QPSLimit: in.QPSLimit, Enabled: in.Enabled,
		})
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toKeyView(k)})
	}
}

func adminDeleteHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		if err := svc.Delete(c.Request.Context(), id); err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

func parseID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.Errorf(apperr.CodeValidationFailed, "id 非法")
	}
	return id, nil
}
