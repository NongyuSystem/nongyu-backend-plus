package post

import (
	"context"
	"strings"

	"nongyu-backend-plus/internal/apperr"
)

// 评价类查询的两个专用接口，以及它们允许用来过滤的字段白名单。
//
// 为什么需要白名单：detail 的键来自规则表，是**数据**不是编译期常量。
// 若直接让调用方传任意键，等于把「detail 里有哪些字段」暴露成公开 API——
// 一旦规则表变动，外部依赖会静默失效，也给了用内部字段探测数据的手段。
// 白名单让「对外承诺的筛选项」是一份明确的短清单。
var (
	// teacherQueryFields 是「查某老师评价」允许的过滤字段。
	teacherQueryFields = []string{"teacherName"}
	// teacherCourseQueryFields 是「查某老师某课程评价」允许的过滤字段。
	teacherCourseQueryFields = []string{"teacherName", "courseName"}
)

// TeacherReviews 查某位老师的全部评价（教师评价 + 课程评价两种类型）。
//
// 两种类型都可能有 teacherName 字段（课程评价里它是选填），所以这里
// board 传空 → 跨类型查。结果按发布时间倒序，游标分页。
func (s *Service) TeacherReviews(ctx context.Context, teacherName string, after PageCursor, limit int) (ListResult, error) {
	teacherName = strings.TrimSpace(teacherName)
	if teacherName == "" {
		return ListResult{}, apperr.Errorf(apperr.CodeValidationFailed, "teacherName 不能为空")
	}
	if len(teacherName) > MaxTeacherNameLen {
		return ListResult{}, apperr.Errorf(apperr.CodeValidationFailed, "teacherName 超长")
	}
	return s.listByDetail(ctx, "", map[string]string{"teacherName": teacherName}, after, limit)
}

// TeacherCourseReviews 查「某位老师的某门课程」的评价。
//
// 两个条件同时满足（AND）：teacherName 与 courseName 都命中。
func (s *Service) TeacherCourseReviews(ctx context.Context, teacherName, courseName string, after PageCursor, limit int) (ListResult, error) {
	teacherName = strings.TrimSpace(teacherName)
	courseName = strings.TrimSpace(courseName)
	if teacherName == "" {
		return ListResult{}, apperr.Errorf(apperr.CodeValidationFailed, "teacherName 不能为空")
	}
	if courseName == "" {
		return ListResult{}, apperr.Errorf(apperr.CodeValidationFailed, "courseName 不能为空")
	}
	if len(teacherName) > MaxTeacherNameLen {
		return ListResult{}, apperr.Errorf(apperr.CodeValidationFailed, "teacherName 超长")
	}
	if len(courseName) > MaxCourseNameLen {
		return ListResult{}, apperr.Errorf(apperr.CodeValidationFailed, "courseName 超长")
	}
	return s.listByDetail(ctx, "", map[string]string{
		"teacherName": teacherName,
		"courseName":  courseName,
	}, after, limit)
}

// MaxTeacherNameLen / MaxCourseNameLen 是查询入参的长度上限。
//
// 与规则表里的 max_len 保持一致（teacherName 64 / courseName 128），
// 但这里是**先挡住明显超长的入参**，避免把超长字符串送去数据库。
const (
	MaxTeacherNameLen = 64
	MaxCourseNameLen  = 128
)

// listByDetail 是两条专用接口的共用实现。
//
// 不复用 List：List 的分支（全站 / 按作者）与「按 detail 字段」是不同的
// 语义，混在一个函数里会让缓存键与越权判断都变复杂。这里**不走缓存**：
// 评价查询按老师/课程过滤，组合数远大于板块数，缓存命中率低而键空间膨胀快。
func (s *Service) listByDetail(ctx context.Context, board Board, match map[string]string, after PageCursor, limit int) (ListResult, error) {
	limit = normalizeLimit(limit)
	rows, err := s.repo.ListByDetail(ctx, board, match, after, limit)
	if err != nil {
		return ListResult{}, err
	}
	return assembleList(rows, limit), nil
}
