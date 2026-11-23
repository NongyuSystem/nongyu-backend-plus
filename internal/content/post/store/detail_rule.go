package store

import (
	"context"
	"sync"

	"nongyu-backend-plus/internal/content/post"
)

// DetailRuleMemoryRepo 是 DetailRuleRepo 的内存实现。
//
// 内存版用于单测与「没配 DATABASE_URL」的本地开发。它的行为必须与
// PG 版一致：**没有规则行的类型返回空切片而不是 nil，也不报错**——
// 「这个类型没有专属字段」是合法状态，不是错误。
type DetailRuleMemoryRepo struct {
	mu sync.RWMutex
	// rules 按类型分组；内层保持插入顺序（对校验顺序无影响，但对测试可读）。
	rules map[post.Board][]post.DetailRule
	// types 是「已登记类型」集合。
	//
	// 必须单独存：feedback/courtyard 是合法类型但**没有规则行**，
	// 若用「有规则行」推断是否已登记，它们会被误判成未登记。
	types map[post.Board]bool
}

// NewDetailRuleMemoryRepo 构造空的内存规则仓库。
func NewDetailRuleMemoryRepo() *DetailRuleMemoryRepo {
	return &DetailRuleMemoryRepo{
		rules: map[post.Board][]post.DetailRule{},
		types: map[post.Board]bool{},
	}
}

// RegisterType 登记一个类型（即使它没有专属字段）。
func (r *DetailRuleMemoryRepo) RegisterType(board post.Board) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.types[board] = true
}

// KnownType 实现 DetailRuleRepo。
func (r *DetailRuleMemoryRepo) KnownType(_ context.Context, board post.Board) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.types[board], nil
}

// Set 登记某类型的全部规则（测试/种子数据用）。同时把该类型标为已登记。
func (r *DetailRuleMemoryRepo) Set(board post.Board, rules []post.DetailRule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rules[board] = append([]post.DetailRule(nil), rules...)
	r.types[board] = true
}

// KnownType 实现 DetailRuleRepo：内存版以「是否 Set 过」为准。
//
// 注意 feedback/courtyard 在内存种子里也**没有**规则行，但它们必须算「已登记」。
// 所以单独维护一个 types 集合，而不是用「有规则行」推断。

func (r *DetailRuleMemoryRepo) RulesFor(_ context.Context, board post.Board) ([]post.DetailRule, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := r.rules[board]
	if out == nil {
		// 返回空切片而不是 nil：调用方据此判断「没有规则」，
		// nil 与空切片在 len() 上等价，但空切片更不容易被误当成「错误」。
		return []post.DetailRule{}, nil
	}
	return append([]post.DetailRule(nil), out...), nil
}

// NewSeedDetailRules 返回带上内置四种类型的内存规则仓库（与迁移里的种子一致）。
//
// 为什么代码里要复制一份迁移的种子数据：本地开发/单测常常不连库，
// 若只有迁移里有种子，代码路径就永远测不到「有规则」的情形。
// 两处一致由 TestSeedMatchesMigration 守着。
func NewSeedDetailRules() *DetailRuleMemoryRepo {
	r := NewDetailRuleMemoryRepo()

	r.Set(post.BoardTeacherReview, []post.DetailRule{
		{Field: "teacherName", ValueType: "string", Required: true, MinLen: 1, MaxLen: 64, Label: "教师姓名"},
		{Field: "courseName", ValueType: "string", Required: true, MinLen: 1, MaxLen: 128, Label: "课程名称"},
		{Field: "rating", ValueType: "int", Required: true, MinValue: floatPtr(1), MaxValue: floatPtr(5), Label: "评分（1-5）"},
	})
	r.Set(post.BoardCourseReview, []post.DetailRule{
		{Field: "courseName", ValueType: "string", Required: true, MinLen: 1, MaxLen: 128, Label: "课程名称"},
		// 授课教师选填：有些课（如通识大课）学生记不住具体老师。
		{Field: "teacherName", ValueType: "string", Required: false, MinLen: 1, MaxLen: 64, Label: "授课教师"},
		{Field: "rating", ValueType: "int", Required: true, MinValue: floatPtr(1), MaxValue: floatPtr(5), Label: "评分（1-5）"},
	})
	// feedback / courtyard：已登记但**不设规则行** → 它们的 detail 必须为空。
	r.RegisterType(post.BoardFeedback)
	r.RegisterType(post.BoardCourtyard)
	return r
}

func floatPtr(v float64) *float64 { return &v }

// 编译期确认。
var _ post.DetailRuleRepo = (*DetailRuleMemoryRepo)(nil)
