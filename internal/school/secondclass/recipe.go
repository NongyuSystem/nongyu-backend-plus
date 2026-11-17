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
}

// RecipeFor 按 dataType 取配方。未知类型返回 false（调用方绝不能把 :dataType 拼进别的键）。
func RecipeFor(dt DataType) (Recipe, bool) {
	r, ok := pageRecipes[dt]
	return r, ok
}

// ParseUpstreamCode 解析二课响应信封里的 code。
//
// 规格 §7.1 / §8.2：上游 code 类型不稳定——成功是字符串 "0"，未登录是数字 6，
// 还有 "2"/"104" 等字符串。必须按「字符串或数字」双类型处理，绝不假设单一类型。
// 本仓不为非零 code 定义错误码（那是客户端本地错误态，后端看不到），这里只做解析。
//
// 定位说明：这个函数是**契约口径的参考实现 + 回归夹具**，供纯函数单测锁定
// 「双类型」这条实测结论。它**不在**后端请求路径上——规格 §3.1 明确「本仓不复制
// 二课平台的响应壳，二课响应由客户端直接消费，后端完全不接触」，POST 回传体是
// {schemaVersion, payload}（payload 即实测的 content），不带上游 code/message。
//
// 返回 (code, true) 表示成功解析出整数 code；false 表示缺失/非数字/非法 JSON。
func ParseUpstreamCode(raw []byte) (int, bool) {
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
