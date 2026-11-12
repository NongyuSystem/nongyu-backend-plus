package announcement

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/content"
	"nongyu-backend-plus/internal/pagination"
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

// UserIDFunc 从请求上下文取当前会话用户。
//
// 为什么需要它（而不是直接 c.GetString(server.CtxUserID)）：内容域若 import
// server，就成了「业务域反向依赖框架适配层」，违反 README 架构约束第 1、2 条。
// 由装配层注入提取函数，业务域只声明「我需要一个当前用户 id」。
// 与 message.UserIDFunc 同构。
type UserIDFunc func(*gin.Context) (int64, bool)

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
func RegisterAdmin(r gin.IRouter, svc *Service, userIDOf UserIDFunc) {
	if userIDOf == nil {
		userIDOf = func(*gin.Context) (int64, bool) { return 0, false }
	}
	r.GET("/admin/announcements", adminListHandler(svc))
	// 管理端单条详情：处理「这条公告为什么没发出去/为什么被撤回」时必须能看单条。
	// 与帖子域 /admin/posts/:id 对齐，两个管理端入口保持同一形态。
	r.GET("/admin/announcements/:id", adminGetHandler(svc))
	r.POST("/admin/announcements", adminCreateHandler(svc, userIDOf))
	r.PATCH("/admin/announcements/:id", adminUpdateHandler(svc, userIDOf))
	r.DELETE("/admin/announcements/:id", adminDeleteHandler(svc, userIDOf))
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
			apperr.WriteGin(c, err)
			return
		}
		limit, err := parseLimit(c.Query("limit"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}

		res, err := svc.List(c.Request.Context(), viewerOf(c), after, limit)
		if err != nil {
			apperr.WriteGin(c, err)
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
				apperr.WriteGin(c, err)
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
			apperr.WriteGin(c, content.Errorf(content.CodeValidationFailed, "公告 id 非法"))
			return
		}
		a, err := svc.Get(c.Request.Context(), v, id)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toPublic(a)})
	}
}

// withAudit 把请求追踪号与操作者写进 context，供 service 记审计。
//
// 追踪号复用 server 写入的 X-Request-Id（既有 RequestIDMiddleware），
// 操作者取已认证 userID。两者都没有时留空——审计实现按「未认证场景」处理。
func withAudit(c *gin.Context, uid int64) *gin.Context {
	rid := c.GetHeader("X-Request-Id")
	if rid == "" {
		rid = c.GetString("requestID")
	}
	userID := ""
	if uid > 0 {
		userID = strconv.FormatInt(uid, 10)
	}
	c.Request = c.Request.WithContext(WithAuditContext(c.Request.Context(), rid, userID))
	return c
}

// ---------- 管理端 handler ----------

func adminListHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		after, err := decodeCursor(c.Query("cursor"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		limit, err := parseLimit(c.Query("limit"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		res, err := svc.AdminList(c.Request.Context(), after, limit)
		if err != nil {
			apperr.WriteGin(c, err)
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

func adminCreateHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, _ := userIDOf(c)
		c = withAudit(c, uid)
		var in createRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			apperr.WriteGin(c, content.Errorf(content.CodeValidationFailed, "请求体非法"))
			return
		}
		aud, err := toAudience(in.Audience)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		a, err := svc.Create(c.Request.Context(), CreateInput{
			Title: in.Title, Body: in.Body,
			EffectiveFrom: in.EffectiveFrom, ExpiresAt: in.ExpiresAt,
			Pinned: in.Pinned, Audience: aud, Publish: in.Publish,
		})
		if err != nil {
			apperr.WriteGin(c, err)
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

// adminGetHandler 是管理端单条详情。
//
// 用 AdminGet 而不是读者侧 Get：管理端要能看到**草稿与已撤回**的公告
// （读者侧那些是不可见的），且要带 status / publishRevision / audience 等
// 管理字段（adminView）。软删的公告也返回 200 并带 deletedAt——
// 用 404 会让人以为记录不存在，而实际是「被删了，需要排查为什么」。
func adminGetHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		a, err := svc.AdminGet(c.Request.Context(), id)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toAdmin(a), "deletedAt": a.DeletedAt})
	}
}

func adminUpdateHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, _ := userIDOf(c)
		c = withAudit(c, uid)
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		var in updateRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			apperr.WriteGin(c, content.Errorf(content.CodeValidationFailed, "请求体非法"))
			return
		}

		var aud *Audience
		if in.Audience != nil {
			conv, cerr := toAudience(*in.Audience)
			if cerr != nil {
				apperr.WriteGin(c, cerr)
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
				apperr.WriteGin(c, err)
				return
			}
		}

		switch in.Action {
		case "":
			if a == nil {
				apperr.WriteGin(c, content.Errorf(content.CodeValidationFailed, "没有任何要变更的内容"))
				return
			}
		case "publish":
			a, err = svc.Publish(c.Request.Context(), id)
		case "revoke":
			a, err = svc.Revoke(c.Request.Context(), id)
		default:
			apperr.WriteGin(c, content.Errorf(content.CodeValidationFailed, "未知动作"))
			return
		}
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toAdmin(a)})
	}
}

func adminDeleteHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, _ := userIDOf(c)
		c = withAudit(c, uid)
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
	// 转调全仓共享实现。公告的排序键是 (pinned, publishedAt, id) 三元组，
	// 所以要把 Pinned 带进游标的 Flag 维度——两元组的游标会丢掉它，
	// 导致「翻到下一页时置顶帖又重新出现」。
	return pagination.Encode(pagination.Cursor{Time: c.PublishedAt, ID: c.ID, Flag: c.Pinned})
}

func boolDigit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func decodeCursor(raw string) (PageCursor, error) {
	c, ok := pagination.Decode(raw)
	if !ok {
		return PageCursor{}, content.Errorf(content.CodeInvalidCursor, "游标无法解析")
	}
	return PageCursor{Pinned: c.Flag, PublishedAt: c.Time, ID: c.ID}, nil
}
