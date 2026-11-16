package jiaowu

import "nongyu-backend-plus/internal/school"

// pageRecipe 是某一 dataType 的直连取数说明。
//
// path 为空表示现有契约没有给出「不依赖用户输入」的完整路径。
// 这种类型不编地址：教室/教师查询要姓名或编号，编一个会变成假路径。
type pageRecipe struct {
	path   string
	parser string
}

// pageRecipes 的 path 只抄已写明的契约，不补未确认的 query。
//
// 培养方案只给第一跳 jjihua.asp?title_id1=1。后续页的隐藏域不能做成静态常量，
// 这里不编。来源：docs/tech/课程表与成绩查询可重写契约清单.md 与
// docs/tech/解析器施工级补充-4个待定解析器.md。
var pageRecipes = map[DataType]pageRecipe{
	DataTypeCourse:          {path: "/xuesheng/gongxuan/gongxuan/kbbanji.asp?title_id1=4", parser: "jiaowu-course-v1"},
	DataTypeScore:           {path: "/xuesheng/chengji/chengji/sear_ch_all.asp", parser: "jiaowu-score-v1"},
	DataTypeExam:            {path: "/xuesheng/kao/kao/xuesheng.asp?title_id1=01", parser: "jiaowu-exam-v1"},
	DataTypeProgress:        {path: "/xuesheng/chengji/xdjd/xuefen_2023.asp?title_id1=1", parser: "jiaowu-progress-v1"},
	DataTypeRank:            {path: "/xuesheng/chengji/chengji/zytongbf.asp", parser: "jiaowu-rank-v1"},
	DataTypePlan:            {path: "/xuesheng/jihua/jihua/jjihua.asp?title_id1=1", parser: "jiaowu-plan-v1"},
	DataTypePersonal:        {path: "/xuesheng/dangan/banji/bjiben.asp", parser: "jiaowu-personal-v1"},
	DataTypeNotice:          {path: "/web/web/web/index.asp", parser: "jiaowu-notice-v1"},
	DataTypeTeachingNotice:  {path: "/web/web/web/index.asp", parser: "jiaowu-teaching-notice-v1"},
	DataTypeCompetition:     {path: "/web/web/web/index.asp", parser: "jiaowu-competition-v1"},
	DataTypeKaike:           {path: "/xuesheng/gongxuan/gongxuan/kai.asp", parser: "jiaowu-kaike-v1"},
	DataTypeClassroomInfo:   {parser: "jiaowu-classroom-info-v1"},
	DataTypeTeacherInfo:     {parser: "jiaowu-teacher-info-v1"},
	DataTypeTeacherCourse:   {parser: "jiaowu-teacher-course-v1"},
	DataTypeClassroomCourse: {parser: "jiaowu-classroom-course-v1"},
}

// recipeOf 返回直连地址与解析器名。地址用白名单里的教务 host，不另写一套。
// 没有确认路径时 address 为空。
func recipeOf(dt DataType) (address, parser string) {
	spec, ok := pageRecipes[dt]
	if !ok {
		return "", ""
	}
	parser = spec.parser
	if spec.path == "" {
		return "", parser
	}
	tgt, err := school.Resolve(school.TargetJiaowu)
	if err != nil {
		return "", parser
	}
	return tgt.Direct.String() + spec.path, parser
}
