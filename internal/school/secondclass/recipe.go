// Package secondclass 的请求配方表与上游 code 解析。
//
// 配方是「告诉客户端怎么直连二课平台」的静态说明（规格 §6.3）：只给槽位不给值，
// 凭据 x-access-token 由客户端自带（from: client），后端零出站、零凭据。
// 本包不 import 教务域，也不复用教务的 pageRecipes（那是 WebVPN 形态，字段集不同）。
package secondclass

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// Recipe 是一条请求配方（规格 §6.3 的七/八字段）。
//
// 字段名与 JSON 形状直接对应 §6.3，外加 §6.4 参数槽位、§6.5 文件字段、
// §6.6 必需请求头、§6.7 多跳 steps。相对教务侧**不含** parser/cookies/session：
// 二课是 JSON API、凭据在 header、后端不建会话（探索笔记「不要照抄」第 1 条）。
type Recipe struct {
	ID          string         `json:"id"`
	Target      string         `json:"target"`
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	ContentType string         `json:"contentType"`
	Auth        RecipeAuth     `json:"auth"`
	Params      []RecipeParam  `json:"params"`
	Fields      []RecipeField  `json:"fields"`
	Headers     []RecipeHeader `json:"headers"`
	Steps       []RecipeStep   `json:"steps,omitempty"`
}

// RecipeAuth 表达凭据放哪、叫什么、谁提供。from 恒为 client（后端不持有凭据）。
type RecipeAuth struct {
	In   string `json:"in"`
	Name string `json:"name"`
	From string `json:"from"`
}

// RecipeParam 是一个参数槽位（规格 §6.4）。只给名字与来源，不给值。
type RecipeParam struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Origin   string `json:"origin"` // client_local | fixed | optional
	Note     string `json:"note,omitempty"`
}

// RecipeField 是 multipart 文件字段（规格 §6.5）。A 组读配方用不到，恒空数组。
type RecipeField struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Kind     string `json:"kind"`
	Note     string `json:"note,omitempty"`
}

// RecipeHeader 是必需请求头（规格 §6.6）。value 为固定常量；client_local 的头
// （client-long / client-lat）不在配方里编值，只声明名字。
type RecipeHeader struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// RecipeStep 是多跳引用（规格 §6.7）。A 组读配方默认单跳，恒为空。
type RecipeStep struct {
	Ref        string            `json:"ref"`
	ParamsFrom map[string]string `json:"params_from,omitempty"`
}

// clientAuth 是全部二课配方共用的凭据声明：header x-access-token，客户端持有。
var clientAuth = RecipeAuth{In: "header", Name: "x-access-token", From: "client"}

// 规格 §6.6 实测必需的 7 个设备头。
// pu-version 等固定项给常量；client-long / client-lat 属 client_local，配方只给名字
// 不给值（GPS 坐标由客户端现场产生，后端绝不代填——规格 §6.4 硬约束）。
// 注意：x-access-token 不在 headers 里，它由 auth 表达，且值全禁入响应（规格 §3.6）。
var deviceHeaders = []RecipeHeader{
	{Name: "pu-version", Value: "12.10.0.10010599"},
	{Name: "client-dev-brand"},
	{Name: "client-dev-type"},
	{Name: "client-os-type", Value: "android"},
	{Name: "client-os-version"},
	{Name: "client-long"},
	{Name: "client-lat"},
}

// pageRecipes 是 A 组 12 个 dataType 的静态配方表（规格 §6.8 覆盖接口 + §6.3 形状）。
//
// path 取自一手契约的 A 组逐条路径，保留上游原样：
//   - importCreditDetail 是 /act//credit/...（双斜杠，不得「修正」）；
//   - reportCardPreview 是相对路径 integrate/v1.0.1/previewReportCard（无 /act/ 前缀）。
//
// 「缺参不编假值」：需要客户端现场产生的参数（page/typeId/flag 等）只给槽位，
// 不给具体数值（探索笔记 §1.5 的可复用思路）。
var pageRecipes = map[DataType]Recipe{
	DataTypeSecondclassCredit: {
		ID: "secondclass.credit.getSecondActivityCredit", Target: "secondclass",
		Method: "POST", Path: "/act/credit/v1.0.0/getSecondActivityCredit",
		ContentType: "form", Auth: clientAuth, Params: []RecipeParam{},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeApplyCredit: {
		ID: "secondclass.credit.getApplyCredit", Target: "secondclass",
		Method: "POST", Path: "/act/credit/v1.0.0/getApplyCredit",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{{Name: "page", Origin: "optional", Note: "分页页码"}},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeCreditRule: {
		ID: "secondclass.credit.getCreditRuleExplain", Target: "secondclass",
		Method: "POST", Path: "/act/credit/v1.0.0/getCreditRuleExplain",
		ContentType: "form", Auth: clientAuth, Params: []RecipeParam{},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeGoodFaithValue: {
		ID: "secondclass.credit.getGoodFaithValue", Target: "secondclass",
		Method: "POST", Path: "/act/credit/v1.0.0/getGoodFaithValue",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{{Name: "page", Origin: "optional", Note: "分页页码"}},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeCreditRecordDetail: {
		ID: "secondclass.credit.getCreditRecordDetail", Target: "secondclass",
		Method: "POST", Path: "/act/integrate/v1.0.0/getCreditRecordDetail",
		ContentType: "form", Auth: clientAuth, Params: []RecipeParam{},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeImportCreditDetail: {
		ID: "secondclass.credit.getImportCreditDetail", Target: "secondclass",
		Method: "POST", Path: "/act//credit/v1.0.0/getImportCreditDetail", // 上游双斜杠，原样保留
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{{Name: "page", Origin: "optional", Note: "分页页码"}},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypePerIntegrateDetail: {
		ID: "secondclass.integrate.getPerIntegrateDetail", Target: "secondclass",
		Method: "POST", Path: "/act/integrate/v1.0.0/getPerIntegrateDetail",
		ContentType: "form", Auth: clientAuth, Params: []RecipeParam{},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeRanking: {
		ID: "secondclass.integrate.getRanking", Target: "secondclass",
		Method: "POST", Path: "/act/integrate/v1.0.0/getRanking",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "activityType", Origin: "optional", Note: "活动类型筛选"},
			{Name: "term", Origin: "optional", Note: "学期筛选"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeReportCard: {
		ID: "secondclass.reportCard.myReportCard", Target: "secondclass",
		Method: "POST", Path: "/act/integrate/v1.0.0/myReportCard",
		ContentType: "form", Auth: clientAuth, Params: []RecipeParam{},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeReportCardSetting: {
		ID: "secondclass.reportCard.settingView", Target: "secondclass",
		Method: "POST", Path: "/act/integrate/v1.0.0/reportCardSettingView",
		ContentType: "form", Auth: clientAuth, Params: []RecipeParam{},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeCreditDetailOfType: {
		ID: "secondclass.integrate.getCreditDetailOfType", Target: "secondclass",
		Method: "POST", Path: "/act/integrate/v1.0.0/getCreditDetailOfType",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{{Name: "typeId", Required: true, Origin: "optional", Note: "类别 id，客户端按用户所选类别填入"}},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeReportCardPreview: {
		ID: "secondclass.reportCard.preview", Target: "secondclass",
		Method: "POST", Path: "integrate/v1.0.1/previewReportCard", // 上游相对路径，无 /act/ 前缀
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "scoreDisplayFlag", Origin: "optional", Note: "成绩单展示标志"},
			{Name: "myChartShow", Origin: "optional", Note: "图表展示标志"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},

	// ---- B 组 24 个 dataType 的读配方（code-review BLOCKER-1 补，防 cache_miss 下发空配方）----
	// path 取自一手清单 datatype_b_test.go 的 bGroupReadEndpoints（ALL_API.md B 组 35 个读接口）：
	// 多个接口归并到同一 dataType 时只取该 dataType 的主接口路径，归并处逐条注明。
	// 全部 POST（一手清单 B 组 48 接口均为 @POST，无 GET 特例）；Auth 用 clientAuth、
	// Headers 用 deviceHeaders（7 头）。参数只给槽位、绝不编具体值；client_local 只声明名字，
	// 值由客户端现场产生。与 A 组一样不发任何出站、不含凭据值。
	DataTypeActDetail: {
		ID: "secondclass.act.getActDetail", Target: "secondclass",
		Method: "POST", Path: "/act/actInfo/v1.0.0/getActDetail", // #6
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{{Name: "actId", Required: true, Origin: "client_local", Note: "活动 id，客户端按所选活动填入"}},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeActList: {
		// 主接口取 #14 getActivityByName；同组的 #17 getMyActivitySearchList / #25
		// getGroupActivityList / #37 getUserSchoolActList / #43 selectBeSignAdminActList
		// 也归到 actList，按「一个 dataType 一条主配方」只取其一。
		ID: "secondclass.act.getActivityByName", Target: "secondclass",
		Method: "POST", Path: "/query/solrAct/v1.0.0/getActivityByName",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "title", Origin: "client_local", Note: "活动名称关键字，客户端现场输入"},
			{Name: "page", Origin: "client_local", Note: "分页页码，客户端现场产生"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeActMemberList: {
		// 去重（票面点名）：#27 actId 版 / #28 activityId 版两接口共用本 dataType，
		// 主接口取 #27 actId 版。
		ID: "secondclass.member.getActMemberList", Target: "secondclass",
		Method: "POST", Path: "/act/actInfo/v1.0.0/getActMemberList",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "actId", Required: true, Origin: "client_local", Note: "活动 id，客户端按所选活动填入"},
			{Name: "page", Origin: "client_local", Note: "分页页码，客户端现场产生"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeActEvalList: {
		// 主接口取 #20 getEvalua；#7 getActEvalList 也归到 actEvalList。
		ID: "secondclass.eval.getActEvalList", Target: "secondclass",
		Method: "POST", Path: "/act/eval/v1.0.0/getActEvalList",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "actId", Required: true, Origin: "client_local", Note: "活动 id，客户端按所选活动填入"},
			{Name: "page", Origin: "client_local", Note: "分页页码，客户端现场产生"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeActTriviaList: {
		ID: "secondclass.trivia.getActTriviaList", Target: "secondclass",
		Method: "POST", Path: "/act/tidbit/v1.0.0/getActTriviaList", // #12
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "actId", Required: true, Origin: "client_local", Note: "活动 id，客户端按所选活动填入"},
			{Name: "page", Origin: "client_local", Note: "分页页码，客户端现场产生"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeActTypeDict: {
		ID: "secondclass.dict.getSchoolActType", Target: "secondclass",
		Method: "POST", Path: "/act/actType/v1.0.0/getSchoolActType", // #13 / #30 同路径
		ContentType: "form", Auth: clientAuth, Params: []RecipeParam{},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeActLevelDict: {
		ID: "secondclass.dict.getActLevelList", Target: "secondclass",
		Method: "POST", Path: "/act/actLevel/v1.0.0/getActLevelList", // #9
		ContentType: "form", Auth: clientAuth, Params: []RecipeParam{},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeActTagDict: {
		ID: "secondclass.dict.getActTagList", Target: "secondclass",
		Method: "POST", Path: "/act/actTag/v1.0.0/getActTagList", // #11
		ContentType: "form", Auth: clientAuth, Params: []RecipeParam{},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeActGroupDict: {
		ID: "secondclass.dict.getActGroupList", Target: "secondclass",
		Method: "POST", Path: "/act/actGroup/v1.0.0/getActGroupList", // #8
		ContentType: "form", Auth: clientAuth, Params: []RecipeParam{},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeMyCollectList: {
		ID: "secondclass.collect.getActivityForMyCollect", Target: "secondclass",
		Method: "POST", Path: "/act/actInfo/v1.0.0/getActivityForMyCollect", // #21
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{{Name: "page", Origin: "client_local", Note: "分页页码，客户端现场产生"}},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeMyActList: {
		ID: "secondclass.act.getActForUserMyList", Target: "secondclass",
		Method: "POST", Path: "/act/actInfo/v1.0.0/getActForUserMyList", // #29
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{{Name: "page", Origin: "client_local", Note: "分页页码，客户端现场产生"}},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeCollegeClassInfo: {
		ID: "secondclass.org.getCollegeAndClassInfoByUser", Target: "secondclass",
		Method: "POST", Path: "/act/sysOrg/v1.0.0/getCollegeAndClassInfoByUser", // #19
		ContentType: "form", Auth: clientAuth, Params: []RecipeParam{},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeActStatus: {
		// 主接口取 #10 getActStatusById；#18 getActivityVerifierList 也归到 actStatus。
		ID: "secondclass.act.getActStatusById", Target: "secondclass",
		Method: "POST", Path: "actInfo/v1.0.0/getActStatusById",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{{Name: "actId", Required: true, Origin: "client_local", Note: "活动 id，客户端按所选活动填入"}},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeActEditDetail: {
		// 主接口取 #15 getActivityEditDetail；#16 getActivityModifyDetail 也归到 actEditDetail。
		ID: "secondclass.act.getActivityEditDetail", Target: "secondclass",
		Method: "POST", Path: "activity/v1.0.0/getActivityEditDetail",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{{Name: "activityId", Required: true, Origin: "client_local", Note: "活动 id，客户端按所选活动填入"}},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeFirstReviewPerson: {
		ID: "secondclass.review.getFirstReviewPerson", Target: "secondclass",
		Method: "POST", Path: "/act/actconfig/v1.0.0/getFirstReviewPerson", // #22
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{{Name: "gid", Required: true, Origin: "client_local", Note: "部落 id，客户端按所选部落填入"}},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeGpsRange: {
		ID: "secondclass.config.getGpsR", Target: "secondclass",
		Method: "POST", Path: "actInfo/v1.0.0/getGpsR", // #23（一手清单此接口无 @Field）
		ContentType: "form", Auth: clientAuth, Params: []RecipeParam{},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeGroupSearchList: {
		ID: "secondclass.group.getGroupByTitle", Target: "secondclass",
		Method: "POST", Path: "/query/solrGroup/v1.0.0/getGroupByTitle", // #24
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "title", Origin: "client_local", Note: "部落名称关键字，客户端现场输入"},
			{Name: "page", Origin: "client_local", Note: "分页页码，客户端现场产生"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeImportMemberResult: {
		ID: "secondclass.scan.getImportMemberResult", Target: "secondclass",
		Method: "POST", Path: "scanCode/v1.0.0/getImportMemberResult", // #26
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{{Name: "uuid", Required: true, Origin: "client_local", Note: "导入批次 uuid，客户端现场产生"}},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeSchoolGroupDict: {
		// 去重（票面点名）：#31 无参版 / #32 group/v1.0.0/getSchoolGroup 多参版
		// 两接口共用本 dataType，主接口取 #31 无参版。
		ID: "secondclass.group.getSchoolGroup", Target: "secondclass",
		Method: "POST", Path: "/act/actconfig/v1.0.0/getSchoolGroup",
		ContentType: "form", Auth: clientAuth, Params: []RecipeParam{},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeSchoolLogo: {
		ID: "secondclass.school.getSchoolLogo", Target: "secondclass",
		Method: "POST", Path: "/act/actInfo/v1.0.0/getSchoolLogo", // #33
		ContentType: "form", Auth: clientAuth, Params: []RecipeParam{},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeUploadFileDetail: {
		ID: "secondclass.scan.getUploadFileDetail", Target: "secondclass",
		Method: "POST", Path: "scanCode/v1.0.0/getUploadFileDetail", // #34
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{{Name: "activityId", Required: true, Origin: "client_local", Note: "活动 id，客户端按所选活动填入"}},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeUploadResult: {
		ID: "secondclass.scan.getUploadResult", Target: "secondclass",
		Method: "POST", Path: "scanCode/v1.0.0/getUploadResult", // #35
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{{Name: "uuid", Required: true, Origin: "client_local", Note: "上传批次 uuid，客户端现场产生"}},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeUserSearchList: {
		ID: "secondclass.user.getUserByName", Target: "secondclass",
		Method: "POST", Path: "/query/solrUser/v1.0.0/getUserByName", // #36
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "nameKey", Origin: "client_local", Note: "用户姓名关键字，客户端现场输入"},
			{Name: "cid", Origin: "client_local", Note: "筛选 id，客户端现场填入"},
			{Name: "sex", Origin: "client_local", Note: "性别筛选，客户端现场填入"},
			{Name: "page", Origin: "client_local", Note: "分页页码，客户端现场产生"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	DataTypeActEvaluateTagDict: {
		// 去重（票面点名）：#38 getevaluationlList / #39 getevaluationlList_TagNamesBean
		// 两接口共用本 dataType，路径相同。
		ID: "secondclass.dict.getActEvaluateTagList", Target: "secondclass",
		Method: "POST", Path: "/act/actTag/v1.0.0/getActEvaluateTagList",
		ContentType: "form", Auth: clientAuth, Params: []RecipeParam{},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
}

// RecipeFor 按 dataType 取配方。未知类型返回 false（调用方绝不能把 :dataType 拼进别的键）。
func RecipeFor(dt DataType) (Recipe, bool) {
	r, ok := pageRecipes[dt]
	return r, ok
}

// writeRecipes 是写类 14 个动作的静态配方表（#75；决策-二课写类配方契约.md §二）。
//
// 与 pageRecipes 的根本区别：写操作不经过 GET，没有「cache_miss」这个下发时机，
// 所以走独立端点 GET /school/secondclass/recipe/:recipeId，按稳定 id 取配方。
//
// 「槽位而不是值」（规格 §6.4 第一性理由）：报名/签到要的 actId、qrCodeTimestamp、
// GPS 坐标、codeTime、gpsCode 全由客户端现场产生，后端既没有值也不该有。故每条
// 参数只声明 {name, required, origin, note}，绝不编任何具体值；RecipeParam 类型本身
// 也没有存值的字段。origin 归属按参数语义：现场值一律 client_local，成绩单展示 flag
// 这类「客户端可传可不传」的配置项为 optional。
//
// path 逐字取自一手清单 ALL_API.md：不带前导 / 的相对路径原样保留（上游 getGsonApi
// 前缀由客户端拼），双斜杠、大小写拼写（signUpLong / signupLong 两种）一律不「修正」。
// Steps 全留空：默认单跳，多跳只表达「引用 + 取值路径」、不驱动控制流（§6.7）。
var writeRecipes = map[string]Recipe{
	// ---- B 组 13 个写操作 ----
	"secondclass.activity.signInOrOutByQrCode": {
		ID: "secondclass.activity.signInOrOutByQrCode", Target: "secondclass",
		Method: "POST", Path: "activity/v2.0.0/signInOrOut", // 相对路径，上游原样
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "activityId", Required: true, Origin: "client_local", Note: "活动 id，客户端按用户所选活动填入"},
			{Name: "qrCodeTimestamp", Required: true, Origin: "client_local", Note: "扫码瞬间的毫秒时间戳，必须由客户端现场生成"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	"secondclass.activity.signInOrOut": {
		ID: "secondclass.activity.signInOrOut", Target: "secondclass",
		Method: "POST", Path: "/act/actInfo/v1.0.0/signInOrOut",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "actId", Required: true, Origin: "client_local", Note: "活动 id，客户端按用户所选活动填入"},
			{Name: "memberId", Required: true, Origin: "client_local", Note: "成员 id，客户端现场提供"},
			{Name: "codeTime", Required: true, Origin: "client_local", Note: "扫码时刻，必须由客户端现场生成"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	"secondclass.member.updateStatus": {
		ID: "secondclass.member.updateStatus", Target: "secondclass",
		Method: "POST", Path: "actInfo/v1.0.0/updateActMemberStatus",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "actId", Required: true, Origin: "client_local", Note: "活动 id，客户端现场提供"},
			{Name: "memberId", Required: true, Origin: "client_local", Note: "成员 id，客户端现场提供"},
			{Name: "validCode", Required: true, Origin: "client_local", Note: "校验码，客户端现场提供"},
			{Name: "gpsCode", Required: true, Origin: "client_local", Note: "GPS 校验码，必须由客户端现场生成"},
			{Name: "status", Required: true, Origin: "client_local", Note: "审核状态，客户端现场提供"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	"secondclass.eval.add": {
		ID: "secondclass.eval.add", Target: "secondclass",
		Method: "POST", Path: "eval/v1.0.0/addActEval",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "actId", Required: true, Origin: "client_local", Note: "活动 id，客户端现场提供"},
			{Name: "tags", Required: true, Origin: "client_local", Note: "评价标签，用户现场填写"},
			{Name: "content", Required: true, Origin: "client_local", Note: "评价文字，用户现场填写"},
			{Name: "score", Required: true, Origin: "client_local", Note: "评分，用户现场填写"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	"secondclass.activity.edit": {
		ID: "secondclass.activity.edit", Target: "secondclass",
		Method: "POST", Path: "/act/activity/v1.0.0/ActivityEdit",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "activityId", Required: true, Origin: "client_local", Note: "活动 id，客户端现场提供"},
			{Name: "signupStartTime", Required: true, Origin: "client_local", Note: "报名开始时间，客户端现场提供"},
			{Name: "signupEndTime", Required: true, Origin: "client_local", Note: "报名结束时间，客户端现场提供"},
			{Name: "startTime", Required: true, Origin: "client_local", Note: "活动开始时间，客户端现场提供"},
			{Name: "endTime", Required: true, Origin: "client_local", Note: "活动结束时间，客户端现场提供"},
			{Name: "addr", Required: true, Origin: "client_local", Note: "活动地址，客户端现场提供"},
			{Name: "signUpPlace", Required: true, Origin: "client_local", Note: "签到地点，客户端现场提供"},
			{Name: "signUpLong", Required: true, Origin: "client_local", Note: "签到点经度，必须由客户端用真实 GPS 现场生成（上游大写 U 拼写，原样保留）"},
			{Name: "signUpLat", Required: true, Origin: "client_local", Note: "签到点纬度，必须由客户端用真实 GPS 现场生成（上游大写 U 拼写，原样保留）"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	"secondclass.activity.updateNoFile": {
		ID: "secondclass.activity.updateNoFile", Target: "secondclass",
		Method: "POST", Path: "activity/v1.0.0/updateActivity", // 表单版（updateActivityNoFile）
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "actId", Required: true, Origin: "client_local", Note: "活动 id，客户端现场提供"},
			{Name: "title", Required: true, Origin: "client_local", Note: "活动标题，客户端现场提供"},
			{Name: "levelId", Required: true, Origin: "client_local", Note: "活动等级 id，客户端现场提供"},
			{Name: "actTypeId", Required: true, Origin: "client_local", Note: "活动类型 id，客户端现场提供"},
			{Name: "description", Required: true, Origin: "client_local", Note: "活动描述，客户端现场提供"},
			{Name: "allowClassIds", Required: true, Origin: "client_local", Note: "可见班级 id 集，客户端现场提供"},
			{Name: "allowCollegeIds", Required: true, Origin: "client_local", Note: "可见学院 id 集，客户端现场提供"},
			{Name: "signupStartTime", Required: true, Origin: "client_local", Note: "报名开始时间，客户端现场提供"},
			{Name: "signupEndTime", Required: true, Origin: "client_local", Note: "报名结束时间，客户端现场提供"},
			{Name: "startTime", Required: true, Origin: "client_local", Note: "活动开始时间，客户端现场提供"},
			{Name: "endTime", Required: true, Origin: "client_local", Note: "活动结束时间，客户端现场提供"},
			{Name: "allowUserCount", Required: true, Origin: "client_local", Note: "名额上限，客户端现场提供"},
			{Name: "addr", Required: true, Origin: "client_local", Note: "活动地址，客户端现场提供"},
			{Name: "gid", Required: true, Origin: "client_local", Note: "部落 id，客户端现场提供"},
			{Name: "tagIds", Required: true, Origin: "client_local", Note: "标签 id 集，客户端现场提供"},
			{Name: "firstCheckId", Required: true, Origin: "client_local", Note: "初审人 id，客户端现场提供"},
			{Name: "signUpType", Required: true, Origin: "client_local", Note: "报名方式，客户端现场提供"},
			{Name: "signWay", Required: true, Origin: "client_local", Note: "签到方式，客户端现场提供"},
			{Name: "signupLong", Required: true, Origin: "client_local", Note: "签到点经度，必须由客户端用真实 GPS 现场生成（上游小写 u 拼写，原样保留）"},
			{Name: "signupLat", Required: true, Origin: "client_local", Note: "签到点纬度，必须由客户端用真实 GPS 现场生成（上游小写 u 拼写，原样保留）"},
			{Name: "signUpPlace", Required: true, Origin: "client_local", Note: "签到地点，客户端现场提供"},
			{Name: "signUpR", Required: true, Origin: "client_local", Note: "签到半径，客户端现场提供"},
			{Name: "allowSignOut", Required: true, Origin: "client_local", Note: "是否允许签退，客户端现场提供"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	"secondclass.activity.updateWithFile": {
		ID: "secondclass.activity.updateWithFile", Target: "secondclass",
		Method: "POST", Path: "activity/v1.0.0/updateActivity", // multipart 版（updateActivity / PartMap+Part）
		ContentType: "multipart", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "actId", Required: true, Origin: "client_local", Note: "活动 id，multipart 文本 part，客户端现场提供"},
		},
		Fields: []RecipeField{
			{Name: "file", Required: false, Kind: "file", Note: "活动封面；客户端直传二课，后端不中转字节流"},
		},
		Headers: deviceHeaders,
	},
	"secondclass.activity.save": {
		ID: "secondclass.activity.save", Target: "secondclass",
		Method: "POST", Path: "actInfo/v1.0.0/saveActivity",
		ContentType: "multipart", Auth: clientAuth,
		Params: []RecipeParam{}, // PartMap 文本 part 的键由客户端现场决定，后端不编假字段名
		Fields: []RecipeField{
			{Name: "file", Required: false, Kind: "file", Note: "活动封面；客户端直传二课，后端不中转字节流"},
		},
		Headers: deviceHeaders,
	},
	"secondclass.collect.update": {
		ID: "secondclass.collect.update", Target: "secondclass",
		Method: "POST", Path: "activity/v1.0.0/updateActivityCollect",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "activityId", Required: true, Origin: "client_local", Note: "活动 id，客户端现场提供"},
			{Name: "collect", Required: true, Origin: "client_local", Note: "收藏/取消收藏标志，客户端现场提供"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	"secondclass.collect.removeAll": {
		ID: "secondclass.collect.removeAll", Target: "secondclass",
		Method: "POST", Path: "/act/actInfo/v1.0.0/removeAllMyCollectActivity",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{}, // 上游除凭据头外无额外参数
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	"secondclass.file.saveInfo": {
		ID: "secondclass.file.saveInfo", Target: "secondclass",
		Method: "POST", Path: "/act/scanCode/v1.0.0/saveActivityFileInfo",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "activityId", Required: true, Origin: "client_local", Note: "活动 id，客户端现场提供"},
			{Name: "uuid", Required: true, Origin: "client_local", Note: "上传批次 uuid，客户端现场提供"},
			{Name: "fileBelong", Required: true, Origin: "client_local", Note: "文件归属，客户端现场提供"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
	"secondclass.trivia.add": {
		ID: "secondclass.trivia.add", Target: "secondclass",
		Method: "POST", Path: "tidbit/v1.0.0/addTrivia",
		ContentType: "multipart", Auth: clientAuth,
		Params: []RecipeParam{}, // PartMap 文本 part 的键由客户端现场决定，后端不编假字段名
		Fields: []RecipeField{
			{Name: "file", Required: false, Kind: "file", Note: "花絮图片；客户端直传二课，后端不中转字节流"},
		},
		Headers: deviceHeaders,
	},
	"secondclass.trivia.delete": {
		ID: "secondclass.trivia.delete", Target: "secondclass",
		Method: "POST", Path: "tidbit/v1.0.0/deleteTidbit",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "tidbitId", Required: true, Origin: "client_local", Note: "花絮 id，客户端现场提供"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},

	// ---- A 组唯一写操作：成绩单展示设置（一手 ALL_API.md:39 的 7 个 @Field）----
	// 纯客户端配置：无现场态参数，故全为 optional（客户端可传可不传），不是 client_local。
	// 注意：scoreDisplayFlag 属于相邻接口 previewReportCard（ALL_API.md:38），不属于本配方；
	// 决策笔记 §二 曾把它误列进来（「8 个 flag」），#78 已按一手清单更正为 7 个。
	"secondclass.reportCard.saveSetting": {
		ID: "secondclass.reportCard.saveSetting", Target: "secondclass",
		Method: "POST", Path: "/act/integrate/v1.0.0/saveReportCardSetting",
		ContentType: "form", Auth: clientAuth,
		Params: []RecipeParam{
			{Name: "totalScoreFlag", Origin: "optional", Note: "是否展示总分"},
			{Name: "classTopFlag", Origin: "optional", Note: "是否展示班级排名"},
			{Name: "majorTopFlag", Origin: "optional", Note: "是否展示专业排名"},
			{Name: "gradeTopFlag", Origin: "optional", Note: "是否展示年级排名"},
			{Name: "schoolTopFlag", Origin: "optional", Note: "是否展示全校排名"},
			{Name: "firstTypeArr", Origin: "optional", Note: "分项展示开关集合"},
			{Name: "creditRecordIdArr", Origin: "optional", Note: "具体记录展示开关集合"},
		},
		Fields: []RecipeField{}, Headers: deviceHeaders,
	},
}

// RecipeForID 按稳定配方 id 取写类配方（规格 §6.1 的独立端点用）。
//
// 这是纯白名单查表：未知 id 返回 false，调用方据此回 400；绝不把用户输入的
// recipeId 拼进任何路径、键或出站 URL（后端零出站，规格 §3.3 / §6.8）。
func RecipeForID(recipeID string) (Recipe, bool) {
	r, ok := writeRecipes[recipeID]
	return r, ok
}

// parseUpstreamCode 解析二课响应信封里的 code。
//
// 规格 §7.1 / §8.2：上游 code 类型不稳定——成功是字符串 "0"，未登录是数字 6，
// 还有 "2"/"104" 等字符串。必须按「字符串或数字」双类型处理，绝不假设单一类型。
// 本仓不为非零 code 定义错误码（那是客户端本地错误态，后端看不到），这里只做解析。
//
// 定位说明：这个函数是**契约口径的参考实现 + 回归夹具**，供纯函数单测锁定
// 「双类型」这条实测结论。它**不在**后端请求路径上——规格 §3.1 明确「本仓不复制
// 二课平台的响应壳，二课响应由客户端直接消费，后端完全不接触」，POST 回传体是
// {schemaVersion, payload}（payload 即实测的 content），不带上游 code/message。
// 故改为非导出（code-review NIT-2）：零生产调用者，导出只会误导成「后端会解析响应」。
//
// 返回 (code, true) 表示成功解析出整数 code；false 表示缺失/非数字/非法 JSON。
func parseUpstreamCode(raw []byte) (int, bool) {
	var env struct {
		Code json.RawMessage `json:"code"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || len(env.Code) == 0 {
		return 0, false
	}
	// 去掉可能的空白后，分别尝试「JSON 字符串」与「JSON 数字」。
	code := bytes.TrimSpace(env.Code)
	if len(code) == 0 {
		return 0, false
	}
	if code[0] == '"' {
		var s string
		if err := json.Unmarshal(code, &s); err != nil {
			return 0, false
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	var n int
	if err := json.Unmarshal(code, &n); err != nil {
		return 0, false
	}
	return n, true
}
