package jiaowu

// 取数账号的管理员接口（挂 adminGroup：ScopeAdmin + RoleAdmin）。
//
// 明文凭据只在 Create/Update 的请求体里出现一次；所有响应、日志、审计
// 都不含 tgt / cookieHeader / vpnTicket。
// 公共缓存失效也在这里：只有管理员能删全局键。

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// FetchAccountAdminStore 是管理端依赖的仓储接口，PG/内存实现都在本域 store/ 子包。
// 导出给装配层声明变量用。
type FetchAccountAdminStore interface {
	List(ctx context.Context) ([]FetchAccount, error)
	Create(ctx context.Context, a FetchAccount) (int64, error)
	Update(ctx context.Context, a FetchAccount) error
	Delete(ctx context.Context, id int64) error
	SetEnabled(ctx context.Context, id int64, enabled bool) error
}

// RegisterFetchAccountAdmin 挂载取数账号与公共缓存失效的管理端点。
func RegisterFetchAccountAdmin(r gin.IRouter, store FetchAccountAdminStore, svc *Service) {
	r.GET("/admin/jiaowu/fetch-accounts", listFetchAccounts(store))
	r.POST("/admin/jiaowu/fetch-accounts", createFetchAccount(store))
	r.PUT("/admin/jiaowu/fetch-accounts/:id", updateFetchAccount(store))
	r.DELETE("/admin/jiaowu/fetch-accounts/:id", deleteFetchAccount(store))
	r.PATCH("/admin/jiaowu/fetch-accounts/:id", setFetchAccountEnabled(store))
	r.DELETE("/admin/jiaowu/public-cache/:dataType", invalidatePublicCache(svc))
}

// fetchAccountInput 是新增/更新的请求体。明文凭据仅在入参出现。
type fetchAccountInput struct {
	Label        string `json:"label"`
	TGT          string `json:"tgt"`
	CookieHeader string `json:"cookieHeader"`
	VPNTicket    string `json:"vpnTicket"`
}

func (in fetchAccountInput) validate() error {
	if strings.TrimSpace(in.TGT) == "" {
		return errFetchAccountBadInput("tgt 不能为空")
	}
	if strings.TrimSpace(in.VPNTicket) == "" {
		return errFetchAccountBadInput("vpnTicket 不能为空")
	}
	if !LooksLikeWebVPNTicket(strings.TrimSpace(in.VPNTicket)) {
		return errFetchAccountBadInput("vpnTicket 形态不合法")
	}
	return nil
}

type errFetchAccountBadInput string

func (e errFetchAccountBadInput) Error() string { return string(e) }

// fetchAccountView 是响应体：绝不含凭据。
type fetchAccountView struct {
	ID      int64  `json:"id"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
}

func viewOf(a FetchAccount) fetchAccountView {
	return fetchAccountView{ID: a.ID, Label: a.Label, Enabled: a.Enabled}
}

func listFetchAccounts(store FetchAccountAdminStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := store.List(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse{Code: "internal_error", Message: "读取取数账号失败"})
			return
		}
		out := make([]fetchAccountView, 0, len(rows))
		for _, a := range rows {
			out = append(out, viewOf(a))
		}
		c.JSON(http.StatusOK, gin.H{"accounts": out})
	}
}

func createFetchAccount(store FetchAccountAdminStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in fetchAccountInput
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Code: "bad_request", Message: "请求体非法"})
			return
		}
		if err := in.validate(); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Code: "bad_request", Message: err.Error()})
			return
		}
		id, err := store.Create(c.Request.Context(), FetchAccount{
			Label:        strings.TrimSpace(in.Label),
			TGT:          strings.TrimSpace(in.TGT),
			CookieHeader: strings.TrimSpace(in.CookieHeader),
			VPNTicket:    strings.TrimSpace(in.VPNTicket),
			Enabled:      true,
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse{Code: "internal_error", Message: "创建失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"id": id})
	}
}

func parseAccountID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, errorResponse{Code: "bad_request", Message: "id 非法"})
		return 0, false
	}
	return id, true
}

func updateFetchAccount(store FetchAccountAdminStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseAccountID(c)
		if !ok {
			return
		}
		var in fetchAccountInput
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Code: "bad_request", Message: "请求体非法"})
			return
		}
		if err := in.validate(); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Code: "bad_request", Message: err.Error()})
			return
		}
		if err := store.Update(c.Request.Context(), FetchAccount{
			ID:           id,
			Label:        strings.TrimSpace(in.Label),
			TGT:          strings.TrimSpace(in.TGT),
			CookieHeader: strings.TrimSpace(in.CookieHeader),
			VPNTicket:    strings.TrimSpace(in.VPNTicket),
		}); err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse{Code: "internal_error", Message: "更新失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"updated": true})
	}
}

func deleteFetchAccount(store FetchAccountAdminStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseAccountID(c)
		if !ok {
			return
		}
		if err := store.Delete(c.Request.Context(), id); err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse{Code: "internal_error", Message: "删除失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": true})
	}
}

func setFetchAccountEnabled(store FetchAccountAdminStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseAccountID(c)
		if !ok {
			return
		}
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.Enabled == nil {
			c.JSON(http.StatusBadRequest, errorResponse{Code: "bad_request", Message: "需要 enabled 布尔值"})
			return
		}
		if err := store.SetEnabled(c.Request.Context(), id, *body.Enabled); err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse{Code: "internal_error", Message: "启停失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"enabled": *body.Enabled})
	}
}

// invalidatePublicCache 只有管理员能删公共缓存全局键。
func invalidatePublicCache(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		dt, ok := ParseDataType(c.Param("dataType"))
		if !ok {
			c.JSON(http.StatusBadRequest, errorResponse{Code: "target_denied", Message: "不支持的 dataType"})
			return
		}
		if !IsPublicDataType(dt) {
			c.JSON(http.StatusBadRequest, errorResponse{Code: "bad_request", Message: "该类型不是公共服务，请用普通失效端点"})
			return
		}
		if err := svc.InvalidatePublicCache(c.Request.Context(), dt); err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse{Code: "internal_error", Message: "失效失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"invalidated": true, "dataType": string(dt)})
	}
}
