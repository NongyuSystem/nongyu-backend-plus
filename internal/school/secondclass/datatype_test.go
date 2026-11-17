package secondclass

import (
	"testing"
	"time"
)

// 规格 §6.8：A 组 13 接口归并为 12 个 dataType。
var allTwelve = []DataType{
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

func TestDataType_ControlledVocabularyCoversAuthoritativeTable(t *testing.T) {
	got := KnownDataTypes()
	// 词表必须逐项覆盖权威表 dataTypeSpecs（A 组 12 + B 组 #74 追加项）。
	// 不再断言固定数量 12：#74 起词表随 B 组扩展，固定值会误报。
	if len(got) != len(dataTypeSpecs) {
		t.Fatalf("受控词表应与 dataTypeSpecs 一一对应（%d 项），实际 %d 项: %v", len(dataTypeSpecs), len(got), got)
	}
	seen := map[DataType]bool{}
	for _, d := range got {
		if seen[d] {
			t.Errorf("KnownDataTypes 有重复项 %q", d)
		}
		seen[d] = true
		if _, ok := dataTypeSpecs[d]; !ok {
			t.Errorf("KnownDataTypes 含权威表之外的项 %q", d)
		}
	}
	for _, d := range allTwelve {
		if !seen[d] {
			t.Errorf("受控词表缺 A 组 %q", d)
		}
	}
}

func TestDataType_ParseRejectsUnknownNames(t *testing.T) {
	// 未知 / 大小写不符 / 空串 / 疑似注入一律拒绝，绝不宽容匹配。
	for _, s := range []string{
		"", "course", "score", "SECONDCLASSCREDIT", "secondclassCredit ",
		"../etc", "secondclassCredit:evil", "ranking\n", "ranking/../../x",
	} {
		if _, ok := ParseDataType(s); ok {
			t.Errorf("ParseDataType(%q) 不应通过白名单", s)
		}
	}
	for _, d := range allTwelve {
		if _, ok := ParseDataType(string(d)); !ok {
			t.Errorf("ParseDataType(%q) 应通过", d)
		}
	}
}

func TestDataType_TTLUsesFourGroups(t *testing.T) {
	// 规格 §6.10：二课是四组（短/中/长/最长），不是教务的 10/30/60min 三组。
	groups := map[time.Duration]int{}
	for _, d := range allTwelve {
		groups[d.TTL()]++
		if d.TTL() == 10*time.Minute || d.TTL() == 30*time.Minute || d.TTL() == time.Hour {
			t.Errorf("%q 的 TTL 落到了教务三档之一: %v", d, d.TTL())
		}
		if d.TTL() < 10*time.Minute || d.TTL() > 24*time.Hour {
			t.Errorf("%q 的 TTL 超出规格建议区间: %v", d, d.TTL())
		}
	}
	if len(groups) != 4 {
		t.Fatalf("应有 4 个不同 TTL 分组，实际 %d: %v", len(groups), groups)
	}
	if ttlShort >= ttlMedium || ttlMedium >= ttlLong || ttlLong >= ttlLongest {
		t.Errorf("四组 TTL 未严格递增: %v %v %v %v", ttlShort, ttlMedium, ttlLong, ttlLongest)
	}
	// 四组各自落在规格建议区间内。
	if ttlShort < 10*time.Minute || ttlShort > 15*time.Minute {
		t.Errorf("短组应在 10~15m: %v", ttlShort)
	}
	if ttlMedium < 30*time.Minute || ttlMedium > 60*time.Minute {
		t.Errorf("中组应在 30~60m: %v", ttlMedium)
	}
	if ttlLong < 6*time.Hour || ttlLong > 12*time.Hour {
		t.Errorf("长组应在 6~12h: %v", ttlLong)
	}
	if ttlLongest != 24*time.Hour {
		t.Errorf("最长组应为 24h: %v", ttlLongest)
	}
}

func TestDataType_TTLGroupAssignments(t *testing.T) {
	// 规格 §6.8 表逐行核对。
	want := map[DataType]time.Duration{
		DataTypeSecondclassCredit:  ttlLong,
		DataTypeApplyCredit:        ttlLong,
		DataTypeCreditRule:         ttlLongest,
		DataTypeGoodFaithValue:     ttlMedium,
		DataTypeCreditRecordDetail: ttlLong,
		DataTypeImportCreditDetail: ttlLong,
		DataTypePerIntegrateDetail: ttlLong,
		DataTypeRanking:            ttlLong,
		DataTypeReportCard:         ttlLong,
		DataTypeReportCardSetting:  ttlLong,
		DataTypeCreditDetailOfType: ttlLong,
		DataTypeReportCardPreview:  ttlShort,
	}
	for d, w := range want {
		if got := d.TTL(); got != w {
			t.Errorf("%q TTL = %v，应为 %v", d, got, w)
		}
	}
	// TTL() 对未知类型给保守兜底，不 panic。
	_ = DataType("nope").TTL()
}

func TestDataType_EveryTypeHasDeclaredShape(t *testing.T) {
	// 每个 dataType 都必须在权威表里带一个合法 shape（规格 §6.8「每个有 TTL 组与 shape」）。
	valid := map[dataShape]bool{shapeArray: true, shapeObject: true, shapeWrapper: true}
	counts := map[dataShape]int{}
	for _, d := range allTwelve {
		s := d.Shape()
		if !valid[s] {
			t.Errorf("%q 的 shape 非法: %v", d, s)
		}
		counts[s]++
	}
	if counts[shapeArray] == 0 || counts[shapeObject] == 0 {
		t.Errorf("词表应同时含数组与单对象形态，实际: %v", counts)
	}
}
