package post_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/content/post"
	"nongyu-backend-plus/internal/content/post/store"
)

// 本文件锁住「类型 → 结构化字段」的数据驱动校验。
//
// 设计要点（2026-11-23 拍板）：
//   - Post 只有一个 detail（JSON），不按类型加固定列；
//   - 字段要求存在 post_type_rule 表里，校验时读表 —— 加新类型只加行、不改代码。

// seedRules 返回与迁移种子一致的内存规则仓库。
func seedRules() *store.DetailRuleMemoryRepo { return store.NewSeedDetailRules() }

// TestDetail_TeacherReview_HappyPath 教师评价带齐三个字段应成功。
func TestDetail_TeacherReview_HappyPath(t *testing.T) {
	r, _, _, _ := newEnv(t)
	code, body := do(t, r, http.MethodPost, "/posts", "7", map[string]any{
		"board": "teacher_review", "title": "王老师的高数", "body": "讲得清楚",
		"detail": map[string]any{
			"teacherName": "王老师",
			"courseName":  "高等数学",
			"rating":      5,
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("应 201，得到 %d %v", code, body)
	}
	d := body["data"].(map[string]any)["detail"].(map[string]any)
	if d["teacherName"] != "王老师" || d["rating"].(float64) != 5 {
		t.Fatalf("detail 未正确回传：%v", d)
	}
}

// TestDetail_MissingRequiredField 缺必填字段应被拒，并带上中文 label。
func TestDetail_MissingRequiredField(t *testing.T) {
	r, _, _, _ := newEnv(t)
	code, body := do(t, r, http.MethodPost, "/posts", "7", map[string]any{
		"board": "teacher_review", "title": "t", "body": "b",
		"detail": map[string]any{"teacherName": "王老师"}, // 缺 courseName / rating
	})
	if code != http.StatusBadRequest {
		t.Fatalf("缺必填字段应 400，得到 %d %v", code, body)
	}
	msg, _ := body["message"].(string)
	if msg == "" {
		t.Fatalf("错误信息应说明缺什么，得到 %v", body)
	}
}

// TestDetail_RejectsUnknownField 未声明的字段应被剔除（而不是报错）。
//
// 与「未知顶层字段丢弃」同口径：多传一个字段不该让整条发帖失败，
// 但它也不能落库——否则 detail 会变成任意 JSON 垃圾场。
func TestDetail_RejectsUnknownField(t *testing.T) {
	r, _, repo, _ := newEnv(t)
	code, _ := do(t, r, http.MethodPost, "/posts", "7", map[string]any{
		"board": "teacher_review", "title": "t", "body": "b",
		"detail": map[string]any{
			"teacherName": "王老师", "courseName": "高数", "rating": 4,
			"injectedField": "不该落库",
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("多传未知字段不该导致失败，得到 %d", code)
	}
	p, err := repo.FindByID(context.Background(), 1)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if _, has := p.Detail["injectedField"]; has {
		t.Fatal("未声明的字段必须被剔除，不能落库")
	}
}

// TestDetail_NonReviewTypeMustBeEmpty feedback/courtyard 带 detail 应被拒。
//
// 规则表里这两个类型没有行 = 「不接受任何结构化字段」。
// 若这里放行，就会出现「反馈帖挂着 teacherName」这种含义不明的数据。
func TestDetail_NonReviewTypeMustBeEmpty(t *testing.T) {
	r, _, _, _ := newEnv(t)
	code, body := do(t, r, http.MethodPost, "/posts", "7", map[string]any{
		"board": "feedback", "title": "t", "body": "b",
		"detail": map[string]any{"teacherName": "不该接受"},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("非评价类带 detail 应 400，得到 %d %v", code, body)
	}
	// 不带 detail 则应正常
	if code, _ := do(t, r, http.MethodPost, "/posts", "7", map[string]any{
		"board": "feedback", "title": "t2", "body": "b",
	}); code != http.StatusCreated {
		t.Fatalf("不带 detail 的反馈帖应 201，得到 %d", code)
	}
}

// TestDetail_IntRatingRejectsFraction 评分传 4.5 应被拒（不能静默截断）。
func TestDetail_IntRatingRejectsFraction(t *testing.T) {
	r, _, _, _ := newEnv(t)
	code, body := do(t, r, http.MethodPost, "/posts", "7", map[string]any{
		"board": "teacher_review", "title": "t", "body": "b",
		"detail": map[string]any{
			"teacherName": "王老师", "courseName": "高数", "rating": 4.5,
		},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("小数评分应被拒（不能截断成 4），得到 %d %v", code, body)
	}
}

// TestDetail_RatingOutOfRange 评分越界应被拒。
func TestDetail_RatingOutOfRange(t *testing.T) {
	r, _, _, _ := newEnv(t)
	for _, rating := range []int{0, 6, -1} {
		code, _ := do(t, r, http.MethodPost, "/posts", "7", map[string]any{
			"board": "teacher_review", "title": "t", "body": "b",
			"detail": map[string]any{
				"teacherName": "王老师", "courseName": "高数", "rating": rating,
			},
		})
		if code != http.StatusBadRequest {
			t.Fatalf("评分 %d 越界应 400，得到 %d", rating, code)
		}
	}
}

// TestDetail_CourseReview_TeacherOptional 课程评价的 teacherName 是选填。
//
// 这条同时验证「同一字段在不同类型下可以有不同的 required」——
// 正是用规则表而不是共享结构体的价值。
func TestDetail_CourseReview_TeacherOptional(t *testing.T) {
	r, _, _, _ := newEnv(t)
	code, body := do(t, r, http.MethodPost, "/posts", "7", map[string]any{
		"board": "course_review", "title": "t", "body": "b",
		"detail": map[string]any{"courseName": "大学物理", "rating": 4},
	})
	if code != http.StatusCreated {
		t.Fatalf("课程评价不填教师应成功，得到 %d %v", code, body)
	}
	d := body["data"].(map[string]any)["detail"].(map[string]any)
	if _, has := d["teacherName"]; has {
		t.Fatal("未填的选填字段不该出现在结果里")
	}
}

// TestDetail_UpdateRevalidates 编辑时重新校验（可改 detail）。
func TestDetail_UpdateRevalidates(t *testing.T) {
	r, _, _, _ := newEnv(t)
	code, body := do(t, r, http.MethodPost, "/posts", "7", map[string]any{
		"board": "teacher_review", "title": "t", "body": "b",
		"detail": map[string]any{"teacherName": "王老师", "courseName": "高数", "rating": 5},
	})
	if code != http.StatusCreated {
		t.Fatalf("创建失败：%d %v", code, body)
	}
	id := int64(body["data"].(map[string]any)["id"].(float64))
	path := "/posts/" + itoa(id)

	// 改成非法评分应被拒
	code, _ = do(t, r, http.MethodPatch, path, "7", map[string]any{
		"detail": map[string]any{"teacherName": "王老师", "courseName": "高数", "rating": 99},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("编辑成越界评分应 400，得到 %d", code)
	}

	// 合法修改应生效
	code, body = do(t, r, http.MethodPatch, path, "7", map[string]any{
		"detail": map[string]any{"teacherName": "王老师", "courseName": "高数", "rating": 3},
	})
	if code != http.StatusOK {
		t.Fatalf("合法编辑应 200，得到 %d %v", code, body)
	}
	d := body["data"].(map[string]any)["detail"].(map[string]any)
	if d["rating"].(float64) != 3 {
		t.Fatalf("评分未更新：%v", d)
	}
}

// TestDetail_NoRuleRepoFailsClosed 服务未配规则仓库时，带 detail 必须被拒。
//
// 「没有规则」不能被理解成「不校验」——那会让 detail 退化成任意 JSON。
func TestDetail_NoRuleRepoFailsClosed(t *testing.T) {
	// newEnv 配了 seed 规则；这里手工构造一个不配的。
	repo := store.NewPostMemoryRepo(func() time.Time { return t0 })
	svc := post.New(post.Options{Repo: repo, Clock: fixedClock{now: t0}})

	_, err := svc.Create(context.Background(), 7, post.CreateInput{
		Board: "teacher_review", Title: "t", Body: "b",
		Detail: map[string]any{"teacherName": "x"},
	})
	if err == nil {
		t.Fatal("未配规则仓库时带 detail 应被拒")
	}
	var de *apperr.Error
	if !errors.As(err, &de) || de.Code != apperr.CodeValidationFailed {
		t.Fatalf("应是 validation_failed，得到 %v", err)
	}

	// 不带 detail 则放行
	if _, err := svc.Create(context.Background(), 7, post.CreateInput{
		Board: "feedback", Title: "t", Body: "b",
	}); err != nil {
		t.Fatalf("不带 detail 应放行：%v", err)
	}
}

// TestDetail_RulesAreDataDriven 加一个新类型只需登记规则，不改代码。
//
// 这是本设计的存在理由：验证「管理员可扩」这件事在机制上真的成立。
func TestDetail_RulesAreDataDriven(t *testing.T) {
	rules := seedRules()
	// 模拟管理员新增一个类型（现实中是往 post_type_rule 插行）。
	rules.Set(post.Board("dorm_review"), []post.DetailRule{
		{Field: "dormName", ValueType: "string", Required: true, MinLen: 1, MaxLen: 32, Label: "宿舍楼"},
		{Field: "cleanRating", ValueType: "int", Required: true, MinValue: fp(1), MaxValue: fp(5), Label: "卫生评分"},
	})

	out, err := post.ValidateDetail(post.Board("dorm_review"), map[string]any{
		"dormName": "3号楼", "cleanRating": 4, "extra": "x",
	}, mustRules(t, rules, post.Board("dorm_review")))
	if err != nil {
		t.Fatalf("新类型校验应通过：%v", err)
	}
	if out["dormName"] != "3号楼" || out["cleanRating"] != int64(4) {
		t.Fatalf("规范化结果不符：%v", out)
	}
	if _, has := out["extra"]; has {
		t.Fatal("未声明字段应被剔除")
	}
}

func mustRules(t *testing.T, r post.DetailRuleRepo, b post.Board) []post.DetailRule {
	t.Helper()
	rs, err := r.RulesFor(context.Background(), b)
	if err != nil {
		t.Fatalf("读规则失败：%v", err)
	}
	return rs
}

func fp(v float64) *float64 { return &v }

// TestDetail_UnregisteredTypeIs400 未登记类型必须 400，不能是 500。
//
// 曾经的现象：类型未登记时一路走到 PG，被外键拦下（SQLSTATE 23503），
// 上层把它当内部错误映射成 500——把客户端的错误说成服务端故障，
// 既误导调用方也让监控误报。
func TestDetail_UnregisteredTypeIs400(t *testing.T) {
	r, _, _, _ := newEnv(t)
	code, body := do(t, r, http.MethodPost, "/posts", "7", map[string]any{
		"board": "not_registered", "title": "t", "body": "b",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("未登记类型应 400，得到 %d %v", code, body)
	}
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "未登记") && !strings.Contains(msg, "停用") {
		t.Fatalf("错误信息应说明类型未登记，得到 %q", msg)
	}
}

// TestDetail_RegisteredTypeWithoutRulesIsOK 「已登记但无规则」是合法状态。
//
// 这条与上一条成对：两者都返回零规则行，但必须区分对待。
// 若混为一谈，feedback 帖就发不出去了。
func TestDetail_RegisteredTypeWithoutRulesIsOK(t *testing.T) {
	rules := seedRules()
	known, err := rules.KnownType(context.Background(), post.BoardFeedback)
	if err != nil || !known {
		t.Fatalf("feedback 应算已登记：known=%v err=%v", known, err)
	}
	rs, _ := rules.RulesFor(context.Background(), post.BoardFeedback)
	if len(rs) != 0 {
		t.Fatalf("feedback 不该有字段规则，得到 %d 条", len(rs))
	}
}
