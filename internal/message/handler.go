package message

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/pagination"
)

// UserIDFunc 从请求上下文取当前会话用户。
//
// 为什么由装配层注入而不是本包直接读 context：会话身份属 server / 身份域，
// 业务域若直接 import 它们就违反了「业务域只依赖 platform 接口、不互相 import」
// （README 架构约束第 1、2 条）。这与 announcement 的 ViewerFunc 是同一手法。
//
// 返回 (id, false) 表示没有有效会话。本包**全部端点都需会话**（由装配层把
// Register 挂在已套会话鉴权的路由组上），handler 仍对 false 做 fail-closed：
// 一律 401，绝不因为「装配层应该已经判过」就默认放行一个 0 号用户。
type UserIDFunc func(*gin.Context) (int64, bool)

// Register 挂载站内消息的六条会话端点。
//
// 鉴权由装配层保证（把这个 Register 挂在已套会话中间件的路由组上）；本包不
// 重复实现一套鉴权，但会用 userIDOf 做二次 fail-closed 取用户：取不到就 401。
//
// 路由按 HTTP method 分树，因此「/messages/ 下既有静态段（read-all / unread-count）
// 又有参数段（:id）」不会触发 gin 路由树的注册顺序冲突：
//   - GET    /messages, /messages/unread-count   （纯静态）
//   - POST   /messages, /messages/read-all      （纯静态）
//   - PATCH  /messages/:id/read                 （纯参数）
//   - DELETE /messages/:id                      （纯参数）
func Register(r gin.IRouter, svc *Service, userIDOf UserIDFunc) {
	if userIDOf == nil {
		// fail-closed：没注入取用户函数时，任何请求都取不到会话 → 全部 401。
		userIDOf = func(*gin.Context) (int64, bool) { return 0, false }
	}
	r.GET("/messages", listHandler(svc, userIDOf))
	r.GET("/messages/unread-count", unreadCountHandler(svc, userIDOf))
	r.PATCH("/messages/:id/read", markReadHandler(svc, userIDOf))
	r.POST("/messages/read-all", markAllReadHandler(svc, userIDOf))
	r.DELETE("/messages/:id", deleteHandler(svc, userIDOf))
	r.POST("/messages", createMailHandler(svc, userIDOf))
}

// ---------- 响应体 ----------

// messageView 是客户端看到的消息形状，字段名一律 camelCase（Spec 信封约定）。
//
// readAt / deletedAt **不用 omitempty**：nil 时显式输出 null，客户端才能观察到
// 「通知 upsert 把 readAt 重置为 null（变回未读）」这一外部可观察行为。
// source* / senderId 用 omitempty：内部信没有 source、通知没有实名发送者。
type messageView struct {
	ID             int64      `json:"id"`
	RecipientID    int64      `json:"recipientId"`
	Kind           string     `json:"kind"`
	SourceDomain   string     `json:"sourceDomain,omitempty"`
	SourceObjectID string     `json:"sourceObjectId,omitempty"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	Hits           int        `json:"hits"`
	ReadAt         *time.Time `json:"readAt"`
	DeletedAt      *time.Time `json:"deletedAt"`
	CreatedAt      time.Time  `json:"createdAt"`
	SenderID       int64      `json:"senderId,omitempty"`
}

func toView(m *Message) messageView {
	return messageView{
		ID: m.ID, RecipientID: m.RecipientID, Kind: string(m.Kind),
		SourceDomain: m.SourceDomain, SourceObjectID: m.SourceObjectID,
		Title: m.Title, Body: m.Body, Hits: m.Hits,
		ReadAt: m.ReadAt, DeletedAt: m.DeletedAt, CreatedAt: m.CreatedAt,
		SenderID: m.SenderID,
	}
}

type listResponse struct {
	Items []messageView `json:"items"`
	Next  string        `json:"next,omitempty"`
}

// ---------- handlers ----------

func listHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := requireUser(c, userIDOf)
		if !ok {
			return
		}
		f, err := parseFilter(c, userID)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
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

		res, err := svc.List(c.Request.Context(), f, after, limit)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		out := listResponse{Items: make([]messageView, 0, len(res.Items))}
		for _, m := range res.Items {
			out.Items = append(out.Items, toView(m))
		}
		if res.Next != nil {
			out.Next = encodeCursor(*res.Next)
		}
		c.JSON(http.StatusOK, out)
	}
}

func unreadCountHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := requireUser(c, userIDOf)
		if !ok {
			return
		}
		f, err := parseFilter(c, userID)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		// 未读数口径固定为 count(readAt is null and deletedAt is null)；
		// 这里的 UnreadOnly 与列表共用解析，但对计数结果无额外影响
		// （UnreadCount 本身就只数未读行），保持同一份 filter 避免口径漂移。
		n, err := svc.UnreadCount(c.Request.Context(), f)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"count": n})
	}
}

func markReadHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := requireUser(c, userIDOf)
		if !ok {
			return
		}
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		m, err := svc.MarkRead(c.Request.Context(), userID, id)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toView(m)})
	}
}

func markAllReadHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := requireUser(c, userIDOf)
		if !ok {
			return
		}
		// 「当前筛选条件」= GET /messages 的同一组 query 参数：
		// 前端把列表当前的 kind / unread 原样带上即可，不另造一套 body 协议。
		f, err := parseFilter(c, userID)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		n, err := svc.MarkAllRead(c.Request.Context(), f)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"updated": n})
	}
}

func deleteHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := requireUser(c, userIDOf)
		if !ok {
			return
		}
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		if err := svc.Delete(c.Request.Context(), userID, id); err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

// createRequest 是 POST /messages 的入参。
//
// senderId **故意不在请求体里**：实名可见的内部信，发送者只能由会话注入，
// 客户端自报会被忽略（ShouldBindJSON 没有该字段，JSON 里多写也不会覆盖）。
type createRequest struct {
	Kind        string `json:"kind"`
	RecipientID int64  `json:"recipientId"`
	Title       string `json:"title"`
	Body        string `json:"body"`
}

func createMailHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := requireUser(c, userIDOf)
		if !ok {
			return
		}
		var in createRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "请求体非法"))
			return
		}
		// 通知不接受外部直接建（传 kind=notification 要拒）。
		if _, err := ParseMailKind(in.Kind); err != nil {
			apperr.WriteGin(c, err)
			return
		}
		m, err := svc.CreateMail(c.Request.Context(), CreateMailInput{
			RecipientID: in.RecipientID,
			SenderID:    userID,
			Title:       in.Title,
			Body:        in.Body,
		})
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": toView(m)})
	}
}

// ---------- 工具 ----------

// requireUser 取当前会话用户，取不到写 401 并返回 false。
func requireUser(c *gin.Context, userIDOf UserIDFunc) (int64, bool) {
	id, ok := userIDOf(c)
	if !ok || id <= 0 {
		apperr.WriteGin(c, apperr.Errorf(apperr.CodeUnauthorized, "需要登录"))
		return 0, false
	}
	return id, true
}

func parseID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.Errorf(apperr.CodeValidationFailed, "id 非法")
	}
	return id, nil
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return 0, nil // Service 会用默认值
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, apperr.Errorf(apperr.CodeValidationFailed, "limit 非法")
	}
	return n, nil
}

// parseFilter 把 query 里的 kind / unread 解析成 ListFilter。
// GET /messages、GET /messages/unread-count、POST /messages/read-all 三处共用，
// 保证「read-all 按当前筛选条件」与列表严格同口径。
func parseFilter(c *gin.Context, userID int64) (ListFilter, error) {
	f := ListFilter{RecipientID: userID}
	if raw := c.Query("kind"); raw != "" {
		k, ok := ParseKind(raw)
		if !ok {
			return ListFilter{}, apperr.Errorf(apperr.CodeValidationFailed, "kind 非法")
		}
		f.Kind = k
	}
	rawUnread := c.Query("unread")
	if rawUnread == "" {
		rawUnread = c.Query("unreadOnly")
	}
	if rawUnread != "" {
		v, err := strconv.ParseBool(rawUnread)
		if err != nil {
			return ListFilter{}, apperr.Errorf(apperr.CodeValidationFailed, "unread 参数非法")
		}
		f.UnreadOnly = v
	}
	return f, nil
}

// encodeCursor 把 (createdAt, id) 编成不透明字符串。
//
// **时间用纳秒精度编码**（不是 UnixMilli）：与 announcement 范本同因，
// PostgreSQL 的 timestamptz 是微秒精度，若只编到毫秒，同毫秒内插入的多条
// 消息会被截断到同一整毫秒，翻页时「严格小于该时刻」会整批滤掉 → 漏条。
func encodeCursor(c PageCursor) string {
	// 转调全仓共享实现：精度与格式判据只有一份。
	return pagination.Encode(pagination.Cursor{Time: c.CreatedAt, ID: c.ID})
}

func decodeCursor(raw string) (PageCursor, error) {
	c, ok := pagination.Decode(raw)
	if !ok {
		return PageCursor{}, apperr.Errorf(apperr.CodeInvalidCursor, "游标无法解析")
	}
	return PageCursor{CreatedAt: c.Time, ID: c.ID}, nil
}

// errorResponse 是错误信封，字段名 camelCase。
type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
