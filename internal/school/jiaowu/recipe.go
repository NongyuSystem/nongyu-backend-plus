package jiaowu

import (
	"strings"

	"nongyu-backend-plus/internal/school"
)

// pageRecipe 是某一 dataType 的取数说明。
//
// path 为空表示现有契约没有给出「不依赖用户输入」的完整路径。
// 这种类型不编地址：教室/教师查询要姓名或编号，编一个会变成假路径。
type pageRecipe struct {
	path   string
	parser string
}

// AccessMode 是这一次取数该用哪种地址形态。
//
// 判定方是**客户端**，不是后端：教务站只在校内网可达，而「学生此刻在校内还是校外」
// 只有客户端知道。后端出口在校内也推不出学生位置。
type AccessMode string

const (
	// AccessDirect 走 https://jiaowu.sicau.edu.cn 直连，只在校内可达。
	AccessDirect AccessMode = "direct"
	// AccessVPN 走 https://webvpn.sicau.edu.cn 编码地址，校外可达。
	AccessVPN AccessMode = "vpn"
)

// HeaderCampusNetwork 是客户端声明「能不能直连校园网」的请求头。
//
// 取值：direct / campus / 1 / true 表示校内（走直连）；
// vpn / 0 / false 表示校外（走编码地址）。大小写不敏感，两侧空白忽略。
// 头缺失或取值无法识别时回落到 AccessVPN：PRD 的前提是「离校后必须经网关」，
// 校外是常见情形，按校外处理不会让常见用户拿到一个必然失败的地址。
const HeaderCampusNetwork = "X-Nongyu-Campus-Network"

// AccessModeFromHeader 把请求头翻译成地址形态。
//
// 返回 ok=false 表示「没声明或声明不可识别」，调用方应使用回落值而不是猜。
func AccessModeFromHeader(raw string) (AccessMode, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "direct", "campus", "intranet", "1", "true":
		return AccessDirect, true
	case "vpn", "webvpn", "0", "false":
		return AccessVPN, true
	default:
		return "", false
	}
}

// accessModeOf 返回本次应使用的形态：头能识别就用它，否则回落 vpn。
func accessModeOf(raw string) AccessMode {
	if mode, ok := AccessModeFromHeader(raw); ok {
		return mode
	}
	return AccessVPN
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

// recipeOf 返回该形态下的地址与解析器名。
//
//   - AccessDirect：白名单 host 的直连地址；
//   - AccessVPN：同一个 host 经纯函数 Encode 产出的编码地址（网关形态）。
//
// 两种形态都从 school.Resolve 出发，不另写一套 host 常量。没有确认路径时 address 为空。
func recipeOf(dt DataType, mode AccessMode) (address, parser string) {
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
	if mode == AccessDirect {
		return tgt.Direct.String() + spec.path, parser
	}
	// 编码形态必须「先拼完整直连 URL 再 Encode」：tgt.Vpn 是 Encode(无 path 的 host)
	// 的结果，Encode 对无 path 的地址会补 "/"，直接接 path 会变成 "...e0//xuesheng/..."。
	// 与 Service.Address 的拼法保持一致，路径因此仍是明文。
	encoded, err := school.Encode(tgt.Direct.String() + spec.path)
	if err != nil {
		return "", parser
	}
	return encoded, parser
}
