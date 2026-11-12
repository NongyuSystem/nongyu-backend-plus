package announcement

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/content"
)

// ViewerFunc 从请求上下文构造「正在看公告的人」。
//
// 为什么由装配层注入而不是本包直接读 context：读者视图需要会话身份、
// 角色，以及（将来）学籍画像——这些分别属于 server 与身份域。内容域若直接
// import 它们，就违反了「业务域只依赖 platform 接口、不互相 import」
// （README 架构约束第 1、2 条）。这与 identity/user 的 Register 接收
// currentUserID 提取函数是同一手法。
//
// 返回零值 Viewer（Authenticated=false）表示匿名读者：只能看到「全员」公告。
type ViewerFunc func(*gin.Context) Viewer

// RegisterPublic 挂载读者侧的三条只读路由。
//
// 它们都在 Spec 的「公开读 5 端点」清单里：无需会话即可访问，
// 但**仍受限流约束**（限流豁免只有 /healthz）。
func RegisterPublic(r gin.IRouter, svc *Service, viewerOf ViewerFunc) {
	if viewerOf == nil {
		viewerOf = func(*gin.Context) Viewer { return Viewer{} }
	}
	r.GET("/announcements", listHandler(svc, viewerOf))
	// 详情路由同时承担 /announcements/latest：
	//
	// 把 latest 做成 :id 的一个特殊取值，而不是并列注册
	// `GET /announcements/latest`，是为了避开 gin 路由树里「同一层级既有
	// 静态段又有参数段」的注册顺序依赖。对外路径语义不变，ipmort 更少。
	r.GET("/announcements/:id", getOrLatestHandler(svc, viewerOf))
}

// RegisterAdmin 挂载管理端四条路由。
//
// 调用方（装配层）负责把这个 Register 挂在一个已经套了
// RevocableAuthMiddleware(ScopeAdmin) + server.AdminOnly 的路由组上——
// 本包不重复做鉴权，避免「两处都判、口径还可能不一致」。
func RegisterAdmin(r gin.IRouter, svc *Service) {
	r.GET("/admin/announcements", adminListHandler(svc))
	r.POST("/admin/announcements", adminCreateHandler(svc))
	r.PATCH("/admin/announcements/:id", adminUpdateHandler(svc))
	r.DELETE("/admin/announcements/:id", adminDeleteHandler(svc))
}

// ---------- 响应体 ----------

// publicView 是读者侧看到的公告形状。
//
// 刻意不含 status / publishRevision / audience：草稿与撤回对读者不可见，
// 受众条件也不应把「这条只发给某些人」暴露给读者。
type publicView struct {
	ID            int64      `json:"id"`
	Title         string     `json:"title"`
	Body          string     `json:"body"`
	PublishedAt   *time.Time `json:"publishedAt"`
	EffectiveFrom *time.Time `json:"effectiveFrom"`
	ExpiresAt     *time.Time `json:"expiresAt"`
	Pinned        bool       `json:"pinned"`
}

// adminView 是管理端看到的公告形状，多出状态、修订号与受众。
type adminView struct {
	ID              int64        `json:"id"`
	Title           string       `json:"title"`
	Body            string       `json:"body"`
	Status          string       `json:"status"`
	PublishedAt     *time.Time   `json:"publishedAt"`
	EffectiveFrom   *time.Time   `json:"effectiveFrom"`
	ExpiresAt       *time.Time   `json:"expiresAt"`
	Pinned          bool         `json:"pinned"`
	PublishRevision int          `json:"publishRevision"`
	Audience        audienceView `json:"audience"`
}

type audienceView struct {
	Scope   string `json:"scope"`
	Roles   []int  `json:"roles,omitempty"`
	College string `json:"college,omitempty"`
	Grade   string `json:"grade,omitempty"`
}

func toPublic(a *Announcement) publicView {
	return publicView{
		ID: a.ID, Title: a.Title, Body: a.Body,
		PublishedAt: a.PublishedAt, EffectiveFrom: a.EffectiveFrom,
		ExpiresAt: a.ExpiresAt, Pinned: a.Pinned,
	}
}

func toAdmin(a *Announcement) adminView {
	return adminView{
		ID: a.ID, Title: a.Title, Body: a.Body, Status: string(a.Status),
		PublishedAt: a.PublishedAt, EffectiveFrom: a.EffectiveFrom,
		ExpiresAt: a.ExpiresAt, Pinned: a.Pinned,
		PublishRevision: a.PublishRevision,
		Audience: audienceView{
			Scope: string(a.Audience.Scope), Roles: a.Audience.Roles,
			College: a.Audience.College, Grade: a.Audience.Grade,
		},
	}
}

// listResponse 是读者侧列表响应。信封只带 code（成功时为空且在 JSON 里省略），
// 数据放 items —— 与仓库既有端点「直接回业务字段」的风格一致。
type listResponse struct {
	Items []publicView `json:"items"`
	Next  string       `json:"next,omitempty"`
}

type adminListResponse struct {
	Items []adminView `json:"items"`
	Next  string      `json:"next,omitempty"`
}

// ---------- 读者侧 handler ----------

func listHandler(svc *Service, viewerOf ViewerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		after, err := decodeCursor(c.Query("cursor"))
		if err != nil {
			writeError(c, err)
			return
		}
		limit, err := parseLimit(c.Query("limit"))
		if err != nil {
			writeError(c, err)
			return
		}

		res, err := svc.List(c.Request.Context(), viewerOf(c), after, limit)
		if err != nil {
			writeError(c, err)
			return
		}

		out := listResponse{Items: make([]publicView, 0, len(res.Items))}
		for _, a := range res.Items {
			out.Items = append(out.Items, toPublic(a))
		}
		if res.Next != nil {
			out.Next = encodeCursor(*res.Next)
		}
		c.JSON(http.StatusOK, out)
	}
}

func getOrLatestHandler(svc *Service, viewerOf ViewerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.Param("id")
		v := viewerOf(c)

		if raw == "latest" {
			a, err := svc.Latest(c.Request.Context(), v)
			if err != nil {
				writeError(c, err)
				return
			}
			// 没有符合条件的公告时回 data:null 而不是 404：
			// 「今天没有公告」是正常状态，不是错误（Spec 的验收第 4.1.4 条）。
			if a == nil {
				c.JSON(http.StatusOK, gin.H{"data": nil})
				return
			}
			c.JSON(http.StatusOK, gin.H{"data": toPublic(a)})
			return
		}

		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeError(c, content.Errorf(content.CodeValidationFailed, "公告 id 非法"))
			return
		}
		a, err := svc.Get(c.Request.Context(), v, id)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toPublic(a)})
	}
}

// ---------- 管理端 handler ----------

func adminListHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		after, err := decodeCursor(c.Query("cursor"))
		if err != nil {
			writeError(c, err)
			return
		}
		limit, err := parseLimit(c.Query("limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		res, err := svc.AdminList(c.Request.Context(), after, limit)
		if err != nil {
			writeError(c, err)
			return
		}
		out := adminListResponse{Items: make([]adminView, 0, len(res.Items))}
		for _, a := range res.Items {
			out.Items = append(out.Items, toAdmin(a))
		}
		if res.Next != nil {
			out.Next = encodeCursor(*res.Next)
		}
		c.JSON(http.StatusOK, out)
	}
}

// createRequest 是管理端创建入参。字段名用 camelCase（Spec 的信封约定）。
type createRequest struct {
	Title         string       `json:"title"`
	Body          string       `json:"body"`
	EffectiveFrom *time.Time   `json:"effectiveFrom"`
	ExpiresAt     *time.Time   `json:"expiresAt"`
	Pinned        bool         `json:"pinned"`
	Audience      audienceView `json:"audience"`
	Publish       bool         `json:"publish"`
}

func adminCreateHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in createRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			writeError(c, content.Errorf(content.CodeValidationFailed, "请求体非法"))
			return
		}
		aud, err := toAudience(in.Audience)
		if err != nil {
			writeError(c, err)
			return
		}
		a, err := svc.Create(c.Request.Context(), CreateInput{
			Title: in.Title, Body: in.Body,
			EffectiveFrom: in.EffectiveFrom, ExpiresAt: in.ExpiresAt,
			Pinned: in.Pinned, Audience: aud, Publish: in.Publish,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": toAdmin(a)})
	}
}

// updateRequest 用指针区分「没传」与「传了空」。
type updateRequest struct {
	Title              *string       `json:"title"`
	Body               *string       `json:"body"`
	EffectiveFrom      *time.Time    `json:"effectiveFrom"`
	ClearEffectiveFrom bool          `json:"clearEffectiveFrom"`
	ExpiresAt          *time.Time    `json:"expiresAt"`
	ClearExpiresAt     bool          `json:"clearExpiresAt"`
	Pinned             *bool         `json:"pinned"`
	Audience           *audienceView `json:"audience"`
	// Action 是状态机动作：publish / revoke。为空表示只改字段。
	Action string `json:"action"`
}

func adminUpdateHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		var in updateRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			writeError(c, content.Errorf(content.CodeValidationFailed, "请求体非法"))
			return
		}

		var aud *Audience
		if in.Audience != nil {
			conv, cerr := toAudience(*in.Audience)
			if cerr != nil {
				writeError(c, cerr)
				return
			}
			aud = &conv
		}

		// 先改字段，再驱动状态机：这样「改完内容立刻发布」是一次请求
		// 就能完成的两步，且失败时不会出现「发布成功但内容没改」。
		var a *Announcement
		if in.Title != nil || in.Body != nil || in.EffectiveFrom != nil ||
			in.ClearEffectiveFrom || in.ExpiresAt != nil || in.ClearExpiresAt ||
			in.Pinned != nil || aud != nil {
			a, err = svc.Update(c.Request.Context(), id, UpdateInput{
				Title: in.Title, Body: in.Body,
				EffectiveFrom: in.EffectiveFrom, ClearEffectiveFrom: in.ClearEffectiveFrom,
				ExpiresAt: in.ExpiresAt, ClearExpiresAt: in.ClearExpiresAt,
				Pinned: in.Pinned, Audience: aud,
			})
			if err != nil {
				writeError(c, err)
				return
			}
		}

		switch in.Action {
		case "":
			if a == nil {
				writeError(c, content.Errorf(content.CodeValidationFailed, "没有任何要变更的内容"))
				return
			}
		case "publish":
			a, err = svc.Publish(c.Request.Context(), id)
		case "revoke":
			a, err = svc.Revoke(c.Request.Context(), id)
		default:
			writeError(c, content.Errorf(content.CodeValidationFailed, "未知动作"))
			return
		}
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toAdmin(a)})
	}
}

func adminDeleteHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		if err := svc.Delete(c.Request.Context(), id); err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

// ---------- 工具 ----------

func parseID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, content.Errorf(content.CodeValidationFailed, "id 非法")
	}
	return id, nil
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return 0, nil // Service 会用默认值
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, content.Errorf(content.CodeValidationFailed, "limit 非法")
	}
	return n, nil
}

func toAudience(v audienceView) (Audience, error) {
	scope, ok := ParseAudienceScope(v.Scope)
	if !ok {
		return Audience{}, content.Errorf(content.CodeValidationFailed, "受众范围非法")
	}
	return Audience{Scope: scope, Roles: v.Roles, College: v.College, Grade: v.Grade}, nil
}

// encodeCursor 把游标编成不透明字符串。
//
// 用 base64 而不是把三元组直接写进 query：游标是实现细节，客户端不该
// 拼它、也不该从它推断排序规则。
//
// **时间用纳秒精度编码**（不是 UnixMilli）。早期实现用毫秒，而 PostgreSQL
// 的 timestamptz 是微秒精度：同一毫秒内的多条公告会被截断到同一个整毫秒，
// 翻页时「严格小于该时刻」会把同毫秒的整批行滤掉，直接漏条。
// 纳秒编码能无损表达 PG 的微秒精度，代价只是游标串长几个字符。
func encodeCursor(c PageCursor) string {
	raw := "p" + boolDigit(c.Pinned) + "|" + strconv.FormatInt(c.PublishedAt.UnixNano(), 10) + "|" + strconv.FormatInt(c.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func boolDigit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func decodeCursor(raw string) (PageCursor, error) {
	if raw == "" {
		return PageCursor{}, nil
	}
	dec, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return PageCursor{}, content.Errorf(content.CodeInvalidCursor, "游标无法解析")
	}
	parts := strings.Split(string(dec), "|")
	if len(parts) != 3 || len(parts[0]) != 2 {
		return PageCursor{}, content.Errorf(content.CodeInvalidCursor, "游标无法解析")
	}
	ns, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return PageCursor{}, content.Errorf(content.CodeInvalidCursor, "游标无法解析")
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return PageCursor{}, content.Errorf(content.CodeInvalidCursor, "游标无法解析")
	}
	return PageCursor{
		Pinned: parts[0][1] == '1',
		// 与 encodeCursor 的纳秒编码对称；用 UTC 保证同一时刻跨部署一致。
		PublishedAt: time.Unix(0, ns).UTC(),
		ID:          id,
	}, nil
}

// errorResponse 与仓库既有端点的错误形状一致。
type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError 把领域错误映射为 HTTP 状态。
//
// 只有内容域错误码会映射成 4xx；其余（数据库故障等）一律 500 且不泄露内部
// 细节——绝不把内部错误包成 4xx 骗客户端。
func writeError(c *gin.Context, err error) {
	code, ok := content.CodeOf(err)
	if !ok {
		c.JSON(http.StatusInternalServerError, errorResponse{Code: "internal", Message: "服务内部错误"})
		return
	}
	status := http.StatusBadRequest
	switch code {
	case content.CodeNotFound:
		status = http.StatusNotFound
	case content.CodeForbidden:
		status = http.StatusForbidden
	case content.CodeLocked:
		status = http.StatusConflict
	case content.CodeAttachmentTooLarge, content.CodePayloadTooLarge:
		status = http.StatusRequestEntityTooLarge
	case content.CodeUnsupportedMediaType:
		status = http.StatusUnsupportedMediaType
	}
	c.JSON(status, errorResponse{Code: string(code), Message: err.Error()})
}
