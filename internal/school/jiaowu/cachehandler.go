package jiaowu

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/school"
)

// RegisterCache 挂载回传缓存子域的三条路由（#40/#34）。
//
//	GET    /school/cache/:dataType          命中直返；未命中回 200 + code=cache_miss
//	POST   /school/cache/:dataType          回传解析后的 JSON（schema 校验后写入）
//	DELETE /school/cache/:dataType          缓存失效（60 秒间隔）
//
// 与 #37 的结论一致：**不为 14 个功能各建语义端点**。前端要取教务页面时用已有的
// GET /school/address?path=<教务路径> 自己拼——语义端点的信息量与它等价。
func RegisterCache(r gin.IRouter, svc *Service) {
	r.GET("/school/cache/:dataType", cacheGetHandler(svc))
	r.POST("/school/cache/:dataType", cacheSubmitHandler(svc))
	r.DELETE("/school/cache/:dataType", cacheInvalidateHandler(svc))
}

// cacheEnvelope 是成功响应的信封（#40 + #41 Q4 + #32 Q4 三层叠加后的最终形状）。
//
// Payload 用 json.RawMessage 原样透出：缓存里存的就是前端回传的那段 JSON，
// 而且它可能是数组、对象或 null（15 类里三种形状都有），
// 用 map 接会丢掉数组形态，再解一次再编回去也只是白白改变字段顺序。
type cacheEnvelope struct {
	Payload       json.RawMessage `json:"payload"`
	Success       *bool           `json:"success,omitempty"`
	SchemaVersion int             `json:"schemaVersion"`
	FetchedAt     string          `json:"fetchedAt"`
	Stale         bool            `json:"stale"`
}

// cacheMissResponse 是未命中的响应：HTTP 200 + 业务码 cache_miss（#40 已定）。
//
// 用它而不是 404，是为了让「没有缓存」看起来是正常流程的一步而不是错误——
// 客户端拿到它就该去直连教务取数，然后回传。
type cacheMissResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Address 是顺带给出的编码地址，省掉客户端再调一次 /school/address。
	Address string `json:"address,omitempty"`
}

// dataTypeFromPath 从 URL 取并校验 dataType。
//
// 只认受控词表里的 15 个名字（#41）。未知名直接 400，绝不把用户输入当缓存键的一段拼进去。
func dataTypeFromPath(c *gin.Context) (DataType, bool) {
	dt, ok := ParseDataType(c.Param("dataType"))
	if !ok {
		c.JSON(http.StatusBadRequest, errorResponse{
			Code:    "target_denied",
			Message: "不支持的 dataType",
		})
		return "", false
	}
	return dt, true
}

// requireUserID 取认证上下文里的 userID。
// 身份只来自中间件写入的上下文，永不读请求体或 query 里的 userID（#40）。
func requireUserID(c *gin.Context) (string, bool) {
	userID := c.GetString(school.CtxUserID)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, errorResponse{Code: "unauthorized", Message: "缺少已认证身份"})
		return "", false
	}
	return userID, true
}

// cacheGetHandler 处理命中直返。
//
// fallback=1 表示「客户端刚才直连教务失败了，请把旧数据给我」——只有这种调用会返回
// stale=true（#32 把 stale 的触发方限定为客户端上报失败）。
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
				// 未命中：顺带把编码地址一同下发，客户端可以直接去取数（#40）。
				c.JSON(http.StatusOK, cacheMissResponse{
					Code:    "cache_miss",
					Message: "没有可用缓存",
					Address: addressForJiaowu(c, svc, userID),
				})
				return
			}
			writeCacheError(c, err)
			return
		}
		c.JSON(http.StatusOK, envelopeOf(snap))
	}
}

// cacheSubmitHandler 接收前端回传的 JSON。
//
// 体上限 1 MiB（#40）：用 MaxBytesReader 在读取层就截断，不先读进内存再判长度。
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
			c.JSON(http.StatusRequestEntityTooLarge, errorResponse{
				Code:    "payload_too_large",
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

// cacheInvalidateHandler 让本用户该类型的缓存失效（#34）。
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

// writeCacheError 把缓存子域的错误映射成 HTTP 响应。
//
// 状态码依据 #39 的两张表：schema_rejected 是调用方数据问题（400）、
// 刷新过频是限流（429）、缓存层未配置是服务不可用（503）。
func writeCacheError(c *gin.Context, err error) {
	var rejected *SchemaRejectedError
	switch {
	case errors.As(err, &rejected):
		// 只回统一文案：具体哪个字段不符合属内部细节，进日志不进响应（#39 Q4）。
		c.JSON(http.StatusBadRequest, errorResponse{
			Code:    "schema_rejected",
			Message: "回传体形状校验未通过",
		})
	case errors.Is(err, ErrRefreshTooSoon):
		c.Header("Retry-After", strconv.Itoa(RefreshRetryAfter()))
		c.JSON(http.StatusTooManyRequests, errorResponse{
			Code:    "refresh_too_soon",
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

// addressForJiaowu 尽力给出教务的编码地址，供 cache_miss 一并下发。
//
// 取不到就返回空串：地址只是便利项，拿不到不该让 cache_miss 变成错误。
func addressForJiaowu(c *gin.Context, svc *Service, userID string) string {
	res, err := svc.Address(c.Request.Context(), userID, school.TargetJiaowu, "")
	if err != nil {
		return ""
	}
	return res.Address
}

// envelopeOf 把缓存快照翻成对外信封。
func envelopeOf(snap CacheSnapshot) cacheEnvelope {
	return cacheEnvelope{
		Payload:       snap.Payload,
		Success:       snap.Success,
		SchemaVersion: snap.SchemaVersion,
		FetchedAt:     snap.FetchedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		Stale:         snap.Stale,
	}
}
