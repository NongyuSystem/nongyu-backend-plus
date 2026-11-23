package classroom_test

import (
	"testing"

	"nongyu-backend-plus/internal/classroom"
)

// 本文件锁住「教室名 → 楼栋键」的归一化规则。
//
// 这是整个功能的地基：算错了，学生就会被导向错误的楼栋。
// 所以宁可「查不到」也不要「猜」——下面专门有一条用例守这个边界。

// TestParseKey_RequirementExamples 需求里给的原始例子。
func TestParseKey_RequirementExamples(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// 「10-B201室 和 10-B213室 都是十教B」
		{"10-B201", "10-B"},
		{"10-B213", "10-B"},
		// 「1-404室 和 1-301室 都是一教」
		{"1-404", "1"},
		{"1-301", "1"},
	}
	for _, c := range cases {
		got, ok := classroom.ParseKey(c.in)
		if !ok {
			t.Fatalf("%q 应能解析", c.in)
		}
		if got != c.want {
			t.Fatalf("%q → %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestParseKey_SameBuildingCollapses 同一楼栋的不同教室必须归并到同一个键。
//
// 这是需求的核心：粒度是楼栋不是教室。
func TestParseKey_SameBuildingCollapses(t *testing.T) {
	groups := map[string][]string{
		"10-B": {"10-B201", "10-B213", "10-B102", "10-B005"},
		"1":    {"1-404", "1-301", "1-101", "1-505"},
		"2":    {"2-201", "2-105"},
	}
	for want, inputs := range groups {
		for _, in := range inputs {
			got, ok := classroom.ParseKey(in)
			if !ok || got != want {
				t.Fatalf("%q → (%q,%v)，期望 %q", in, got, ok, want)
			}
		}
	}
}

// TestParseKey_CaseAndSeparatorVariants 大小写与全角分隔符都要能处理。
//
// 用户从教务页面/文档里复制过来时，这两种变体非常常见。
func TestParseKey_CaseAndSeparatorVariants(t *testing.T) {
	cases := map[string]string{
		"10-b201":     "10-B",
		"10－B201":     "10-B", // 全角连字符
		"10—B201":     "10-B", // em dash
		"  10-B201  ": "10-B",
		"10-B201室":    "10-B",
	}
	for in, want := range cases {
		got, ok := classroom.ParseKey(in)
		if !ok || got != want {
			t.Fatalf("%q → (%q,%v)，期望 %q", in, got, ok, want)
		}
	}
}

// TestParseKey_OnlyBuildingNo 只给楼栋号也要能查（管理员/用户可能这么输入）。
func TestParseKey_OnlyBuildingNo(t *testing.T) {
	cases := map[string]string{
		"10":  "10",
		"1":   "1",
		" 3 ": "3",
	}
	for in, want := range cases {
		got, ok := classroom.ParseKey(in)
		if !ok || got != want {
			t.Fatalf("%q → (%q,%v)，期望 %q", in, got, ok, want)
		}
	}
}

// TestParseKey_RefusesToGuess 无法识别时必须返回 ok=false，绝不猜。
//
// 「猜一个楼栋」会让用户走到错的楼，比「查不到」糟糕得多。
func TestParseKey_RefusesToGuess(t *testing.T) {
	bad := []string{
		"",        // 空
		"   ",     // 空白
		"教学楼",     // 中文，无数字
		"B",       // 纯字母
		"-201",    // 缺楼栋号
		"A-B201",  // 楼栋号必须是数字（A 不是楼栋号）
		"10 B201", // 空格不是分隔符（无法确定是楼栋还是教室）
		"10/B201", // 斜杠不是分隔符
	}
	for _, in := range bad {
		if got, ok := classroom.ParseKey(in); ok {
			t.Fatalf("%q 不该被解析成 %q", in, got)
		}
	}
}

// TestNormalizeKey_MatchesParseKey 管理员录入的键与查询算出的键必须同格式。
//
// 这条守的是「两处算法一致」——若不一致，整个功能静默失效
// （录入成功、查询永远查不到，而且不报错）。
func TestNormalizeKey_MatchesParseKey(t *testing.T) {
	cases := []struct {
		no, zone string
		sample   string // 从这个教室名解析，应与 NormalizeKey 结果相同
	}{
		{"10", "B", "10-B201"},
		{"10", "b", "10-b213"},
		{"1", "", "1-404"},
		{"2", "", "2-105"},
	}
	for _, c := range cases {
		want, ok := classroom.NormalizeKey(c.no, c.zone)
		if !ok {
			t.Fatalf("NormalizeKey(%q,%q) 应成功", c.no, c.zone)
		}
		got, ok := classroom.ParseKey(c.sample)
		if !ok {
			t.Fatalf("ParseKey(%q) 应成功", c.sample)
		}
		if got != want {
			t.Fatalf("录入键 %q 与查询键 %q 不一致（教室 %q）", want, got, c.sample)
		}
	}
}

// TestNormalizeKey_RejectsBadInput 管理员输错时要报错，不能静默产出怪键。
func TestNormalizeKey_RejectsBadInput(t *testing.T) {
	bad := [][2]string{
		{"", ""},
		{"A", ""},      // 楼栋号无数字
		{"10", "BC"},   // 分区必须是单字母
		{"10", "1"},    // 分区不能是数字
		{"10 ", " B "}, // 带空格应被 trim 后接受 → 这条其实合法，见下
	}
	for _, c := range bad[:4] {
		if got, ok := classroom.NormalizeKey(c[0], c[1]); ok {
			t.Fatalf("NormalizeKey(%q,%q) 应失败，得到 %q", c[0], c[1], got)
		}
	}
	// 带空格是合法的（会自动 trim）。
	if got, ok := classroom.NormalizeKey(" 10 ", " B "); !ok || got != "10-B" {
		t.Fatalf("带空格应被 trim 后接受，得到 (%q,%v)", got, ok)
	}
}

// TestParseRoomNo 房间号只用于展示。
func TestParseRoomNo(t *testing.T) {
	cases := map[string]string{
		"10-B201": "B201",
		"1-404":   "404",
		"10":      "",
	}
	for in, want := range cases {
		if got := classroom.ParseRoomNo(in); got != want {
			t.Fatalf("ParseRoomNo(%q) = %q，期望 %q", in, got, want)
		}
	}
}
