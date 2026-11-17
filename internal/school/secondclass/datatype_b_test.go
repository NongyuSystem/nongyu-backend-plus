package secondclass

import (
	"testing"
	"time"
)

// bGroupReadEndpoints 是一手契约 ALL_API.md「B. 活动（48）」里的 35 个读接口。
// 其余 13 个是写类（signInOrOut / addActEval / saveActivity / updateActivity ...），
// 按规格 §6.8「写类不建 dataType、不落缓存」不进本表。
//
// 每行 dt 是该读接口落表后的 dataType。这里刻意用字符串字面量作**独立事实源**，
// 不引用 datatype.go 里将要新增的常量：TDD 红阶段这些名字还没注册，
// ParseDataType 应全部返回 false，测试随之失败。
//
// 票面「47 个读接口」是规格 §2 的全局口径：A 组 12 读（#73 已落表）+ 本表 35 读 = 47。
// 本票只负责 B 组这 35 个；A 组 12 个保持原样，不重复落表。
//
// 去重三处（票面点名，按「同一 dataType 可服务多个接口」归并，不造重复 dataType）：
//   - #38/#39 getActEvaluateTagList 出现两次 → 同一 actEvaluateTagDict
//   - #27/#28 getActMemberList 有 actId / activityId 两版 → 同一 actMemberList
//   - #31/#32 getSchoolGroup 参数不同两版 → 同一 schoolGroupDict
var bGroupReadEndpoints = []struct {
	path   string
	method string
	dt     string
}{
	{"/act/actInfo/v1.0.0/getActDetail", "getActDetail", "actDetail"},                                     // #6
	{"eval/v1.0.0/getActEvalList", "getActEvalList", "actEvalList"},                                       // #7
	{"/act/actGroup/v1.0.0/getActGroupList", "getActGroup", "actGroupDict"},                               // #8
	{"/act/actLevel/v1.0.0/getActLevelList", "getActLevelList", "actLevelDict"},                           // #9
	{"actInfo/v1.0.0/getActStatusById", "getActStatusById", "actStatus"},                                  // #10
	{"/act/actTag/v1.0.0/getActTagList", "getActTag", "actTagDict"},                                       // #11
	{"/act/tidbit/v1.0.0/getActTriviaList", "getActTrivia", "actTriviaList"},                              // #12
	{"/act/actType/v1.0.0/getSchoolActType", "getActTypeList", "actTypeDict"},                             // #13
	{"/query/solrAct/v1.0.0/getActivityByName", "getActivityByName", "actList"},                           // #14
	{"activity/v1.0.0/getActivityEditDetail", "getActivityEditDetail", "actEditDetail"},                   // #15
	{"activity/v1.0.0/getActivityModifyDetail", "getActivityModifyDetail", "actEditDetail"},               // #16
	{"/act/activity/v1.0.0/getMyActivitySearchList", "getActivitySearch", "actList"},                      // #17
	{"activity/v1.0.0/getActivityVerifierList", "getActivityVerifierList", "actStatus"},                   // #18
	{"/act/sysOrg/v1.0.0/getCollegeAndClassInfoByUser", "getClassInfoList", "collegeClassInfo"},           // #19
	{"/act/eval/v1.0.0/getActEvalList", "getEvalua", "actEvalList"},                                       // #20
	{"/act/actInfo/v1.0.0/getActivityForMyCollect", "getEventCollection", "myCollectList"},                // #21
	{"/act/actconfig/v1.0.0/getFirstReviewPerson", "getFirstReviewPerson", "firstReviewPerson"},           // #22
	{"actInfo/v1.0.0/getGpsR", "getGpsR", "gpsRange"},                                                     // #23
	{"/query/solrGroup/v1.0.0/getGroupByTitle", "getGroupByName", "groupSearchList"},                      // #24
	{"group/v1.0.0/getGroupActivityList", "getGroupDetailActList", "actList"},                             // #25
	{"scanCode/v1.0.0/getImportMemberResult", "getImportMemberResult", "importMemberResult"},              // #26
	{"/act/actInfo/v1.0.0/getActMemberList", "getMember", "actMemberList"},                                // #27 actId 版
	{"actInfo/v1.0.0/getActMemberList", "getMemberList", "actMemberList"},                                 // #28 activityId 版
	{"/act/actInfo/v1.0.0/getActForUserMyList", "getMineActivityList", "myActList"},                       // #29
	{"/act/actType/v1.0.0/getSchoolActType", "getSchoolActType", "actTypeDict"},                           // #30
	{"/act/actconfig/v1.0.0/getSchoolGroup", "getSchoolGroup", "schoolGroupDict"},                         // #31 无参版
	{"group/v1.0.0/getSchoolGroup", "getSchoolGroup", "schoolGroupDict"},                                  // #32 多参版
	{"/act/actInfo/v1.0.0/getSchoolLogo", "getSchoolLogo", "schoolLogo"},                                  // #33
	{"scanCode/v1.0.0/getUploadFileDetail", "getUploadFileDetail", "uploadFileDetail"},                    // #34
	{"scanCode/v1.0.0/getUploadResult", "getUploadResult", "uploadResult"},                                // #35
	{"/query/solrUser/v1.0.0/getUserByName", "getUserByName", "userSearchList"},                           // #36
	{"/act/actInfo/v1.0.0/getUserSchoolActList", "getUsersSchoolList", "actList"},                         // #37
	{"/act/actTag/v1.0.0/getActEvaluateTagList", "getevaluationlList", "actEvaluateTagDict"},              // #38 去重一
	{"/act/actTag/v1.0.0/getActEvaluateTagList", "getevaluationlList_TagNamesBean", "actEvaluateTagDict"}, // #39 去重一
	{"/act/actInfo/v1.0.0/selectBeSignAdminActList", "selectBeSignAdminActList", "actList"},               // #43
}

// bGroupDataTypes 是 35 个读接口归并后的 24 个 B 组 dataType（规格 §6.8 分组名 + 「等」扩展）。
// 与 endpoints 里的 dt 集合交叉核对，防止两处清单漂移。
var bGroupDataTypes = []string{
	"actDetail", "actEvalList", "actGroupDict", "actLevelDict", "actStatus",
	"actTagDict", "actTriviaList", "actTypeDict", "actList", "actEditDetail",
	"collegeClassInfo", "myCollectList", "firstReviewPerson", "gpsRange",
	"groupSearchList", "importMemberResult", "actMemberList", "myActList",
	"schoolGroupDict", "schoolLogo", "uploadFileDetail", "uploadResult",
	"userSearchList", "actEvaluateTagDict",
}

// bGroupActivityTypes：活动类（名额/状态变化快）用短组。
var bGroupActivityTypes = []string{
	"actDetail", "actList", "actMemberList", "actTriviaList", "actStatus",
	"actEditDetail", "myCollectList", "myActList", "groupSearchList", "userSearchList",
}

// bGroupDictTypes：字典/规则/基础数据类用长组。
var bGroupDictTypes = []string{
	"actTypeDict", "actLevelDict", "actTagDict", "actEvaluateTagDict",
	"actGroupDict", "schoolGroupDict", "collegeClassInfo", "firstReviewPerson",
	"gpsRange", "importMemberResult", "schoolLogo", "uploadFileDetail", "uploadResult",
}

func TestBGroup_AllDataTypesInControlledVocabulary(t *testing.T) {
	known := map[DataType]bool{}
	for _, d := range KnownDataTypes() {
		known[d] = true
	}
	seen := map[string]bool{}
	for _, name := range bGroupDataTypes {
		if seen[name] {
			t.Errorf("B 组 dataType 清单有重复项 %q", name)
		}
		seen[name] = true
		if _, ok := ParseDataType(name); !ok {
			t.Errorf("B 组 dataType %q 未进受控词表（ParseDataType 拒绝）", name)
		}
		if !known[DataType(name)] {
			t.Errorf("B 组 dataType %q 不在 KnownDataTypes() 里", name)
		}
	}
	if len(bGroupDataTypes) != 24 {
		t.Errorf("B 组归并后应有 24 个 dataType，实际 %d", len(bGroupDataTypes))
	}
}

func TestBGroup_EveryReadEndpointHasRegisteredDataType(t *testing.T) {
	// 35 个读接口逐一落表：每个接口映射的 dataType 都必须在受控词表里。
	if len(bGroupReadEndpoints) != 35 {
		t.Fatalf("B 组读接口应有 35 个，实际 %d", len(bGroupReadEndpoints))
	}
	declared := map[string]bool{}
	for _, name := range bGroupDataTypes {
		declared[name] = true
	}
	used := map[string]bool{}
	for _, e := range bGroupReadEndpoints {
		if e.path == "" || e.method == "" || e.dt == "" {
			t.Errorf("接口表有空字段: %+v", e)
		}
		if _, ok := ParseDataType(e.dt); !ok {
			t.Errorf("接口 %s (%s) 映射的 %q 未注册", e.method, e.path, e.dt)
		}
		if !declared[e.dt] {
			t.Errorf("接口 %s 用了清单外的 dataType %q", e.method, e.dt)
		}
		used[e.dt] = true
	}
	for _, name := range bGroupDataTypes {
		if !used[name] {
			t.Errorf("dataType %q 没有任何读接口映射（落表却空置）", name)
		}
	}
}

func TestBGroup_ReadEndpointCountMatchesSpec(t *testing.T) {
	// 规格 §2：全局读 47 = A 组 12（#73 已落表）+ B 组 35（本票）。
	if got := len(allTwelve) + len(bGroupReadEndpoints); got != 47 {
		t.Errorf("全局读接口应为 47（A 组 12 + B 组 35），实际 %d", got)
	}
}

func TestBGroup_DeduplicatedEndpointsShareOneDataType(t *testing.T) {
	// 票面点名的三处去重：同一接口名/路径的多版必须落同一个 dataType，不造重复 dataType。
	// 每个 case 列出「同一逻辑接口」的所有 (path, method) 版本与期望的唯一 dataType。
	cases := []struct {
		name     string
		variants []struct{ path, method string }
		want     string
	}{
		{"getActEvaluateTagList 两次", []struct{ path, method string }{
			{"/act/actTag/v1.0.0/getActEvaluateTagList", "getevaluationlList"},
			{"/act/actTag/v1.0.0/getActEvaluateTagList", "getevaluationlList_TagNamesBean"},
		}, "actEvaluateTagDict"},
		{"getActMemberList actId/activityId 两版", []struct{ path, method string }{
			{"/act/actInfo/v1.0.0/getActMemberList", "getMember"},
			{"actInfo/v1.0.0/getActMemberList", "getMemberList"},
		}, "actMemberList"},
		{"getSchoolGroup 无参/多参两版", []struct{ path, method string }{
			{"/act/actconfig/v1.0.0/getSchoolGroup", "getSchoolGroup"},
			{"group/v1.0.0/getSchoolGroup", "getSchoolGroup"},
		}, "schoolGroupDict"},
	}
	for _, c := range cases {
		dts := map[string]bool{}
		for _, v := range c.variants {
			found := false
			for _, e := range bGroupReadEndpoints {
				if e.path == v.path && e.method == v.method {
					found = true
					dts[e.dt] = true
					if e.dt != c.want {
						t.Errorf("%s: %s (%s) 的 dataType = %q，应为 %q", c.name, v.method, v.path, e.dt, c.want)
					}
				}
			}
			if !found {
				t.Errorf("%s: 接口表里找不到 %s (%s)", c.name, v.method, v.path)
			}
		}
		if len(dts) != 1 {
			t.Errorf("%s: 多版应归并到唯一 dataType，实际出现 %d 个: %v", c.name, len(dts), dts)
		}
	}
}

func TestBGroup_TTLGroupAssignmentFollowsChangeFrequency(t *testing.T) {
	// 票面：活动类多用短组，字典/规则类用长组；活动评价用中组（规格 §6.10）。
	for _, name := range bGroupActivityTypes {
		if got := DataType(name).TTL(); got != ttlShort {
			t.Errorf("活动类 %q 应为短组 %v，实际 %v", name, ttlShort, got)
		}
	}
	for _, name := range bGroupDictTypes {
		if got := DataType(name).TTL(); got != ttlLong {
			t.Errorf("字典类 %q 应为长组 %v，实际 %v", name, ttlLong, got)
		}
	}
	if got := DataType("actEvalList").TTL(); got != ttlMedium {
		t.Errorf("活动评价 actEvalList 应为中组 %v，实际 %v", ttlMedium, got)
	}
	// B 组没有 24h 规则说明类；最长组仍由 A 组 creditRule 独占。
	for _, name := range bGroupDataTypes {
		if DataType(name).TTL() == ttlLongest {
			t.Errorf("B 组 %q 不该落到最长组（那是规则说明专用）", name)
		}
	}
	// 四组常量仍严格递增（不因 B 组引入而退化）。
	if !(ttlShort < ttlMedium && ttlMedium < ttlLong && ttlLong < ttlLongest) {
		t.Errorf("四组 TTL 未严格递增: %v %v %v %v", ttlShort, ttlMedium, ttlLong, ttlLongest)
	}
	// 每组都落在规格 §6.10 建议区间。
	if ttlShort < 10*time.Minute || ttlShort > 15*time.Minute {
		t.Errorf("短组越界: %v", ttlShort)
	}
	if ttlMedium < 30*time.Minute || ttlMedium > 60*time.Minute {
		t.Errorf("中组越界: %v", ttlMedium)
	}
	if ttlLong < 6*time.Hour || ttlLong > 12*time.Hour {
		t.Errorf("长组越界: %v", ttlLong)
	}
}

func TestBGroup_EveryTypeHasDeclaredShape(t *testing.T) {
	valid := map[dataShape]bool{shapeArray: true, shapeObject: true, shapeWrapper: true}
	counts := map[dataShape]int{}
	for _, name := range bGroupDataTypes {
		s := DataType(name).Shape()
		if !valid[s] {
			t.Errorf("%q 的 shape 非法: %v", name, s)
		}
		counts[s]++
	}
	if counts[shapeArray] == 0 {
		t.Errorf("B 组应至少有一个数组形态，实际: %v", counts)
	}
}
