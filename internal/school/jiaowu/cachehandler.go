package jiaowu

import (
	"context"
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
//	GET    /school/jiaowu/cache/:dataType   命中直返；未命中回 200 + code=cache_miss
//	POST   /school/jiaowu/cache/:dataType   回传解析后的 JSON（schema 校验后写入）
//	DELETE /school/jiaowu/cache/:dataType   缓存失效（60 秒间隔）
//
// 与 #37 的结论一致：**不为 14 个功能各建语义端点**。前端要取教务页面时用已有的
// GET /school/address?path=<教务路径> 自己拼——语义端点的信息量与它等价。
//
// 2026-11-17 前缀分层：原为 /school/cache/*，改为 /school/jiaowu/cache/*。
// 理由是 /school/ 下将同时存在 jiaowu 与 secondclass 两个学校侧系统，
// 顶层的 /school/cache 说不出「是谁的缓存」；加 /jiaowu/ 段后归属自明。
// 二课侧对应 /school/secondclass/cache/*，两边形状同构、前缀不同。
func RegisterCache(r gin.IRouter, svc *Service) {
	r.GET("/school/jiaowu/cache/:dataType", cacheGetHandler(svc))
	r.POST("/school/jiaowu/cache/:dataType", cacheSubmitHandler(svc))
	r.DELETE("/school/jiaowu/cache/:dataType", cacheInvalidateHandler(svc))
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

// cacheMissResponse 是未命中的响应：HTTP 200 + 业务码 cache_miss。
//
// 客户端拿到它就按 method 请求 address，自己解析 HTML，再把 JSON 回传。
//
// address 形态取决于请求头 X-Nongyu-Campus-Network：
//   - 校内（direct）：直连教务 URL，后端用已保管的 tgt 换出 ASPSESSIONID 一并下发；
//   - 校外（vpn）：WebVPN 编码地址，**cookies 恒为空**——网关票只能由客户端自己登录网关产生，
//     后端换不到（见 sessionStateClientSide 说明）。
//
// 校外形态额外带回 WebVPN 字段：把「本地票状态」明确告诉客户端。
// 原来只回 session=client_side，客户端分不清「还没登录网关」和「已登录票正常」，
// 也就无法按你的要求「只在票无效/需要登录时弹窗」。现在 code 与 /fetch 端点
// 完全一致（webvpn_ticket_missing / webvpn_ticket_expired），客户端一套逻辑即可。
//
// 诚实边界：cache_miss 零出站，无法验证网关侧是否仍认这张票。webvpn.status=ready
// 只表示「本地保管着一张未被判死的票」，不是「网关保证放行」。
// 真正判死仍靠 /fetch 的 302，或客户端自己请求时遇到的 302。
//
// 无论哪种形态，取不到票时仍是 200，不变成 500。
type cacheMissResponse struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Target  string         `json:"target,omitempty"`
	Address string         `json:"address,omitempty"`
	Method  string         `json:"method,omitempty"`
	Cookies []IssuedCookie `json:"cookies"`
	Parser  string         `json:"parser,omitempty"`
	// Needs / BasePath：缺入参的类型（教室、教师）没有完整地址，
	// 但必须告诉客户端缺什么、骨架路径长什么样，否则它拿到空 address 无从下手。
	Needs string `json:"needs,omitempty"`
	// BasePath 是缺入参时可用的骨架路径（不含 query），客户端自己拼。
	BasePath string `json:"basePath,omitempty"`
	// Mode 是本次判定出的地址形态：direct（直连）或 vpn（经网关编码）。
	Mode string `json:"mode,omitempty"`
	// Session 是会话语义：ok（已下发票）/ unavailable（需要票但没拿到）/ client_side（票由客户端自备）。
	Session string `json:"session,omitempty"`
	// WebVPN 是校外形态的本地票状态信号（direct 形态为 nil，不出现在响应里）。
	WebVPN *WebVPNGateResult `json:"webvpn,omitempty"`
}

// sessionStateUnavailable 表示需要票但没拿到（未接 issuer、无 tgt、换票失败）。
const sessionStateUnavailable = "unavailable"

// sessionStateClientSide 表示票由客户端自备（vpn 形态：后端不参与票价链路）。
//
// 为什么不在后端换网关票：wengine_vpn_ticket 只在网关服务端已登记该会话为已登录时有效，
// 那种票只有客户端真实登录网关才会产生。后端用 tgt 走门户 appid 换票再回投，
// 只能拿到游客票（回投停在 /wui/#/ 的 fragment 上），打编码地址一律 302。
const sessionStateClientSide = "client_side"

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
				c.JSON(http.StatusOK, svc.cacheMiss(c.Request.Context(), userID, dt,
					c.GetHeader(HeaderCampusNetwork)))
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

// cacheMiss 组装未命中 recipe。
//
// 地址形态由请求头 X-Nongyu-Campus-Network 决定：
//
//	direct → 直连教务地址 + ASPSESSIONID（后端用已保管 tgt 换；要求后端能到 jiaowu）
//	vpn    → WebVPN 编码地址，**不下发票**（见下）
//
// 校外形态为什么不下发票：wengine_vpn_ticket 只能在网关**已经登记该会话为已登录**时有效，
// 那种票只有客户端真实登录网关才会产生（回投链停在 /wui/#/ 的 fragment 上，后端拿不到）。
// 所以 vpn 形态只下发编码地址，由客户端用自己 WebView 里的网关会话去访问；
// 客户端没有会话时，网关会 302 到登录页，客户端据此重新登录并自己重试。
//
// 换票失败时**仍回 200**，只是 cookies 为空、session=unavailable。
// 不把 issuer 的错误文本写进响应或日志：那里可能带凭据前缀。
func (s *Service) cacheMiss(ctx context.Context, userID string, dt DataType, declared string) cacheMissResponse {
	mode := accessModeOf(declared)
	recipe := recipeOf(dt, mode)
	resp := cacheMissResponse{
		Code:     "cache_miss",
		Message:  "没有可用缓存",
		Target:   string(school.TargetJiaowu),
		Address:  recipe.Address,
		Method:   http.MethodGet,
		Cookies:  []IssuedCookie{},
		Parser:   recipe.Parser,
		Needs:    recipe.Needs,
		BasePath: recipe.BasePath,
		Mode:     string(mode),
		Session:  sessionStateUnavailable,
	}

	if mode == AccessVPN {
		// 校外形态：后端不参与票价链路，只给编码地址。
		//
		// 缺陷修复（2026-11-18）：原来这里无条件回 session=client_side，
		// 客户端拿不到「没交票」的信号，弹窗触发条件没有信号源。
		// 现在查本地票状态并附带 webvpn 字段，code 与 /fetch 完全同码。
		gate := s.inspectWebVPNTicket(ctx, userID)
		resp.WebVPN = &gate
		resp.Session = sessionStateClientSide
		// 审计按状态分流，便于观测「多少用户卡在没交票」。
		switch gate.Status {
		case WebVPNTicketReady:
			s.audit(ctx, userID, "cache_miss", "vpn_ticket_ready", string(school.TargetWebvpn))
		case WebVPNTicketExpired:
			s.audit(ctx, userID, "cache_miss", "vpn_ticket_expired", string(school.TargetWebvpn))
		case WebVPNTicketMissing:
			s.audit(ctx, userID, "cache_miss", "vpn_ticket_missing", string(school.TargetWebvpn))
		default:
			s.audit(ctx, userID, "cache_miss", "vpn_recipe_client_side", string(school.TargetWebvpn))
		}
		return resp
	}

	// 校内形态：拿教务会话票。
	if s.sessions == nil {
		s.audit(ctx, userID, "cache_miss", "session_unavailable", string(school.TargetJiaowu))
		return resp
	}
	cookies, err := s.sessions.IssueSession(ctx, userID)
	if err != nil || len(cookies) == 0 {
		s.audit(ctx, userID, "cache_miss", "session_unavailable", string(school.TargetJiaowu))
		return resp
	}
	resp.Cookies = cookies
	resp.Session = "ok"
	s.audit(ctx, userID, "cache_miss", "session_ok", string(school.TargetJiaowu))
	return resp
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
