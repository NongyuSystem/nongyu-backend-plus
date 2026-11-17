package secondclass

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// writeRecipeIDs 是票面要求的 14 个写配方 id（独立事实源，不从实现里读出来）：
// docs/tech/决策-二课写类配方契约.md §二 的 13 个 B 组写操作 + A 组 saveReportCardSetting。
// 它充当「期望值」，用来避免「拿实现生成期望值」的同义反复。
var writeRecipeIDs = []string{
	"secondclass.activity.signInOrOutByQrCode", // activity/v2.0.0/signInOrOut（扫码签到/签退）
	"secondclass.activity.signInOrOut",         // /act/actInfo/v1.0.0/signInOrOut（签到/签退）
	"secondclass.member.updateStatus",          // actInfo/v1.0.0/updateActMemberStatus（成员审核）
	"secondclass.eval.add",                     // eval/v1.0.0/addActEval（评价）
	"secondclass.activity.edit",                // /act/activity/v1.0.0/ActivityEdit（编辑活动）
	"secondclass.activity.updateNoFile",        // activity/v1.0.0/updateActivity 表单版
	"secondclass.activity.updateWithFile",      // activity/v1.0.0/updateActivity multipart 版
	"secondclass.activity.save",                // actInfo/v1.0.0/saveActivity（发布活动）
	"secondclass.collect.update",               // activity/v1.0.0/updateActivityCollect（收藏）
	"secondclass.collect.removeAll",            // /act/actInfo/v1.0.0/removeAllMyCollectActivity（清空收藏）
	"secondclass.file.saveInfo",                // /act/scanCode/v1.0.0/saveActivityFileInfo（附件登记）
	"secondclass.trivia.add",                   // tidbit/v1.0.0/addTrivia（花絮增）
	"secondclass.trivia.delete",                // tidbit/v1.0.0/deleteTidbit（花絮删）
	"secondclass.reportCard.saveSetting",       // /act/integrate/v1.0.0/saveReportCardSetting（成绩单设置）
}

// writeRecipePaths 是 14 条上游路径的逐字期望值（来自 ALL_API.md A#13 / B 组）。
// 相对路径不带前导 / 的，一律原样保留，绝不补前缀、绝不规整斜杠。
var writeRecipePaths = map[string]string{
	"secondclass.activity.signInOrOutByQrCode": "activity/v2.0.0/signInOrOut",
	"secondclass.activity.signInOrOut":         "/act/actInfo/v1.0.0/signInOrOut",
	"secondclass.member.updateStatus":          "actInfo/v1.0.0/updateActMemberStatus",
	"secondclass.eval.add":                     "eval/v1.0.0/addActEval",
	"secondclass.activity.edit":                "/act/activity/v1.0.0/ActivityEdit",
	"secondclass.activity.updateNoFile":        "activity/v1.0.0/updateActivity",
	"secondclass.activity.updateWithFile":      "activity/v1.0.0/updateActivity",
	"secondclass.activity.save":                "actInfo/v1.0.0/saveActivity",
	"secondclass.collect.update":               "activity/v1.0.0/updateActivityCollect",
	"secondclass.collect.removeAll":            "/act/actInfo/v1.0.0/removeAllMyCollectActivity",
	"secondclass.file.saveInfo":                "/act/scanCode/v1.0.0/saveActivityFileInfo",
	"secondclass.trivia.add":                   "tidbit/v1.0.0/addTrivia",
	"secondclass.trivia.delete":                "tidbit/v1.0.0/deleteTidbit",
	"secondclass.reportCard.saveSetting":       "/act/integrate/v1.0.0/saveReportCardSetting",
}

// clientLocalNames 是规格 §6.4 点名「必须客户端现场产生」的槽位名。
// signUpLong/signUpLat（ActivityEdit 大写 U）与 signupLong/signupLat
// （updateActivityNoFile 小写 u）是上游真实存在的两种拼写，两者都必须 client_local，
// 都不得被「修正」成同一种。
var clientLocalNames = []string{
	"qrCodeTimestamp", "codeTime", "gpsCode",
	"signUpLong", "signUpLat", "signupLong", "signupLat",
	"actId", "activityId",
}

// 规格 §6.3/§6.4/§6.6：恰好 14 条写配方、id 全局唯一、map key 与 Recipe.ID 一致。
// 这是对静态表的完整性不变量（同包白盒），不影响 A 组 pageRecipes / recipecode_test.go。
func TestWriteRecipes_ExactlyFourteenUniqueIDs(t *testing.T) {
	want := map[string]bool{}
	for _, id := range writeRecipeIDs {
		if want[id] {
			t.Fatalf("期望清单里有重复 id %q", id)
		}
		want[id] = true
	}
	got := map[string]bool{}
	for id, r := range writeRecipes {
		if got[id] {
			t.Errorf("写配方表出现重复 key %q", id)
		}
		got[id] = true
		if r.ID != id {
			t.Errorf("map key %q 与 Recipe.ID %q 不一致（RecipeForID 会取错）", id, r.ID)
		}
	}
	if len(got) != 14 {
		t.Errorf("应有 14 条写配方，实际 %d", len(got))
	}
	for id := range want {
		if !got[id] {
			t.Errorf("缺少写配方 %q", id)
		}
	}
	for id := range got {
		if !want[id] {
			t.Errorf("多出未登记的写配方 %q", id)
		}
	}
}

// 票面验收：GET 端点用的 RecipeForID 能取到全部 14 条；未知 id 返回 false。
func TestRecipeForID_KnownAndUnknown(t *testing.T) {
	for _, id := range writeRecipeIDs {
		r, ok := RecipeForID(id)
		if !ok {
			t.Errorf("RecipeForID(%q) 未命中", id)
			continue
		}
		if r.ID != id {
			t.Errorf("RecipeForID(%q).ID = %q", id, r.ID)
		}
	}
	for _, bogus := range []string{"", "bogus", "secondclass.activity.nope", "../../etc/passwd", "secondclass.credit.getSecondActivityCredit"} {
		if r, ok := RecipeForID(bogus); ok {
			t.Errorf("RecipeForID(%q) 不应命中，却返回 %q", bogus, r.ID)
		}
	}
}

// 票面验收：七/八字段 + headers 齐全；target 固定 secondclass；auth 是客户端自带的
// header 凭据且不含任何 token 值；method 全 POST；成功响应形状合法。
func TestWriteRecipes_ContractFieldsAndAuth(t *testing.T) {
	for _, id := range writeRecipeIDs {
		r, ok := RecipeForID(id)
		if !ok {
			continue
		}
		if r.Target != "secondclass" {
			t.Errorf("%q target 必须固定 secondclass，实际 %q", id, r.Target)
		}
		if r.Method != "POST" {
			t.Errorf("%q 写操作必须是 POST，实际 %q", id, r.Method)
		}
		switch r.ContentType {
		case "form", "multipart", "none":
		default:
			t.Errorf("%q contentType 非法: %q", id, r.ContentType)
		}
		if r.Auth.In != "header" || r.Auth.Name != "x-access-token" || r.Auth.From != "client" {
			t.Errorf("%q auth 必须是客户端自带的 header 凭据: %+v", id, r.Auth)
		}
		if r.Path == "" || r.ID == "" {
			t.Errorf("%q 缺 id/path: %+v", id, r)
		}
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("序列化 %q 失败: %v", id, err)
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("%q 不是 JSON 对象: %v", id, err)
		}
		for _, k := range []string{"id", "target", "method", "path", "contentType", "auth", "params", "fields", "headers"} {
			if _, ok := obj[k]; !ok {
				t.Errorf("%q 配方缺字段 %q", id, k)
			}
		}
		// 空数组必须是 [] 而不是 null（客户端会直接遍历）。
		for _, k := range []string{"params", "fields", "headers"} {
			if _, ok := obj[k].([]any); !ok {
				t.Errorf("%q 的 %q 必须是数组，实际 %T", id, k, obj[k])
			}
		}
	}
}

// 票面验收（第一性理由）：client_local 槽位「只给名字不给值」。
// 结构上 RecipeParam 就没有存值的字段；这里再锁死序列化结果里没有任何具体值。
func TestWriteRecipes_ClientLocalSlotsHaveNoValues(t *testing.T) {
	allowedParamKeys := map[string]bool{"name": true, "required": true, "origin": true, "note": true}
	// 某个 client_local 名字后面直接跟数字，说明把值写进了响应（如 "qrCodeTimestamp":1234567890123）。
	valueAfterName := map[string]*regexp.Regexp{}
	for _, n := range clientLocalNames {
		valueAfterName[n] = regexp.MustCompile(`"` + regexp.QuoteMeta(n) + `"\s*:\s*[-0-9]`)
	}

	seen := map[string]bool{}
	for _, id := range writeRecipeIDs {
		r, ok := RecipeForID(id)
		if !ok {
			continue
		}
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("序列化 %q 失败: %v", id, err)
		}
		var obj map[string]any
		_ = json.Unmarshal(raw, &obj)
		params, _ := obj["params"].([]any)
		for _, p := range params {
			pm, ok := p.(map[string]any)
			if !ok {
				t.Errorf("%q 参数不是对象: %v", id, p)
				continue
			}
			for k := range pm {
				if !allowedParamKeys[k] {
					t.Errorf("%q 参数槽位出现非契约字段 %q（可能混入了具体值）: %v", id, k, pm)
				}
			}
			name, _ := pm["name"].(string)
			if name == "" {
				t.Errorf("%q 有空参数名", id)
			}
			switch pm["origin"] {
			case "client_local", "fixed", "optional":
			default:
				t.Errorf("%q 参数 %q 的 origin 非法: %v", id, name, pm["origin"])
			}
			for _, cl := range clientLocalNames {
				if name == cl {
					if pm["origin"] != "client_local" {
						t.Errorf("%q 的 %q 必须 origin=client_local，实际 %v", id, name, pm["origin"])
					}
					seen[cl] = true
				}
			}
		}
		// 脱敏：参数名后面不得出现任何数值；响应体不得出现 Bearer 之类凭据形状。
		for _, cl := range clientLocalNames {
			if valueAfterName[cl].Match(raw) {
				t.Errorf("%q 响应体里 %q 后面跟了具体值（伪造现场值）: %s", id, cl, raw)
			}
		}
		if strings.Contains(string(raw), "Bearer") {
			t.Errorf("%q 响应体出现 Bearer 凭据", id)
		}
		// auth 只有三个键，且没有地方能放 token 值。
		auth, _ := obj["auth"].(map[string]any)
		if len(auth) != 3 || auth["name"] != "x-access-token" || auth["from"] != "client" {
			t.Errorf("%q auth 形状必须恰好 {in,name,from} 且 name/from 正确: %v", id, auth)
		}
		for k := range auth {
			if k != "in" && k != "name" && k != "from" {
				t.Errorf("%q auth 出现多余字段 %q（可能是 token 值）", id, k)
			}
		}
	}
	// 5 个票面点名的 client_local 参数（含两种 GPS 拼写）都必须真的出现在配方里。
	for _, must := range []string{"qrCodeTimestamp", "codeTime", "gpsCode", "signUpLong", "signUpLat"} {
		if !seen[must] {
			t.Errorf("配方表里没有票面点名的 client_local 参数 %q", must)
		}
	}
}

// 票面验收：7 个必需设备头由配方 headers 给出；pu-version 等 fixed 有 Value，
// client-long / client-lat 为 client_local（只给名字不给值）。x-access-token 不在 headers。
func TestWriteRecipes_DeviceHeaders(t *testing.T) {
	wantNames := []string{
		"pu-version", "client-dev-brand", "client-dev-type", "client-os-type",
		"client-os-version", "client-long", "client-lat",
	}
	for _, id := range writeRecipeIDs {
		r, ok := RecipeForID(id)
		if !ok {
			continue
		}
		if len(r.Headers) != 7 {
			t.Errorf("%q 应有 7 个设备头，实际 %d", id, len(r.Headers))
		}
		byName := map[string]RecipeHeader{}
		for _, h := range r.Headers {
			byName[h.Name] = h
			if h.Name == "x-access-token" {
				t.Errorf("%q 的 headers 不应含凭据头 x-access-token（它属于 auth）", id)
			}
		}
		for i, n := range wantNames {
			if _, ok := byName[n]; !ok {
				t.Errorf("%q 缺设备头 %q", id, n)
				continue
			}
			if r.Headers[i].Name != n {
				t.Errorf("%q 设备头顺序变化：第 %d 个是 %q，应为 %q", id, i, r.Headers[i].Name, n)
			}
		}
		if h := byName["pu-version"]; h.Value == "" {
			t.Errorf("%q 的 pu-version 是 fixed，应有 Value", id)
		} else if h.Value != "12.10.0.10010599" {
			t.Errorf("%q 的 pu-version 值应为固定常量，实际 %q", id, h.Value)
		}
		if h := byName["client-os-type"]; h.Value != "android" {
			t.Errorf("%q 的 client-os-type 应为固定 android，实际 %q", id, h.Value)
		}
		for _, n := range []string{"client-long", "client-lat"} {
			if h := byName[n]; h.Value != "" {
				t.Errorf("%q 的 %q 是 client_local，不应带 Value，实际 %q", id, n, h.Value)
			}
		}
		// 序列化后 client-long/lat 必须不含 "value" 键（omitempty 生效）。
		raw, _ := json.Marshal(r.Headers)
		var hs []map[string]any
		_ = json.Unmarshal(raw, &hs)
		for _, h := range hs {
			if h["name"] == "client-long" || h["name"] == "client-lat" {
				if _, ok := h["value"]; ok {
					t.Errorf("%q 的 %v 序列化后带上了 value（GPS 值泄漏）", id, h)
				}
			}
		}
	}
}

// 票面验收：上游路径原样保留；含双斜杠的 /act//credit/… 读配方不得被「修正」。
func TestWriteRecipes_PathsPreservedVerbatim(t *testing.T) {
	for id, want := range writeRecipePaths {
		r, ok := RecipeForID(id)
		if !ok {
			t.Errorf("缺写配方 %q", id)
			continue
		}
		if r.Path != want {
			t.Errorf("%q 路径被改写：应为 %q，实际 %q", id, want, r.Path)
		}
		if strings.Contains(r.Path, "//") {
			t.Errorf("%q 路径 %q 意外出现双斜杠（请核对上游）", id, r.Path)
		}
	}
	// A 组读配方 importCreditDetail 的双斜杠是本仓已知的上游原样，必须原样存活。
	r, ok := RecipeFor(DataTypeImportCreditDetail)
	if !ok {
		t.Fatal("A 组 importCreditDetail 读配方缺失")
	}
	if r.Path != "/act//credit/v1.0.0/getImportCreditDetail" {
		t.Errorf("读配方双斜杠被「修正」了: %q", r.Path)
	}
}

// 票面验收：配方不含 parser/session/cookies[]（三项教务专有）；默认单跳，steps 缺省。
func TestWriteRecipes_NoJiaowuFieldsAndSingleHop(t *testing.T) {
	for _, id := range writeRecipeIDs {
		r, ok := RecipeForID(id)
		if !ok {
			continue
		}
		if len(r.Steps) != 0 {
			t.Errorf("%q 默认单跳，steps 必须为空，实际 %+v", id, r.Steps)
		}
		raw, _ := json.Marshal(r)
		var obj map[string]any
		_ = json.Unmarshal(raw, &obj)
		for _, k := range []string{"parser", "session", "cookies", "address", "mode", "needs", "basePath"} {
			if _, ok := obj[k]; ok {
				t.Errorf("%q 配方不应含教务侧字段 %q", id, k)
			}
		}
		if _, ok := obj["steps"]; ok {
			t.Errorf("%q 默认单跳，序列化后不应出现 steps 字段", id)
		}
	}
}

// #78 更正回归：saveReportCardSetting 的一手契约（ALL_API.md:39）只有 7 个 @Field，
// scoreDisplayFlag 属于相邻接口 previewReportCard（ALL_API.md:38），不得列入本配方。
// 决策笔记 §二 曾误写「8 个 flag」，这里锁死修正后的 7 槽位与「无 scoreDisplayFlag」。
func TestSaveSettingRecipe_ExactlySevenFlags(t *testing.T) {
	r, ok := RecipeForID("secondclass.reportCard.saveSetting")
	if !ok {
		t.Fatal("缺少 secondclass.reportCard.saveSetting 写配方")
	}
	want := []string{
		"totalScoreFlag", "classTopFlag", "majorTopFlag", "gradeTopFlag",
		"schoolTopFlag", "firstTypeArr", "creditRecordIdArr",
	}
	if len(r.Params) != len(want) {
		t.Errorf("saveReportCardSetting 应有 7 个参数槽位，实际 %d", len(r.Params))
	}
	byName := map[string]bool{}
	for _, p := range r.Params {
		byName[p.Name] = true
	}
	for _, n := range want {
		if !byName[n] {
			t.Errorf("saveReportCardSetting 缺少一手 @Field %q", n)
		}
	}
	if byName["scoreDisplayFlag"] {
		t.Errorf("saveReportCardSetting 不应含 scoreDisplayFlag（它属于 previewReportCard，一手 ALL_API.md:38）")
	}
}
