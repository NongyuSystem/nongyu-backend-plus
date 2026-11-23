package post_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/content/post"
)

// 本文件锁住两个「评价专用查询」接口。
//
//	GET /post-reviews/teacher?teacherName=...
//	GET /post-reviews/teacher-course?teacherName=...&courseName=...
//
// 两条都**需要会话**；匹配是 detail 字段的**精确**匹配（大小写不敏感）。

// seedReview 造一条评价贴，返回它的 id。
func seedReview(t *testing.T, r *gin.Engine, uid, board, teacher, course string, rating int) int64 {
	t.Helper()
	body := map[string]any{
		"board": board, "title": teacher + "的" + course, "body": "评价内容",
		"detail": map[string]any{
			"teacherName": teacher,
			"courseName":  course,
			"rating":      rating,
		},
	}
	code, resp := do(t, r, http.MethodPost, "/posts", uid, body)
	if code != http.StatusCreated {
		t.Fatalf("造评价失败：%d %v", code, resp)
	}
	return int64(resp["data"].(map[string]any)["id"].(float64))
}

// TestTeacherReviews_FiltersByTeacher 按老师过滤，且跨两种评价类型。
func TestTeacherReviews_FiltersByTeacher(t *testing.T) {
	r, _, _, _ := newEnv(t)

	// 王老师：一条教师评价 + 一条课程评价（两种类型都该被查到）。
	seedReview(t, r, "7", "teacher_review", "王老师", "高等数学", 5)
	seedReview(t, r, "7", "course_review", "王老师", "线性代数", 4)
	// 李老师：不该出现在王老师的结果里。
	seedReview(t, r, "7", "teacher_review", "李老师", "大学物理", 3)

	code, body := do(t, r, http.MethodGet, "/post-reviews/teacher?teacherName=王老师", "9", nil)
	if code != http.StatusOK {
		t.Fatalf("查询失败：%d %v", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("王老师应有 2 条评价（跨两种类型），实际 %d 条", len(items))
	}
	for _, it := range items {
		d := it.(map[string]any)["detail"].(map[string]any)
		if d["teacherName"] != "王老师" {
			t.Fatalf("结果混进了别的老师：%v", d)
		}
	}
	// 条件回显应带上（客户端翻页要原样带回去）。
	q, _ := body["query"].(map[string]any)
	if q["teacherName"] != "王老师" {
		t.Fatalf("应回显查询条件，得到 %v", body["query"])
	}
}

// TestTeacherCourseReviews_FiltersByBoth 「某老师某课程」两个条件都要满足。
func TestTeacherCourseReviews_FiltersByBoth(t *testing.T) {
	r, _, _, _ := newEnv(t)
	seedReview(t, r, "7", "teacher_review", "王老师", "高等数学", 5)
	seedReview(t, r, "7", "teacher_review", "王老师", "线性代数", 4)
	seedReview(t, r, "7", "course_review", "李老师", "高等数学", 3)

	code, body := do(t, r, http.MethodGet,
		"/post-reviews/teacher-course?teacherName=王老师&courseName=高等数学", "9", nil)
	if code != http.StatusOK {
		t.Fatalf("查询失败：%d %v", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("应只命中 1 条（王老师+高等数学），实际 %d 条", len(items))
	}
	d := items[0].(map[string]any)["detail"].(map[string]any)
	if d["teacherName"] != "王老师" || d["courseName"] != "高等数学" {
		t.Fatalf("命中的不是目标记录：%v", d)
	}
}

// TestReviews_RequireSession 未登录必须 401。
func TestReviews_RequireSession(t *testing.T) {
	r, _, _, _ := newEnv(t)
	seedReview(t, r, "7", "teacher_review", "王老师", "高等数学", 5)

	for _, path := range []string{
		"/post-reviews/teacher?teacherName=王老师",
		"/post-reviews/teacher-course?teacherName=王老师&courseName=高等数学",
	} {
		if code, _ := do(t, r, http.MethodGet, path, "", nil); code != http.StatusUnauthorized {
			t.Fatalf("%s 未登录应 401，得到 %d", path, code)
		}
	}
}

// TestReviews_ExactMatchOnly 精确匹配，不做模糊。
//
// 「王」不该命中「王老师」——模糊匹配会让结果不可预测，
// 也会让「某个名字是另一个名字前缀」时结果爆炸。
func TestReviews_ExactMatchOnly(t *testing.T) {
	r, _, _, _ := newEnv(t)
	seedReview(t, r, "7", "teacher_review", "王老师", "高等数学", 5)

	_, body := do(t, r, http.MethodGet, "/post-reviews/teacher?teacherName=王", "9", nil)
	if items, _ := body["items"].([]any); len(items) != 0 {
		t.Fatalf("精确匹配下「王」不该命中「王老师」，实际 %d 条", len(items))
	}
}

// TestReviews_CaseInsensitive 大小写不敏感（课程名常含英文）。
func TestReviews_CaseInsensitive(t *testing.T) {
	r, _, _, _ := newEnv(t)
	seedReview(t, r, "7", "course_review", "Mr. Smith", "C Language", 5)

	// 用小写查，应命中。**必须 URL 编码**：查询串里有空格，
	// 裸空格会让 httptest.NewRequest 解析失败（panic），
	// 真实客户端也必须编码——这正是 curl --data-urlencode 的用途。
	q := url.Values{}
	q.Set("teacherName", "mr. smith")
	q.Set("courseName", "c language")
	_, body := do(t, r, http.MethodGet, "/post-reviews/teacher-course?"+q.Encode(), "9", nil)
	if items, _ := body["items"].([]any); len(items) != 1 {
		t.Fatalf("大小写不敏感应命中，实际 %d 条（body=%v）", len(items), body)
	}
}

// TestReviews_SoftDeletedExcluded 软删的评价不出现。
func TestReviews_SoftDeletedExcluded(t *testing.T) {
	r, _, _, _ := newEnv(t)
	id := seedReview(t, r, "7", "teacher_review", "王老师", "高等数学", 5)
	if code, _ := do(t, r, http.MethodDelete, "/posts/"+itoa(id), "7", nil); code != http.StatusOK {
		t.Fatal("删帖失败")
	}
	_, body := do(t, r, http.MethodGet, "/post-reviews/teacher?teacherName=王老师", "9", nil)
	if items, _ := body["items"].([]any); len(items) != 0 {
		t.Fatalf("软删的评价不该出现，实际 %d 条", len(items))
	}
}

// TestReviews_RejectEmptyParams 缺参数要 400 并说明缺什么。
func TestReviews_RejectEmptyParams(t *testing.T) {
	r, _, _, _ := newEnv(t)

	cases := []string{
		"/post-reviews/teacher",                        // 缺 teacherName
		"/post-reviews/teacher?teacherName=",           // 空值
		"/post-reviews/teacher-course?teacherName=王老师", // 缺 courseName
		"/post-reviews/teacher-course?courseName=数学",   // 缺 teacherName
	}
	for _, path := range cases {
		code, body := do(t, r, http.MethodGet, path, "9", nil)
		if code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，得到 %d %v", path, code, body)
		}
	}
}

// TestReviews_Pagination 游标翻页不重不漏。
func TestReviews_Pagination(t *testing.T) {
	r, _, _, _ := newEnv(t)
	for i := 0; i < 5; i++ {
		seedReview(t, r, "7", "teacher_review", "王老师", "课程"+itoa(int64(i)), 5)
	}

	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 10; i++ {
		path := "/post-reviews/teacher?teacherName=王老师&limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		code, body := do(t, r, http.MethodGet, path, "9", nil)
		if code != http.StatusOK {
			t.Fatalf("翻页失败：%d", code)
		}
		items, _ := body["items"].([]any)
		for _, it := range items {
			id := itoa(int64(it.(map[string]any)["id"].(float64)))
			if seen[id] {
				t.Fatalf("翻页重复：%s", id)
			}
			seen[id] = true
		}
		next, _ := body["next"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 5 {
		t.Fatalf("应翻到 5 条，实际 %d 条", len(seen))
	}
}

// TestReviews_NoCacheKeyPollution 评价查询不走列表缓存。
//
// 若它复用了 /posts 的缓存键，会与公开列表互相污染：
// 用同一键缓存「全站帖子」和「王老师的评价」是灾难。
func TestReviews_NoCacheKeyPollution(t *testing.T) {
	r, svc, _, _ := newEnv(t)
	seedReview(t, r, "7", "teacher_review", "王老师", "高等数学", 5)

	ctx := context.Background()
	// 先查评价
	res, err := svc.TeacherReviews(ctx, "王老师", post.PageCursor{}, 20)
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("应有 1 条，得到 %d", len(res.Items))
	}
	// 再查全站列表（不应被评价查询影响）
	all, err := svc.List(ctx, "", 0, post.PageCursor{}, 20)
	if err != nil {
		t.Fatalf("全站列表失败：%v", err)
	}
	if len(all.Items) != 1 {
		t.Fatalf("全站列表应为 1 条，得到 %d", len(all.Items))
	}
}

// TestReviews_EmptyResultIs200 查不到时返回 200 + 空列表，不是 404。
//
// 这两个入口是课程模块的固定功能位，「还没有人评价」是**正常状态**
// 而不是错误。返回 404 会让前端把它当异常处理，也容易被误认为接口挂了。
func TestReviews_EmptyResultIs200(t *testing.T) {
	r, _, _, _ := newEnv(t)

	for _, path := range []string{
		"/post-reviews/teacher?teacherName=不存在的老师",
		"/post-reviews/teacher-course?teacherName=不存在的老师&courseName=不存在的课",
	} {
		code, body := do(t, r, http.MethodGet, path, "9", nil)
		if code != http.StatusOK {
			t.Fatalf("%s 应 200（空列表），得到 %d %v", path, code, body)
		}
		items, _ := body["items"].([]any)
		if len(items) != 0 {
			t.Fatalf("%s 应为空列表，得到 %d 条", path, len(items))
		}
		// items 必须是数组而不是 null：前端可以直接 .length 而不用判空。
		if body["items"] == nil {
			t.Fatalf("%s 的 items 应为 []，得到 null", path)
		}
	}
}

// TestReviews_NoSearchParameters 这两个入口不接受检索参数。
//
// 它们是**固定功能位**（从课程上下文进入），不是搜索。若接受 q/keyword，
// 接口语义会变成「搜索」，那是产品上另一件事（需要模糊匹配、排序、
// 可能还要权限）。这条测试锁住「不提供搜索入口」这个边界：
// 传了 q 也不生效，结果与不传相同。
func TestReviews_NoSearchParameters(t *testing.T) {
	r, _, _, _ := newEnv(t)
	seedReview(t, r, "7", "teacher_review", "王老师", "高等数学", 5)

	// 带一个「看起来像搜索」的参数：不应改变结果。
	code1, body1 := do(t, r, http.MethodGet, "/post-reviews/teacher?teacherName=王老师", "9", nil)
	code2, body2 := do(t, r, http.MethodGet,
		"/post-reviews/teacher?teacherName=王老师&q=王&keyword=高数", "9", nil)

	if code1 != http.StatusOK || code2 != http.StatusOK {
		t.Fatalf("两种请求都应 200，得到 %d / %d", code1, code2)
	}
	n1 := len(body1["items"].([]any))
	n2 := len(body2["items"].([]any))
	if n1 != n2 {
		t.Fatalf("q/keyword 不该影响结果：%d vs %d", n1, n2)
	}
}

// TestTeacherReviews_CourseReviewWithTeacherShowsUp 语义确认：
//
// 「查某老师」会同时返回 teacher_review 与 course_review 中含该教师名的评价。
// 注意 course_review 的 teacherName 是**选填**的，所以只填了课程名的
// 课程评价不会出现在老师的结果里——这是刻意的（没有教师名就无从归属）。
func TestTeacherReviews_CourseReviewWithTeacherShowsUp(t *testing.T) {
	r, _, _, _ := newEnv(t)

	// 课程评价，填了教师名 → 该出现在老师结果里。
	seedReview(t, r, "7", "course_review", "张老师", "数据结构", 5)
	// 课程评价，没填教师名 → 不该出现（人工构造 detail 时省略 teacherName）。
	// 这里直接走 service 造，因为 handler 层会按规则校验（teacherName 选填，可省）。
	code, _ := do(t, r, http.MethodPost, "/posts", "7", map[string]any{
		"board": "course_review", "title": "没写老师的课评", "body": "b",
		"detail": map[string]any{"courseName": "操作系统", "rating": 4},
	})
	if code != http.StatusCreated {
		t.Fatalf("造无教师名的课程评价失败：%d", code)
	}

	_, body := do(t, r, http.MethodGet, "/post-reviews/teacher?teacherName=张老师", "9", nil)
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("应只返回填了张老师的那 1 条，得到 %d 条", len(items))
	}
}
