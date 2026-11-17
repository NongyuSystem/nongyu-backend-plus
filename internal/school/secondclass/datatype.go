// Package secondclass 的 dataType 受控词表：A 组 12 个 + B 组 24 个读类数据类型。
//
// 规格 §6.8：A 组 13 个接口（12 读 + 1 写 saveReportCardSetting）按数据形态归并成
// 12 个 dataType；B 组 48 个接口里 35 个读接口按分组名归并为 24 个 dataType；
// 写类（A 组 1 + B 组 13）不建 dataType、不落缓存。这里是整个读子域的唯一事实源：
// 路由白名单、schema 校验、TTL、配方、审计 Target 全从这里读。
//
// 「必须过白名单」：URL 里的字符串一律先过 ParseDataType，未知名直接 400，
// 绝不把用户输入直接拼进缓存键（规格 §6.8 / §6.9）。
package secondclass

import "time"

// DataType 是二课回传数据的类型标识（受控词表）。
type DataType string

// A 组 12 个 dataType（规格 §6.8 表逐行）。#73 已落表，本票不改动这些条目。
const (
	DataTypeSecondclassCredit  DataType = "secondclassCredit"  // 学期学时总览（单对象）
	DataTypeApplyCredit        DataType = "applyCredit"        // 申请类学分
	DataTypeCreditRule         DataType = "creditRule"         // 学分规则说明
	DataTypeGoodFaithValue     DataType = "goodFaithValue"     // 诚信值
	DataTypeCreditRecordDetail DataType = "creditRecordDetail" // 学分明细（数组，两层嵌套）
	DataTypeImportCreditDetail DataType = "importCreditDetail" // 导入批次明细
	DataTypePerIntegrateDetail DataType = "perIntegrateDetail" // 综测明细
	DataTypeRanking            DataType = "ranking"            // 排名
	DataTypeReportCard         DataType = "reportCard"         // 成绩单
	DataTypeReportCardSetting  DataType = "reportCardSetting"  // 成绩单设置
	DataTypeCreditDetailOfType DataType = "creditDetailOfType" // 按类别学分明细
	DataTypeReportCardPreview  DataType = "reportCardPreview"  // 成绩单预览
)

// B 组 24 个 dataType（本票 #74 落表）。
//
// 覆盖 B 组 35 个读接口，按「同一 dataType 可服务多个接口」归并，不造重复 dataType：
//   - getActEvaluateTagList 出现两次（getevaluationlList / getevaluationlList_TagNamesBean）→ actEvaluateTagDict
//   - getActMemberList 有 actId / activityId 两版 → actMemberList
//   - getSchoolGroup 有 无参 / 多参 两版 → schoolGroupDict
//
// 命名沿用规格 §6.8 给的分组名（actDetail / actList / actMemberList / actEvalList /
// actTriviaList / actTypeDict / actLevelDict / actTagDict / actGroupDict / myCollectList /
// myActList / collegeClassInfo / actStatus / uploadResult 等），并对「等」里未点名的
// 读接口按语义补名（actEditDetail / actEvaluateTagDict / firstReviewPerson / gpsRange /
// groupSearchList / importMemberResult / schoolGroupDict / schoolLogo / uploadFileDetail /
// userSearchList）。写类 13 个（signInOrOut / addActEval / saveActivity / updateActivity ...）
// 不在此列。
const (
	DataTypeActDetail          DataType = "actDetail"          // 活动详情（单对象，含用户态字段）
	DataTypeActList            DataType = "actList"            // 活动列表/搜索/筛选（数组）
	DataTypeActMemberList      DataType = "actMemberList"      // 活动已报名成员（数组；actId/activityId 两版共用）
	DataTypeActEvalList        DataType = "actEvalList"        // 活动评价列表（数组）
	DataTypeActTriviaList      DataType = "actTriviaList"      // 活动花絮列表（数组）
	DataTypeActTypeDict        DataType = "actTypeDict"        // 我校活动类型字典（数组）
	DataTypeActLevelDict       DataType = "actLevelDict"       // 活动等级字典（数组）
	DataTypeActTagDict         DataType = "actTagDict"         // 活动标签字典（数组）
	DataTypeActGroupDict       DataType = "actGroupDict"       // 部落分组字典（数组）
	DataTypeMyCollectList      DataType = "myCollectList"      // 我的收藏活动（数组）
	DataTypeMyActList          DataType = "myActList"          // 我参与过的活动（数组）
	DataTypeCollegeClassInfo   DataType = "collegeClassInfo"   // 学院与班级信息（单对象）
	DataTypeActStatus          DataType = "actStatus"          // 活动状态/审核级别（单对象）
	DataTypeActEditDetail      DataType = "actEditDetail"      // 活动编辑/修改详情（单对象）
	DataTypeFirstReviewPerson  DataType = "firstReviewPerson"  // 初审人列表（数组）
	DataTypeGpsRange           DataType = "gpsRange"           // 签到 GPS 半径配置（数组）
	DataTypeGroupSearchList    DataType = "groupSearchList"    // 按名称搜部落（数组）
	DataTypeImportMemberResult DataType = "importMemberResult" // 导入成员结果（单对象）
	DataTypeSchoolGroupDict    DataType = "schoolGroupDict"    // 学校分组字典（数组；无参/多参两版共用）
	DataTypeSchoolLogo         DataType = "schoolLogo"         // 学校 Logo（单对象）
	DataTypeUploadFileDetail   DataType = "uploadFileDetail"   // 上传文件详情（单对象）
	DataTypeUploadResult       DataType = "uploadResult"       // 上传结果（单对象）
	DataTypeUserSearchList     DataType = "userSearchList"     // 按名称搜用户（数组）
	DataTypeActEvaluateTagDict DataType = "actEvaluateTagDict" // 评价标签字典（数组；两次接口共用）
)

// TTL 四组（规格 §6.10）。数值由实施定，取各组建议区间的下界附近的整数值：
//   - 短   10~15 分钟 → 12 分钟（活动列表/详情变化快；A 组只有成绩单预览属此类）
//   - 中   30~60 分钟 → 45 分钟（诚信值、活动评价）
//   - 长   6~12 小时  → 8 小时（学分、综测、排名、成绩单、基础字典）
//   - 最长 24 小时    → 24 小时（规则说明）
//
// 这组值**不是**教务的 10/30/60min 三档（探索笔记「不要照抄」第 4 条）。
// 与教务一致的是「Redis TTL 与新鲜度窗口是两套语义」：dt.TTL() 是新鲜度窗口，
// 真正写进 Redis 的存活时间是 redisRetention（见 cache.go）。
//
// B 组分派（票面「活动类多用短组，字典/规则类用长组」）：
//   - 短组：活动列表/详情/成员/花絮/状态/收藏/我的活动/搜索（名额状态变化快）
//   - 中组：活动评价
//   - 长组：各类字典/规则/基础数据（类型、等级、标签、分组、学院班级、Logo、上传配置等）
//   - 最长组：B 组不用（规则说明类仅 A 组 creditRule）
const (
	ttlShort   = 12 * time.Minute
	ttlMedium  = 45 * time.Minute
	ttlLong    = 8 * time.Hour
	ttlLongest = 24 * time.Hour
)

// dataShape 是 payload 的顶层形状（规格 §6.8「每个有 TTL 组与 shape」）。
//
// 三分法沿用教务的形状（但自己实现、不 import 教务）：
//   - shapeArray：payload 是数组
//   - shapeObject：payload 是单对象（可为 null）
//   - shapeWrapper：payload 是包装对象（顶层带自己的业务字段）
type dataShape int

const (
	shapeArray dataShape = iota
	shapeObject
	shapeWrapper
)

// dataTypeSpec 是每个数据类型的静态规格。
type dataTypeSpec struct {
	ttl   time.Duration
	shape dataShape
}

// dataTypeSpecs 是受控词表的权威表。增删类型必须同步改这里 + schema/*.json。
// TTL 组按规格 §6.8 表逐行；shape 按实测夹具与语义判定。
var dataTypeSpecs = map[DataType]dataTypeSpec{
	// ---- A 组 12 条（#73，原样保留）----
	DataTypeSecondclassCredit:  {ttl: ttlLong, shape: shapeObject},
	DataTypeApplyCredit:        {ttl: ttlLong, shape: shapeArray},
	DataTypeCreditRule:         {ttl: ttlLongest, shape: shapeWrapper},
	DataTypeGoodFaithValue:     {ttl: ttlMedium, shape: shapeWrapper},
	DataTypeCreditRecordDetail: {ttl: ttlLong, shape: shapeArray},
	DataTypeImportCreditDetail: {ttl: ttlLong, shape: shapeArray},
	DataTypePerIntegrateDetail: {ttl: ttlLong, shape: shapeWrapper},
	DataTypeRanking:            {ttl: ttlLong, shape: shapeWrapper},
	DataTypeReportCard:         {ttl: ttlLong, shape: shapeObject},
	DataTypeReportCardSetting:  {ttl: ttlLong, shape: shapeObject},
	DataTypeCreditDetailOfType: {ttl: ttlLong, shape: shapeArray},
	DataTypeReportCardPreview:  {ttl: ttlShort, shape: shapeObject},

	// ---- B 组 24 条（#74）----
	// 短组：活动类（名额/状态变化快）。
	DataTypeActDetail:       {ttl: ttlShort, shape: shapeObject},
	DataTypeActList:         {ttl: ttlShort, shape: shapeArray},
	DataTypeActMemberList:   {ttl: ttlShort, shape: shapeArray},
	DataTypeActTriviaList:   {ttl: ttlShort, shape: shapeArray},
	DataTypeActStatus:       {ttl: ttlShort, shape: shapeObject},
	DataTypeActEditDetail:   {ttl: ttlShort, shape: shapeObject},
	DataTypeMyCollectList:   {ttl: ttlShort, shape: shapeArray},
	DataTypeMyActList:       {ttl: ttlShort, shape: shapeArray},
	DataTypeGroupSearchList: {ttl: ttlShort, shape: shapeArray},
	DataTypeUserSearchList:  {ttl: ttlShort, shape: shapeArray},
	// 中组：活动评价。
	DataTypeActEvalList: {ttl: ttlMedium, shape: shapeArray},
	// 长组：字典/规则/基础数据。
	DataTypeActTypeDict:        {ttl: ttlLong, shape: shapeArray},
	DataTypeActLevelDict:       {ttl: ttlLong, shape: shapeArray},
	DataTypeActTagDict:         {ttl: ttlLong, shape: shapeArray},
	DataTypeActEvaluateTagDict: {ttl: ttlLong, shape: shapeArray},
	DataTypeActGroupDict:       {ttl: ttlLong, shape: shapeArray},
	DataTypeSchoolGroupDict:    {ttl: ttlLong, shape: shapeArray},
	DataTypeCollegeClassInfo:   {ttl: ttlLong, shape: shapeObject},
	DataTypeFirstReviewPerson:  {ttl: ttlLong, shape: shapeArray},
	DataTypeGpsRange:           {ttl: ttlLong, shape: shapeArray},
	DataTypeImportMemberResult: {ttl: ttlLong, shape: shapeObject},
	DataTypeSchoolLogo:         {ttl: ttlLong, shape: shapeObject},
	DataTypeUploadFileDetail:   {ttl: ttlLong, shape: shapeObject},
	DataTypeUploadResult:       {ttl: ttlLong, shape: shapeObject},
}

// ParseDataType 把外部字符串解析成受控的 DataType。
// 未知名字（含大小写不符、空串、疑似注入）一律返回 false，绝不宽容匹配。
func ParseDataType(s string) (DataType, bool) {
	dt := DataType(s)
	if _, ok := dataTypeSpecs[dt]; !ok {
		return "", false
	}
	return dt, true
}

// TTL 返回该数据类型的新鲜度窗口。
func (d DataType) TTL() time.Duration {
	if spec, ok := dataTypeSpecs[d]; ok {
		return spec.ttl
	}
	// 未知类型不进入缓存路径（ParseDataType 已经拦过）；给最长档兜底，保守不误判为新鲜。
	return ttlLongest
}

// Shape 返回 payload 的顶层形状。
func (d DataType) Shape() dataShape {
	if spec, ok := dataTypeSpecs[d]; ok {
		return spec.shape
	}
	return shapeArray
}

// KnownDataTypes 返回全部合法类型名（A 组 12 + B 组 24，供测试与文档核对）。
// NewSchemaValidator 用它反查「每个受控类型都必须有 schema」，漏一份即启动期失败。
func KnownDataTypes() []DataType {
	return []DataType{
		// ---- A 组 12 条（#73，原样保留）----
		DataTypeSecondclassCredit,
		DataTypeApplyCredit,
		DataTypeCreditRule,
		DataTypeGoodFaithValue,
		DataTypeCreditRecordDetail,
		DataTypeImportCreditDetail,
		DataTypePerIntegrateDetail,
		DataTypeRanking,
		DataTypeReportCard,
		DataTypeReportCardSetting,
		DataTypeCreditDetailOfType,
		DataTypeReportCardPreview,

		// ---- B 组 24 条（#74）----
		DataTypeActDetail,
		DataTypeActList,
		DataTypeActMemberList,
		DataTypeActEvalList,
		DataTypeActTriviaList,
		DataTypeActTypeDict,
		DataTypeActLevelDict,
		DataTypeActTagDict,
		DataTypeActGroupDict,
		DataTypeMyCollectList,
		DataTypeMyActList,
		DataTypeCollegeClassInfo,
		DataTypeActStatus,
		DataTypeActEditDetail,
		DataTypeFirstReviewPerson,
		DataTypeGpsRange,
		DataTypeGroupSearchList,
		DataTypeImportMemberResult,
		DataTypeSchoolGroupDict,
		DataTypeSchoolLogo,
		DataTypeUploadFileDetail,
		DataTypeUploadResult,
		DataTypeUserSearchList,
		DataTypeActEvaluateTagDict,
	}
}
