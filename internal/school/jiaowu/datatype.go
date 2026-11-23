// Package jiaowu 的缓存数据类型清单。
//
// 这里集中定义「回传数据的 15 个名字」与其 TTL，是整个缓存子域的唯一事实源：
// 路由白名单、schema 校验、TTL 取值、审计 Target 全部从这里读，避免四处散写。
//
// 名字取自 #41 的逐类清单（count 对账后的 15 个），不是票面早期写的 14 个：
// kaike 是第 15 个；notice 与 teachingNotice 同源但 result 形状相同，各自独立。
package jiaowu

import "time"

// DataType 是教务回传数据的类型标识。
//
// 它是**受控词表**：只有下面 15 个常量合法。任何来自 URL 的字符串都必须先过
// ParseDataType，未知名一律拒绝，绝不把用户输入直接当缓存键的一段拼进去。
type DataType string

const (
	DataTypeCourse          DataType = "course"          // 课表
	DataTypeScore           DataType = "score"           // 成绩
	DataTypeExam            DataType = "exam"            // 考试安排
	DataTypeProgress        DataType = "progress"        // 学业进度
	DataTypeRank            DataType = "rank"            // 加权排名
	DataTypePlan            DataType = "plan"            // 培养方案
	DataTypePersonal        DataType = "personal"        // 学籍信息
	DataTypeNotice          DataType = "notice"          // 教学通知 + 竞赛通知（合并视图）
	DataTypeTeachingNotice  DataType = "teachingNotice"  // 仅教学通知
	DataTypeCompetition     DataType = "competition"     // 仅竞赛通知
	DataTypeClassroomInfo   DataType = "classroomInfo"   // 教室搜索列表
	DataTypeTeacherInfo     DataType = "teacherInfo"     // 教师信息
	DataTypeTeacherCourse   DataType = "teacherCourse"   // 教师课表
	DataTypeClassroomCourse DataType = "classroomCourse" // 教室课表
	DataTypeKaike           DataType = "kaike"           // 开课目录
	// —— 评教（2026-11-23）——
	// 与其他 15 类不同，评教**不走缓存**（见 ttlNone 的说明），
	// 且提交是**写操作**。两者的地址都由后端产出、由客户端自己发请求。
	DataTypeEvaluation       DataType = "evaluation"       // 评教待评列表
	DataTypeEvaluationSubmit DataType = "evaluationSubmit" // 评教提交地址（只给地址）
)

// TTL 分组。取值依据 #32 决议：
//   - 高频变化（通知类）10 分钟
//   - 中频（考试安排）30 分钟
//   - 低频/稳定（其余 11 类）1 小时
//
// 注意这**不是**旧仓客户端 staleTime 的照搬：那个语义是「多久后重取、仍可读旧值」，
// 而服务端 TTL 是「多久后缓存消失、变 cache_miss」（#32 明确纠正过）。
const (
	ttlHighFrequency = 10 * time.Minute
	ttlMedium        = 30 * time.Minute
	ttlStable        = time.Hour
	// ttlNone 表示**不缓存**。
	//
	// 为什么需要这个值而不是「用 0 兜底」： 在既有代码里意味着
	// 「没配 TTL」，而 TTL() 对未知类型返回 ttlStable 兜底。若评教也用 0，
	// 它会静默落进「一小时缓存」——而评教数据每提交一次就变，
	// 缓存会让用户看到「已评价」却还能再评，或反之。
	// 用一个显式的负数把「明确不缓存」与「未配置」区分开。
	ttlNone = time.Duration(-1)
)

// dataTypeSpec 是每个数据类型的静态规格。
type dataTypeSpec struct {
	ttl time.Duration
	// shape 记录顶层形状，供 schema 与文档对齐；也用于「这个类型是不是数组」的快速判断。
	shape dataShape
}

// dataShape 是 payload 的顶层形状。
//
// #41 查明 15 类不是同一种形状，分三种：
//   - 数组：8 类（course/score/exam/progress/notice/teachingNotice/competition/classroomInfo）
//   - 单对象：3 类（rank/personal/teacherInfo），可为 null
//   - 包装对象：4 类（plan/kaike/teacherCourse/classroomCourse）
type dataShape int

const (
	shapeArray dataShape = iota
	shapeObject
	shapeWrapper
)

// dataTypeSpecs 是受控词表的权威表。增删数据类型必须同步改这里 + schema/*.json。
var dataTypeSpecs = map[DataType]dataTypeSpec{
	DataTypeCourse:          {ttl: ttlStable, shape: shapeArray},
	DataTypeScore:           {ttl: ttlStable, shape: shapeArray},
	DataTypeExam:            {ttl: ttlMedium, shape: shapeArray},
	DataTypeProgress:        {ttl: ttlStable, shape: shapeArray},
	DataTypeRank:            {ttl: ttlStable, shape: shapeObject},
	DataTypePlan:            {ttl: ttlStable, shape: shapeWrapper},
	DataTypePersonal:        {ttl: ttlStable, shape: shapeObject},
	DataTypeNotice:          {ttl: ttlHighFrequency, shape: shapeArray},
	DataTypeTeachingNotice:  {ttl: ttlHighFrequency, shape: shapeArray},
	DataTypeCompetition:     {ttl: ttlHighFrequency, shape: shapeArray},
	DataTypeClassroomInfo:   {ttl: ttlStable, shape: shapeArray},
	DataTypeTeacherInfo:     {ttl: ttlStable, shape: shapeObject},
	DataTypeTeacherCourse:   {ttl: ttlStable, shape: shapeWrapper},
	DataTypeClassroomCourse: {ttl: ttlStable, shape: shapeWrapper},
	DataTypeKaike:           {ttl: ttlStable, shape: shapeWrapper},
	// 评教：不缓存（ttlNone）。shape 记 object 是因为它下发的不是业务数据，
	// 而是「地址 + 解析器 + 缺入参说明」这种取数说明。
	DataTypeEvaluation:       {ttl: ttlNone, shape: shapeObject},
	DataTypeEvaluationSubmit: {ttl: ttlNone, shape: shapeObject},
}

// ParseDataType 把外部字符串解析成受控的 DataType。
// 未知名字（含大小写不符、空串、疑似注入）一律返回 false，绝不宽容匹配。
func ParseDataType(s string) (DataType, bool) {
	dt := DataType(s)
	spec, ok := dataTypeSpecs[dt]
	if !ok {
		return "", false
	}
	_ = spec
	return dt, true
}

// TTL 返回该数据类型的缓存存活时间。
func (d DataType) TTL() time.Duration {
	if spec, ok := dataTypeSpecs[d]; ok {
		return spec.ttl
	}
	// 未知类型不进入缓存路径（ParseDataType 已经拦过）；这里给保守默认值兜底。
	return ttlStable
}

// Shape 返回 payload 的顶层形状。
func (d DataType) Shape() dataShape {
	if spec, ok := dataTypeSpecs[d]; ok {
		return spec.shape
	}
	return shapeArray
}

// KnownDataTypes 返回全部合法类型名（按声明顺序无关，仅供测试与文档核对）。
func KnownDataTypes() []DataType {
	all := make([]DataType, 0, len(dataTypeSpecs))
	for _, d := range []DataType{
		DataTypeCourse, DataTypeScore, DataTypeExam, DataTypeProgress, DataTypeRank,
		DataTypePlan, DataTypePersonal, DataTypeNotice, DataTypeTeachingNotice,
		DataTypeCompetition, DataTypeClassroomInfo, DataTypeTeacherInfo,
		DataTypeTeacherCourse, DataTypeClassroomCourse, DataTypeKaike,
	} {
		all = append(all, d)
	}
	return all
}
