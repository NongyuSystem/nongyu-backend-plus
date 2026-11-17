// Package secondclass 的读三端点：GET/POST/DELETE /school/secondclass/cache/:dataType。
//
// 形状与教务 RegisterCache 同构、前缀不同（规格 §6.1）；但响应体按二课自己的契约：
//   - 命中信封沿用教务字段名（payload/schemaVersion/fetchedAt/stale），不新增；
//   - 未命中是 {code:"cache_miss", message, recipe:{...}}，**不照抄**教务的
//     Address/Method/Cookies/Parser/Needs/BasePath/Mode/Session 字段集（探索笔记「不要照抄」第 1 条）。
package secondclass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/school"
)

// RegisterCache 挂载二课读子域的三条路由。由 app 装配层在缓存就绪后调用；
// 认证由全局中间件链统一负责，业务 handler 不自行鉴权（端点不进 publicPaths）。
func RegisterCache(r gin.IRouter, svc *Service) {
	r.GET("/school/secondclass/cache/:dataType", cacheGetHandler(svc))
	r.POST("/school/secondclass/cache/:dataType", cacheSubmitHandler(svc))
	r.DELETE("/school/secondclass/cache/:dataType", cacheInvalidateHandler(svc))
}

// errorResponse 是错误响应：统一错误码 + 可读信息（规格 §3.1）。
type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// cacheEnvelope 是命中响应的信封（规格 §6.2）。
//
// Payload 用 json.RawMessage 原样透出：它可能是数组、单对象或 null，
// 用 map 接会丢掉数组形态，重新序列化也只会白白改变字段顺序。
// 字段名沿用教务侧既有形状，不新增。
type cacheEnvelope struct {
	Payload       json.RawMessage `json:"payload"`
	SchemaVersion int             `json:"schemaVersion"`
	FetchedAt     string          `json:"fetchedAt"`
	Stale         bool            `json:"stale"`
}

// cacheMissResponse 是未命中的响应：HTTP 200 + 业务码 cache_miss + 完整配方。
// 客户端拿到它就照 recipe 直连二课平台取数（后端零出站），拿到后自愿回传。
type cacheMissResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Recipe  Recipe `json:"recipe"`
}

// dataTypeFromPath 从 URL 取并校验 dataType。
//
// 只认受控词表里的 12 个名字（规格 §6.8）。未知名直接 400 unknown_datatype，
// 绝不把用户输入当缓存键的一段拼进去（验收标准第 6 条）。
func dataTypeFromPath(c *gin.Context) (DataType, bool) {
	dt, ok := ParseDataType(c.Param("dataType"))
	if !ok {
		c.JSON(http.StatusBadRequest, errorResponse{
			Code:    "unknown_datatype",
			Message: "不支持的 dataType",
		})
		return "", false
	}
	return dt, true
}

// requireUserID 取认证上下文里的 userID。
// 身份只来自中间件写入的上下文，永不读请求体或 query 里的 userID（规格 §3.3）。
func requireUserID(c *gin.Context) (string, bool) {
	userID := c.GetString(school.CtxUserID)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, errorResponse{Code: "unauthorized", Message: "缺少已认证身份"})
		return "", false
	}
	return userID, true
}

// cacheGetHandler 处理命中直返 / 未命中下发配方。
//
// fallback=1 表示「客户端刚才直连二课失败了，请把旧数据给我」——只有这种调用会返回
// stale=true。配方在 miss 时同一次往返捎带（规格 §6.1）。
func cacheGetHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		dt, ok := dataTypeFromPath(c)
		if !ok {
			return
		}
		userID, ok := requireUserID(c)
		if !ok {
			return
		}

		fallback := c.Query("fallback") == "1"
		snap, err := svc.CacheGet(c.Request.Context(), userID, dt, fallback)
		if err != nil {
			if errors.Is(err, ErrCacheMiss) {
				miss, missErr := svc.cacheMiss(c.Request.Context(), userID, dt)
				if missErr != nil {
					writeCacheError(c, missErr)
					return
				}
				c.JSON(http.StatusOK, miss)
				return
			}
			writeCacheError(c, err)
			return
		}
		c.JSON(http.StatusOK, envelopeOf(snap))
	}
}

// cacheSubmitHandler 接收客户端自愿回传的 JSON 副本。
//
// 体上限 1 MiB（规格 §6.12）：用 MaxBytesReader 在读取层就截断，不先读进内存再判长度。
// 超限或 schema 不合格都映射成 400 invalid_payload，且旧数据不受影响。
func cacheSubmitHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		dt, ok := dataTypeFromPath(c)
		if !ok {
			return
		}
		userID, ok := requireUserID(c)
		if !ok {
			return
		}

		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSubmitBytes)
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			// MaxBytesReader 超限会在这里报错。
			svc.audit(c.Request.Context(), userID, "secondclass.record", "payload_too_large", string(dt))
			c.JSON(http.StatusBadRequest, errorResponse{
				Code:    "invalid_payload",
				Message: "回传体超过 1 MiB",
			})
			return
		}

		snap, err := svc.CacheSubmit(c.Request.Context(), userID, dt, body)
		if err != nil {
			writeCacheError(c, err)
			return
		}
		c.JSON(http.StatusOK, envelopeOf(snap))
	}
}

// cacheInvalidateHandler 让本用户该类型的缓存失效（60 秒节流）。
func cacheInvalidateHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		dt, ok := dataTypeFromPath(c)
		if !ok {
			return
		}
		userID, ok := requireUserID(c)
		if !ok {
			return
		}
		if err := svc.CacheInvalidate(c.Request.Context(), userID, dt); err != nil {
			writeCacheError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"invalidated": true})
	}
}

// writeCacheError 把缓存子域的错误映射成 HTTP 响应（规格 §3.2 表 A）。
//
// 注意：schema/超限都归 invalid_payload（教务是 schema_rejected，二课按规格换掉）；
// 节流是 rate_limited（教务是 refresh_too_soon）。
func writeCacheError(c *gin.Context, err error) {
	var invalid *InvalidPayloadError
	switch {
	case errors.As(err, &invalid):
		// 只回统一文案：具体哪个字段不符合属内部细节，进日志不进响应（规格 §8.1）。
		c.JSON(http.StatusBadRequest, errorResponse{
			Code:    "invalid_payload",
			Message: "回传体超限或形状校验未通过",
		})
	case errors.Is(err, ErrRefreshTooSoon):
		c.Header("Retry-After", strconv.Itoa(RefreshRetryAfter()))
		c.JSON(http.StatusTooManyRequests, errorResponse{
			Code:    "rate_limited",
			Message: "刷新过于频繁，请稍后再试",
		})
	case errors.Is(err, ErrCacheUnavailable):
		c.JSON(http.StatusServiceUnavailable, errorResponse{
			Code:    "cache_unavailable",
			Message: "缓存服务未启用",
		})
	default:
		c.JSON(http.StatusInternalServerError, errorResponse{
			Code:    "internal_error",
			Message: "内部错误",
		})
	}
}

// ErrRecipeMissing 表示某个受控 dataType 竟然取不到读配方——这是内部数据不一致
// （词表与配方表漂移），不是客户端错误。cacheMiss 用它做 fail-closed：绝不下发空配方。
var ErrRecipeMissing = errors.New("secondclass: 受控 dataType 缺少读配方")

// cacheMiss 组装未命中响应（200 + code=cache_miss + 完整配方）。
//
// 配方是包级静态表数据，不依赖缓存层是否可用，也不做任何出站；下发这件事本身记审计
// secondclass.recipe_issue（规格 §3.4 审计 action 表）。
//
// fail-closed（code-review BLOCKER-1）：RecipeFor 的 ok **不再丢弃**。「dt 已过白名单」
// 只能说明它在 dataTypeSpecs 里，不能保证它在 pageRecipes 里（历史上 B 组 24 个
// dataType 正因这句错误注释被下发了空配方）。缺配方时记 Error 日志并返回错误，
// 由 handler 走 writeCacheError 映射成 500 internal_error，绝不把空 id/path 的
// 配方发给客户端。返回签名带 error 就是为了把这条失败路径显式交给调用方。
func (s *Service) cacheMiss(ctx context.Context, userID string, dt DataType) (cacheMissResponse, error) {
	recipe, ok := RecipeFor(dt)
	if !ok {
		s.logger.Error("受控 dataType 缺少读配方，回 internal_error（fail-closed，不下发空配方）",
			"dataType", string(dt), "userID", userID)
		return cacheMissResponse{}, fmt.Errorf("%w: %q", ErrRecipeMissing, dt)
	}
	s.audit(ctx, userID, "secondclass.recipe_issue", "ok", string(dt))
	return cacheMissResponse{
		Code:    "cache_miss",
		Message: "没有可用缓存，请按配方取数后自愿回传",
		Recipe:  recipe,
	}, nil
}

// envelopeOf 把缓存快照翻成对外信封。
func envelopeOf(snap CacheSnapshot) cacheEnvelope {
	return cacheEnvelope{
		Payload:       snap.Payload,
		SchemaVersion: snap.SchemaVersion,
		FetchedAt:     snap.FetchedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		Stale:         snap.Stale,
	}
}
