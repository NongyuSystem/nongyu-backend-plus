package post

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/apperr"
)

// RegisterReviewQueries 挂载两个「课程模块专用」的评价查询接口。
//
// 定位（维护者澄清）：**不是搜索接口，是课程模块的两个固定功能入口**。
// 用户在课程/教师上下文里点进去，参数由前端从该上下文带过来，
// 用户不能自己输入关键词。所以这里不提供 q/keyword 之类的检索参数，
// 也只做精确匹配——搜索的语义不属于这两个入口。
//
// 与 RegisterPublic 分开是因为**鉴权要求不同**：这两条需要会话
// （装配层挂在鉴权组上），而 /posts 是公开读。分成两个 Register 让
// 「哪些要登录」在装配层一眼可见，而不是读 handler 内部才知道。
//
//	GET /post-reviews/teacher?teacherName=王老师
//	GET /post-reviews/teacher-course?teacherName=王老师&courseName=高等数学
//
// 为什么不用 /posts/reviews/*：gin 的路由树不允许 `/posts/:id`（通配符）
// 与 `/posts/reviews/teacher`（静态段）在同一层共存，启动即 panic。
// 用独立前缀既避开冲突，也让「这是两个专用入口」在 URL 上显式可见。
func RegisterReviewQueries(r gin.IRouter, svc *Service, userIDOf UserIDFunc) {
	if userIDOf == nil {
		userIDOf = func(*gin.Context) (int64, bool) { return 0, false }
	}
	r.GET("/post-reviews/teacher", teacherReviewsHandler(svc, userIDOf))
	r.GET("/post-reviews/teacher-course", teacherCourseReviewsHandler(svc, userIDOf))
}

// reviewListResponse 与普通列表同形状（items + next），
// 但额外带上查询条件回显：客户端翻页时要把条件原样带上，
// 回显能让它确认「我这次查的确实是这个老师」。
type reviewListResponse struct {
	Items []publicView `json:"items"`
	Next  string       `json:"next,omitempty"`
	// Query 回显本次实际使用的过滤条件（已 trim）。
	Query map[string]string `json:"query"`
}

func teacherReviewsHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 需要会话：fail-closed 取用户（不依赖中间件一定生效）。
		if _, ok := userIDOf(c); !ok {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份"))
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
		name := c.Query("teacherName")
		res, err := svc.TeacherReviews(c.Request.Context(), name, after, limit)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		writeReviewList(c, res, map[string]string{"teacherName": strings.TrimSpace(name)})
	}
}

func teacherCourseReviewsHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := userIDOf(c); !ok {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份"))
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
		teacher := c.Query("teacherName")
		course := c.Query("courseName")
		res, err := svc.TeacherCourseReviews(c.Request.Context(), teacher, course, after, limit)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		writeReviewList(c, res, map[string]string{
			"teacherName": strings.TrimSpace(teacher),
			"courseName":  strings.TrimSpace(course),
		})
	}
}

func writeReviewList(c *gin.Context, res ListResult, query map[string]string) {
	out := reviewListResponse{
		Items: make([]publicView, 0, len(res.Items)),
		Query: query,
	}
	for _, p := range res.Items {
		out.Items = append(out.Items, toPublic(p, 0))
	}
	if res.Next != nil {
		out.Next = encodeCursor(*res.Next)
	}
	c.JSON(http.StatusOK, out)
}
