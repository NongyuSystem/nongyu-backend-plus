package secondclass

import (
	"encoding/json"
	"testing"
)

// 规格 §8.2：配方表完整性——12 个 id 唯一、七字段齐全、槽位合法、origin 取值合法。
func TestRecipeTable_CompleteForAllTwelveDataTypes(t *testing.T) {
	ids := map[string]bool{}
	for _, dt := range allTwelve {
		r, ok := RecipeFor(dt)
		if !ok {
			t.Errorf("%q 没有配方", dt)
			continue
		}
		if ids[r.ID] {
			t.Errorf("配方 id 重复: %q", r.ID)
		}
		ids[r.ID] = true
		if r.ID == "" || r.Target != "secondclass" || r.Path == "" {
			t.Errorf("%q 配方缺 id/target/path: %+v", dt, r)
		}
		if r.Method != "POST" && r.Method != "GET" {
			t.Errorf("%q 配方 method 非法: %q", dt, r.Method)
		}
		if r.ContentType != "form" && r.ContentType != "none" && r.ContentType != "multipart" {
			t.Errorf("%q 配方 contentType 非法: %q", dt, r.ContentType)
		}
		if r.Auth.In != "header" || r.Auth.Name != "x-access-token" || r.Auth.From != "client" {
			t.Errorf("%q 配方 auth 必须是客户端自带的 header 凭据: %+v", dt, r.Auth)
		}
		for _, p := range r.Params {
			switch p.Origin {
			case "client_local", "fixed", "optional":
			default:
				t.Errorf("%q 配方参数 %q 的 origin 非法: %q", dt, p.Name, p.Origin)
			}
			if p.Name == "" {
				t.Errorf("%q 配方有空参数名", dt)
			}
		}
		// headers 形状：{name,value}，不得含 token（全禁入响应）。
		for _, h := range r.Headers {
			if h.Name == "" {
				t.Errorf("%q 配方有空 header 名", dt)
			}
		}
	}
	if len(ids) != 12 {
		t.Errorf("应有 12 个唯一配方 id，实际 %d", len(ids))
	}
	if _, ok := RecipeFor(DataType("bogus")); ok {
		t.Error("未知 dataType 不应有配方")
	}
}

// 配方序列化后必须是规格 §6.3 的七字段对象（外加 §6.4/§6.6/§6.7 的子结构），
// 且不含教务侧的 parser/cookies/session 字段（「不要照抄」清单第 1 条）。
func TestRecipe_SerializesToContractFieldsOnly(t *testing.T) {
	r, _ := RecipeFor(DataTypeSecondclassCredit)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("序列化配方失败: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("配方不是 JSON 对象: %v", err)
	}
	for _, k := range []string{"id", "target", "method", "path", "contentType", "auth", "params", "fields", "headers"} {
		if _, ok := obj[k]; !ok {
			t.Errorf("配方缺字段 %q", k)
		}
	}
	for _, k := range []string{"parser", "cookies", "session", "address", "mode", "needs", "basePath"} {
		if _, ok := obj[k]; ok {
			t.Errorf("配方不应含教务侧字段 %q", k)
		}
	}
}

// 规格 §7：上游拼写错误 fistActTypeId 须原样保留，schema 不得「修正」它。
func TestSchema_PreservesUpstreamTypoFistActTypeId(t *testing.T) {
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	// 真实夹具里用的是 fistActTypeId（缺 r）。若 schema 误写成 firstActTypeId，
	// 这份样本会因 additionalProperties:false 之类约束被拒——这里确保它照常通过。
	body, _ := submitBodyFromFixture(t, "2-学分明细.json")
	if err := v.Validate(DataTypeCreditRecordDetail, body); err != nil {
		t.Fatalf("含 fistActTypeId 的真实样本应通过: %v", err)
	}
	// 把拼写「修正」成 firstActTypeId 后应被拒（证明 schema 认的是上游那个错拼）。
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	_ = m
	fixed := `{"schemaVersion":1,"payload":[{"firstTypeName":"智育","firstTypeId":"x","totalScore":11.0,"content":[{"creditRecordId":"1","eventName":"e","validCredit":0.5,"display":0,"firstActTypeId":"x","firstActTypeName":"智育","grantTime":"2025-11-18"}]}]}`
	if err := v.Validate(DataTypeCreditRecordDetail, []byte(fixed)); err == nil {
		t.Error("把 fistActTypeId 改成 firstActTypeId 后应被拒（上游拼写须原样保留）")
	}
}

// 规格 §7 / §8.2：code 双类型解析——字符串 "0" 与数字 6 都要能正确判定。
func TestParseUpstreamCode_HandlesBothStringAndNumber(t *testing.T) {
	cases := []struct {
		raw      string
		wantCode int
		wantOK   bool
	}{
		{`{"code":"0","message":"成功","content":{}}`, 0, true},
		{`{"code":6,"message":"用户未登录"}`, 6, true},
		{`{"code":"2","message":"用户不存在"}`, 2, true},
		{`{"code":"104","message":"手机格式错误"}`, 104, true},
		{`{"code":0}`, 0, true},
		{`{"code":"abc"}`, 0, false},
		{`{"message":"成功"}`, 0, false},
		{`not json`, 0, false},
	}
	for _, tc := range cases {
		got, ok := ParseUpstreamCode([]byte(tc.raw))
		if ok != tc.wantOK || (ok && got != tc.wantCode) {
			t.Errorf("ParseUpstreamCode(%s) = (%d,%v)，应为 (%d,%v)", tc.raw, got, ok, tc.wantCode, tc.wantOK)
		}
	}
}

// 真实失败夹具：{"code": 6, ...} 没有 content，且是数字 code。
func TestParseUpstreamCode_RealFailureFixture(t *testing.T) {
	raw := readFixture(t, "3-未登录失败.json")
	code, ok := ParseUpstreamCode(raw)
	if !ok || code != 6 {
		t.Errorf("未登录失败夹具应解析出数字 code=6，实际 (%d,%v)", code, ok)
	}
}
