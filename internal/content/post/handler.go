package post

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/pagination"
)

// UserIDFunc 从请求上下文取当前会话用户（由装配层注入）。
//
// 业务域不 import server：内容域若反向依赖框架适配层，就违反
// README 架构约束第 1、2 条。与 announcement / message 同一手法。
type UserIDFunc func(c *gin.Context) (int64, bool)

// RegisterPublic 挂载读者侧两条只读路由（公开读）。
func RegisterPublic(r gin.IRouter, svc *Service, userIDOf UserIDFunc) {
	if userIDOf == nil {
		userIDOf = func(*gin.Context) (int64, bool) { return 0, false }
	}
	r.GET("/posts", listHandler(svc, userIDOf))
	r.GET("/posts/:id", getHandler(svc, userIDOf))
}

// RegisterSession 挂载需要会话的写路由。
//
// **必须挂在已套鉴权的路由组上**：本包不重复做鉴权，只做「取当前用户 →
// 业务 → 映射状态码」。取不到用户时 fail-closed 401（双保险）。
func RegisterSession(r gin.IRouter, svc *Service, userIDOf UserIDFunc) {
	if userIDOf == nil {
		userIDOf = func(*gin.Context) (int64, bool) { return 0, false }
	}
	r.POST("/posts", createHandler(svc, userIDOf))
	r.POST("/posts/attachments", uploadHandler(svc, userIDOf))
	r.POST("/posts/:id/replies", replyHandler(svc, userIDOf))
	r.POST("/posts/:id/like", reactHandler(svc, userIDOf, "like", true))
	r.DELETE("/posts/:id/like", reactHandler(svc, userIDOf, "like", false))
	r.POST("/posts/:id/favorite", reactHandler(svc, userIDOf, "favorite", true))
	r.DELETE("/posts/:id/favorite", reactHandler(svc, userIDOf, "favorite", false))
	r.POST("/posts/:id/replies/:replyId/like", reactCommentHandler(svc, userIDOf, true))
	r.DELETE("/posts/:id/replies/:replyId/like", reactCommentHandler(svc, userIDOf, false))
	r.PATCH("/posts/:id", patchHandler(svc, userIDOf))
	r.DELETE("/posts/:id", deleteHandler(svc, userIDOf))
}

// RegisterAdmin 挂载管理端两条路由（装配层负责 admin scope + 角色门槛）。
func RegisterAdmin(r gin.IRouter, svc *Service) {
	r.GET("/admin/posts", adminListHandler(svc))
	// 管理端单帖详情：处理举报/申诉时必须能看单条（列表不够）。
	// 它带真实作者（adminView），而读者侧详情不带——这正是两个入口分开的理由。
	r.GET("/admin/posts/:id", adminGetHandler(svc))
	r.DELETE("/admin/posts/:id", adminDeleteHandler(svc))
}

// ---------- 响应体（camelCase）----------

// publicView 是读者侧看到的帖子形状。
//
// **不含 authorId**：帖子对读者匿名（User Story 18）。作者本人靠 isMine 识别。
type publicView struct {
	ID            int64      `json:"id"`
	Board         string     `json:"board"`
	Title         string     `json:"title"`
	Body          string     `json:"body"`
	Attachments   []string   `json:"attachments,omitempty"`
	AttachmentURL []string   `json:"attachmentUrls,omitempty"`
	PublishedAt   time.Time  `json:"publishedAt"`
	EditedAt      *time.Time `json:"editedAt"`
	IsMine        bool       `json:"isMine"`
	// 反应状态：**只有登录用户**看到自己的；匿名读者恒 false。
	// 这两个字段不进列表响应（列表不逐条查反应，成本高且用途低）。
	LikedByMe     bool `json:"likedByMe"`
	FavoritedByMe bool `json:"favoritedByMe"`
}

// adminView 多带真实作者（管理端处理问题时需要，且该读取记审计）。
type adminView struct {
	publicView
	AuthorID int64 `json:"authorId"`
}

type replyView struct {
	ID          int64     `json:"id"`
	PostID      int64     `json:"postId"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"publishedAt"`
	// 留言作者**不下发**（匿名），但「我是否赞过这条留言」要下发，
	// 否则前端无法把点赞按钮画成已选中态。
	LikedByMe bool `json:"likedByMe"`
}

func toPublic(p *Post, viewerID int64) publicView {
	return publicView{
		ID: p.ID, Board: string(p.Board), Title: p.Title, Body: p.Body,
		Attachments: p.Attachments, PublishedAt: p.PublishedAt,
		EditedAt: p.EditedAt,
		// isMine 只在有会话时可能为真；匿名读者恒 false。
		IsMine: viewerID > 0 && p.AuthorID == viewerID,
	}
}

func toAdmin(p *Post) adminView {
	return adminView{publicView: toPublic(p, 0), AuthorID: p.AuthorID}
}

type listResponse struct {
	Items []publicView `json:"items"`
	Next  string       `json:"next,omitempty"`
}

// ---------- 读者侧 ----------

func listHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
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
		// board 为空表示不过滤；非空必须是合法枚举。
		var board Board
		if raw := c.Query("board"); raw != "" {
			b, verr := ValidateBoard(raw)
			if verr != nil {
				apperr.WriteGin(c, verr)
				return
			}
			board = b
		}
		res, err := svc.List(c.Request.Context(), board, after, limit)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		viewerID, _ := userIDOf(c)
		out := listResponse{Items: make([]publicView, 0, len(res.Items))}
		for _, p := range res.Items {
			out.Items = append(out.Items, toPublic(p, viewerID))
		}
		if res.Next != nil {
			out.Next = encodeCursor(*res.Next)
		}
		c.JSON(http.StatusOK, out)
	}
}

func getHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		d, err := svc.Get(c.Request.Context(), id)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		viewerID, _ := userIDOf(c)
		v := toPublic(d.Post, viewerID)
		v.AttachmentURL = d.AttachmentURLs

		// 反应状态逐请求现算（不缓存）：点赞数是高频变化的量，
		// 缓存它带来的不一致比省下的查询更贵。
		if viewerID > 0 {
			st := svc.ReactionStateOf(c.Request.Context(), TargetPost, d.Post.ID, viewerID)
			v.LikedByMe = st.LikedByMe
			v.FavoritedByMe = st.FavoritedByMe
		}

		replies := make([]replyView, 0, len(d.Replies))
		for _, r := range d.Replies {
			rv := replyView{ID: r.ID, PostID: r.PostID, Body: r.Body, PublishedAt: r.PublishedAt}
			if viewerID > 0 {
				rv.LikedByMe = svc.ReactionStateOf(c.Request.Context(), TargetComment, r.ID, viewerID).LikedByMe
			}
			replies = append(replies, rv)
		}
		c.JSON(http.StatusOK, gin.H{"data": v, "replies": replies})
	}
}

// ---------- 会话侧 ----------

type createRequest struct {
	Board       string   `json:"board"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Attachments []string `json:"attachments"`
}

func createHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, ok := userIDOf(c)
		if !ok {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份"))
			return
		}
		var in createRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "请求体非法"))
			return
		}
		p, err := svc.Create(c.Request.Context(), uid, CreateInput{
			Board: in.Board, Title: in.Title, Body: in.Body, Attachments: in.Attachments,
		})
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": toPublic(p, uid)})
	}
}

// uploadHandler 是附件上传（后端中转）的入口。
//
// 体积限制、内容嗅探、key 生成都在 service.Upload 里（它拿得到 repo/store）；
// handler 只负责「取会话用户 + 交出 *http.Request」。
//
// 为什么把整个 Request 交给 service 而不是先解析出 file：multipart 的解析
// 与体积上限是同一件事（MaxBytesReader 必须在解析之前包住 body），
// 拆成两半会让「谁负责包 body」变得模糊。
func uploadHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := userIDOf(c); !ok {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份"))
			return
		}
		res, err := svc.Upload(c.Request.Context(), c.Request)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusCreated, res)
	}
}

func replyHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, ok := userIDOf(c)
		if !ok {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份"))
			return
		}
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		var in struct {
			Body string `json:"body"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "请求体非法"))
			return
		}
		r, err := svc.AddReply(c.Request.Context(), uid, id, in.Body)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": replyView{
			ID: r.ID, PostID: r.PostID, Body: r.Body, PublishedAt: r.PublishedAt,
		}})
	}
}

// reactHandler 处理对帖子的点赞/收藏。
func reactHandler(svc *Service, userIDOf UserIDFunc, kind string, add bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, ok := userIDOf(c)
		if !ok {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份"))
			return
		}
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		if err := svc.React(c.Request.Context(), uid, string(TargetPost), id, kind, add); err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

// reactCommentHandler 处理对留言的点赞（Spec 只要求留言点赞，没有收藏）。
func reactCommentHandler(svc *Service, userIDOf UserIDFunc, add bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, ok := userIDOf(c)
		if !ok {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份"))
			return
		}
		replyID, err := parseID(c.Param("replyId"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		if err := svc.React(c.Request.Context(), uid, string(TargetComment), replyID, string(ReactionLike), add); err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

func patchHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, ok := userIDOf(c)
		if !ok {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份"))
			return
		}
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		var in struct {
			Title       *string   `json:"title"`
			Body        *string   `json:"body"`
			Attachments *[]string `json:"attachments"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "请求体非法"))
			return
		}
		p, err := svc.Update(c.Request.Context(), uid, id, UpdateInput{
			Title: in.Title, Body: in.Body, Attachments: in.Attachments,
		})
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toPublic(p, uid)})
	}
}

func deleteHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, ok := userIDOf(c)
		if !ok {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份"))
			return
		}
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		if err := svc.Delete(c.Request.Context(), uid, id); err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

// ---------- 管理端 ----------

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
		// 管理端走 AdminList（跨板块）：与读者侧分开入口，避免
		// 「误传零值 board 变成看全部」这类静默越权。
		res, err := svc.AdminList(c.Request.Context(), after, limit)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		items := make([]adminView, 0, len(res.Items))
		for _, p := range res.Items {
			items = append(items, toAdmin(p))
		}
		out := gin.H{"items": items}
		if res.Next != nil {
			out["next"] = encodeCursor(*res.Next)
		}
		c.JSON(http.StatusOK, out)
	}
}

// adminGetHandler 是管理端单帖详情（含真实作者、含已软删状态）。
func adminGetHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		// 用 FindPost 而不是 Get：管理端要看得到已软删的帖子
		//（否则「为什么这条被删了」无从排查）。
		p, err := svc.FindPost(c.Request.Context(), id)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		v := toAdmin(p)
		// 已软删的帖子也回 200，并在响应里带 deletedAt，
		// 由管理端 UI 自己决定怎么标红——用 404 会让人以为「记录不存在」。
		c.JSON(http.StatusOK, gin.H{"data": v, "deletedAt": p.DeletedAt})
	}
}

func adminDeleteHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		if err := svc.AdminDelete(c.Request.Context(), id); err != nil {
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
		return 0, apperr.Errorf(apperr.CodeValidationFailed, "id 非法")
	}
	return id, nil
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, apperr.Errorf(apperr.CodeValidationFailed, "limit 非法")
	}
	return n, nil
}

// encodeCursor/decodeCursor 转调全仓共享实现（internal/pagination）。
//
// 游标的精度与格式判据只有一份：三个域此前各写一份，修精度 bug 要改三处。
func encodeCursor(c PageCursor) string {
	return pagination.Encode(pagination.Cursor{Time: c.PublishedAt, ID: c.ID})
}

func decodeCursor(raw string) (PageCursor, error) {
	c, ok := pagination.Decode(raw)
	if !ok {
		return PageCursor{}, apperr.Errorf(apperr.CodeInvalidCursor, "游标无法解析")
	}
	return PageCursor{PublishedAt: c.Time, ID: c.ID}, nil
}

// isMaxBytesErr 判断是否是 MaxBytesReader 触发的「请求体过大」。
var errHTTPMaxBytes = &http.MaxBytesError{}

func isMaxBytesErr(err error) bool {
	if errors.Is(err, errHTTPMaxBytes) {
		return true
	}
	// gin 会把 FormFile 的错误包一层，这里再做一次宽松匹配。
	return strings.Contains(err.Error(), "request body too large")
}

// 编译期确认：io 与 errors 都被用到（避免 import 冗余告警）。
var _ = io.Discard
