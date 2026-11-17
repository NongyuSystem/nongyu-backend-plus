package secondclass

import (
	"testing"
)

func TestSchemaValidator_CompilesAllTwelveAtStartup(t *testing.T) {
	// 规格 §8.2：go:embed schema 在启动期全部编译通过（fail-fast）。
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("启动期 schema 编译失败: %v", err)
	}
	got := v.DataTypes()
	if len(got) != 12 {
		t.Fatalf("编译出的 schema 应有 12 份，实际 %d: %v", len(got), got)
	}
	have := map[DataType]bool{}
	for _, d := range got {
		have[d] = true
	}
	for _, d := range allTwelve {
		if !have[d] {
			t.Errorf("缺 %q 的 schema", d)
		}
	}
}

func TestSchemaValidator_ValidatesRealFixtures(t *testing.T) {
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	// 两份真实样本：单对象（学期学分）与数组（学分明细，含上游拼写错误 fistActTypeId）。
	body1, _ := submitBodyFromFixture(t, "1-学期学分.json")
	if err := v.Validate(DataTypeSecondclassCredit, body1); err != nil {
		t.Errorf("学期学分样本应通过 secondclassCredit 校验: %v", err)
	}
	body2, _ := submitBodyFromFixture(t, "2-学分明细.json")
	if err := v.Validate(DataTypeCreditRecordDetail, body2); err != nil {
		t.Errorf("学分明细样本应通过 creditRecordDetail 校验: %v", err)
	}
}

func TestSchemaValidator_RejectsMalformedBodies(t *testing.T) {
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	cases := []struct {
		name string
		dt   DataType
		body string
	}{
		{"不是合法 JSON", DataTypeSecondclassCredit, `{"schemaVersion":`},
		{"缺 schemaVersion", DataTypeSecondclassCredit, `{"payload":{}}`},
		{"schemaVersion 错值", DataTypeSecondclassCredit, `{"schemaVersion":2,"payload":{}}`},
		{"缺 payload", DataTypeSecondclassCredit, `{"schemaVersion":1}`},
		{"数组类型给了对象", DataTypeCreditRecordDetail, `{"schemaVersion":1,"payload":{"x":1}}`},
		{"对象类型给了数组", DataTypeSecondclassCredit, `{"schemaVersion":1,"payload":[1,2]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := v.Validate(tc.dt, []byte(tc.body)); err == nil {
				t.Errorf("应拒绝该回传体")
			}
		})
	}
}

func TestSchemaValidator_UnknownDataTypeHasNoSchema(t *testing.T) {
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	if err := v.Validate(DataType("notARealType"), []byte(`{"schemaVersion":1,"payload":{}}`)); err == nil {
		t.Error("未知 dataType 应校验失败")
	}
	if err := (*SchemaValidator)(nil).Validate(DataTypeRanking, []byte(`{}`)); err == nil {
		t.Error("nil 校验器应报错而非放行")
	}
}
