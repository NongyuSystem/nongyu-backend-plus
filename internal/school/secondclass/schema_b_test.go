package secondclass

import (
	"encoding/json"
	"testing"
)

// B 组 schema 的验收测试：照 #73 的 //go:embed schema/*.json 机制，新增的 B 组
// schema 文件会被自动纳入；KnownDataTypes() 必须能反查到全部 B 组 dataType。
// 不访问真实二课平台，全部用构造样例（规格 §8.3 / §8.4）。

func TestBSchema_CompilesAllBGroupAtStartup(t *testing.T) {
	// 启动期编译全部 schema（A 组 12 + B 组 24 = 36 份），fail-fast。
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("启动期 schema 编译失败（B 组新增文件应被 go:embed 自动纳入）: %v", err)
	}
	have := map[DataType]bool{}
	for _, d := range v.DataTypes() {
		have[d] = true
	}
	// 全部 B 组 dataType 都必须有已编译 schema。
	for _, name := range bGroupDataTypes {
		if !have[DataType(name)] {
			t.Errorf("B 组 %q 缺已编译 schema", name)
		}
	}
	// 全部 A 组 dataType 也仍在（不能因 B 组扩展而丢）。
	for _, d := range allTwelve {
		if !have[d] {
			t.Errorf("A 组 %q 的 schema 因 B 组扩展而丢失", d)
		}
	}
	// 总数 = A 组 12 + B 组 24 = 36，与 KnownDataTypes 长度一致（强闭环）。
	if len(v.DataTypes()) != len(KnownDataTypes()) {
		t.Errorf("已编译 schema 数 %d 与受控词表数 %d 不一致", len(v.DataTypes()), len(KnownDataTypes()))
	}
	if want := len(allTwelve) + len(bGroupDataTypes); len(v.DataTypes()) != want {
		t.Errorf("应编译 %d 份 schema（A 12 + B 24），实际 %d", want, len(v.DataTypes()))
	}
}

func TestBSchema_AllBGroupValidateConstructedSamples(t *testing.T) {
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	// 每个 B 组 dataType 用一份最小合法构造样例过校验（{} 是宽松 schema 都能接受的）。
	for _, name := range bGroupDataTypes {
		dt := DataType(name)
		body := []byte(`{"schemaVersion":1,"payload":{}}`)
		if err := v.Validate(dt, body); err != nil {
			// 数组形态的类型给 {} 会被拒，属预期；这里改用 [] 适配。
			if dt.Shape() == shapeArray {
				body = []byte(`{"schemaVersion":1,"payload":[]}`)
				if err2 := v.Validate(dt, body); err2 == nil {
					continue
				}
			}
			t.Errorf("B 组 %q 连最小构造样例都过不了: %v", name, err)
		}
	}
}

func TestBSchema_ActDetailHasNullableGroupFields(t *testing.T) {
	// 票面：活动详情 schema 含 gid/groupName/schoolName/allowCollegeNames，且按可空字段处理。
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	dt := DataType("actDetail")
	// 四个字段存在且为 null 时也要通过（运行时是否真有值属未验证项 §10.1④）。
	body := []byte(`{"schemaVersion":1,"payload":{"gid":null,"groupName":null,"schoolName":null,"allowCollegeNames":null,"title":"示例活动"}}`)
	if err := v.Validate(dt, body); err != nil {
		t.Errorf("活动详情四个可空字段为 null 时应通过: %v", err)
	}
	// 字段给真实类型也要通过：gid 字符串、allowCollegeNames 数组。
	body2 := []byte(`{"schemaVersion":1,"payload":{"gid":"g1","groupName":"部落A","schoolName":"川农","allowCollegeNames":["信息工程学院"]}}`)
	if err := v.Validate(dt, body2); err != nil {
		t.Errorf("活动详情四个字段给真实类型时应通过: %v", err)
	}
	// 缺这四个字段也必须通过（可空 = 不要求必填）。
	body3 := []byte(`{"schemaVersion":1,"payload":{"title":"示例活动"}}`)
	if err := v.Validate(dt, body3); err != nil {
		t.Errorf("活动详情缺四个可空字段时应通过: %v", err)
	}
}

func TestBSchema_ActDetailReflectsUserStateFieldsAsNullable(t *testing.T) {
	// 票面：用户态字段 collectFlag/signInTime/currentUserButton 等如实体现（可空）。
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	dt := DataType("actDetail")
	body := []byte(`{"schemaVersion":1,"payload":{"collectFlag":null,"signInTime":null,"signBackTime":null,"currentUserButton":null,"currentUserIdentity":null,"evaluationScore":null,"triviaStatus":null,"title":"示例"}}`)
	if err := v.Validate(dt, body); err != nil {
		t.Errorf("用户态字段为 null 应通过（可空如实体现）: %v", err)
	}
	// 给字符串值也通过。
	body2 := []byte(`{"schemaVersion":1,"payload":{"collectFlag":"1","signInTime":"2026-11-16 10:00","currentUserButton":"signUp"}}`)
	if err := v.Validate(dt, body2); err != nil {
		t.Errorf("用户态字段给字符串值应通过: %v", err)
	}
}

func TestBSchema_ListPayloadRequiresArray(t *testing.T) {
	// 票面：列表类 schema 校验 content 是数组。payload 是列表顶层，须为数组（或 null 空态）。
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	// 这些 dataType 的 payload 顶层是数组（列表类）。
	listTypes := []string{
		"actList", "actMemberList", "actEvalList", "actTriviaList", "myCollectList",
		"myActList", "groupSearchList", "userSearchList", "actGroupDict", "actLevelDict",
		"actTagDict", "actTypeDict", "schoolGroupDict", "actEvaluateTagDict",
	}
	for _, name := range listTypes {
		dt := DataType(name)
		// 数组应通过。
		if err := v.Validate(dt, []byte(`{"schemaVersion":1,"payload":[]}`)); err != nil {
			t.Errorf("列表类 %q 的 payload=[] 应通过: %v", name, err)
		}
		// 给对象应被拒（校验 content 是数组，不假设一定非空但也不接受对象）。
		if err := v.Validate(dt, []byte(`{"schemaVersion":1,"payload":{"x":1}}`)); err == nil {
			t.Errorf("列表类 %q 的 payload 为对象时应被拒", name)
		}
	}
}

func TestBSchema_EmptyContentAcceptsArrayOrNull(t *testing.T) {
	// 票面：空态 content 用 oneOf:[array,null] 兼容，不假设一定非空（§10.1④）。
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	// 典型列表类：payload 为 null（空态）与 [] 都应通过。
	for _, name := range []string{"actList", "actMemberList", "myCollectList", "actEvalList", "actTriviaList"} {
		dt := DataType(name)
		if err := v.Validate(dt, []byte(`{"schemaVersion":1,"payload":null}`)); err != nil {
			t.Errorf("%q 空态 payload=null 应通过（oneOf array/null）: %v", name, err)
		}
		if err := v.Validate(dt, []byte(`{"schemaVersion":1,"payload":[]}`)); err != nil {
			t.Errorf("%q 空态 payload=[] 应通过: %v", name, err)
		}
	}
}

func TestBSchema_PaginationTotalFieldNotHardcoded(t *testing.T) {
	// 票面：分页响应里的总数字段不写死（§10.1④ 未验证项）。
	// 构造一份带任意总数字段名的分页响应，schema 不得因「不认识该字段」而拒，
	// 也不得把它 required 化。这里用宽松 additionalProperties 的列表 schema 验证：
	// 顶层任意 total 字段都不影响 content 数组校验。
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	dt := DataType("actList")
	// content 数组 + 任意命名的总数字段（total / totalElements / totalCount / 自定义X）。
	for _, body := range []string{
		`{"schemaVersion":1,"payload":[{"id":1,"title":"活动"}],"total":99}`,
		`{"schemaVersion":1,"payload":[{"id":1}],"totalElements":88}`,
		`{"schemaVersion":1,"payload":[{"id":1}],"totalCount":77,"weirdTotalName":66}`,
		`{"schemaVersion":1,"payload":[{"id":1}]}`, // 完全没有总数字段也通过
	} {
		if err := v.Validate(dt, []byte(body)); err != nil {
			t.Errorf("分页总数字段不应被写死/必填，样例 %s 被拒: %v", body, err)
		}
	}
}

func TestBSchema_PreservesUpstreamTypoFistActTypeId(t *testing.T) {
	// 票面：上游拼写错误 fistActTypeId（缺 r）原样保留，不得「修正」为 firstActTypeId。
	// 学分明细的真实夹具已在 recipecode_test.go 覆盖 A 组；这里额外验证 B 组
	// actList 的 items 若带 fistActTypeId 能通过，而 firstActTypeId 不作为要求。
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	dt := DataType("actList")
	// 带上游错拼字段的活动项应通过（不因未知字段被拒，也不要求它必填）。
	body := []byte(`{"schemaVersion":1,"payload":[{"id":1,"fistActTypeId":"x","totalScore":11.0}]}`)
	if err := v.Validate(dt, body); err != nil {
		t.Errorf("含 fistActTypeId 与数字 totalScore 的活动项应通过: %v", err)
	}
	// schema 文本里绝不能出现「修正后的」firstActTypeId 作为 properties 键。
	raw, err := schemaFS.ReadFile("schema/actList.json")
	if err != nil {
		t.Fatalf("读 actList.json 失败: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("actList.json 不是合法 JSON: %v", err)
	}
	// 递归找所有 properties 键，确保没有 firstActTypeId。
	var walk func(m map[string]any)
	bad := false
	walk = func(m map[string]any) {
		if p, ok := m["properties"].(map[string]any); ok {
			for k, v := range p {
				if k == "firstActTypeId" {
					bad = true
				}
				if sub, ok := v.(map[string]any); ok {
					walk(sub)
				}
			}
		}
		if items, ok := m["items"].(map[string]any); ok {
			walk(items)
		}
		if one, ok := m["oneOf"].([]any); ok {
			for _, s := range one {
				if sm, ok := s.(map[string]any); ok {
					walk(sm)
				}
			}
		}
	}
	walk(doc)
	if bad {
		t.Error("actList schema 里出现了 firstActTypeId（上游错拼 fistActTypeId 被错误「修正」）")
	}
}

func TestBSchema_TotalScoreValidatedAsNumber(t *testing.T) {
	// 票面：totalScore 按数字校验（实测 11.0，不是字符串，规格 §7.2 第 4 条）。
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	// actList items 若声明 totalScore，必须是 number。
	dt := DataType("actList")
	if err := v.Validate(dt, []byte(`{"schemaVersion":1,"payload":[{"totalScore":11.0}]}`)); err != nil {
		t.Errorf("totalScore 为数字 11.0 应通过: %v", err)
	}
	if err := v.Validate(dt, []byte(`{"schemaVersion":1,"payload":[{"totalScore":"11.0"}]}`)); err == nil {
		t.Error("totalScore 为字符串应被拒（实测是数字，schema 以实测为准）")
	}
}

func TestBSchema_RejectsMalformedEnvelope(t *testing.T) {
	// 与 A 组一致：schemaVersion 必填且为 1；payload 必填；非法 JSON 拒绝。
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	for _, body := range []string{
		`{"payload":{}}`,                   // 缺 schemaVersion
		`{"schemaVersion":2,"payload":{}}`, // schemaVersion 错值
		`{"schemaVersion":1}`,              // 缺 payload
		`{"schemaVersion":`,                // 非法 JSON
	} {
		if err := v.Validate(DataType("actDetail"), []byte(body)); err == nil {
			t.Errorf("畸形回传体 %s 应被拒", body)
		}
	}
}
