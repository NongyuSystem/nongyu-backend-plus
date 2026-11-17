// Package secondclass 的 dataType 受控词表：A 组 12 个读类数据类型。
//
// 规格 §6.8：A 组 13 个接口（12 读 + 1 写 saveReportCardSetting）按数据形态归并成
// 12 个 dataType；写类不建 dataType、不落缓存。这里是整个读子域的唯一事实源：
// 路由白名单、schema 校验、TTL、配方、审计 Target 全从这里读。
//
// 「必须过白名单」：URL 里的字符串一律先过 ParseDataType，未知名直接 400，
// 绝不把用户输入直接拼进缓存键（规格 §6.8 / §6.9）。
package secondclass

import "time"

// DataType 是二课回传数据的类型标识（受控词表）。
type DataType string

// A 组 12 个 dataType（规格 §6.8 表逐行）。
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

// TTL 四组（规格 §6.10）。数值由实施定，取各组建议区间的下界附近的整数值：
//   - 短   10~15 分钟 → 12 分钟（活动列表/详情变化快；A 组只有成绩单预览属此类）
//   - 中   30~60 分钟 → 45 分钟（诚信值）
//   - 长   6~12 小时  → 8 小时（学分、综测、排名、成绩单、基础字典）
//   - 最长 24 小时    → 24 小时（规则说明）
//
// 这组值**不是**教务的 10/30/60min 三档（探索笔记「不要照抄」第 4 条）。
// 与教务一致的是「Redis TTL 与新鲜度窗口是两套语义」：dt.TTL() 是新鲜度窗口，
// 真正写进 Redis 的存活时间是 redisRetention（见 cache.go）。
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

// KnownDataTypes 返回全部合法类型名（按 §6.8 表顺序，供测试与文档核对）。
func KnownDataTypes() []DataType {
	return []DataType{
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
	}
}
