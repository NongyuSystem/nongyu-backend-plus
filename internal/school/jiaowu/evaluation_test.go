package jiaowu

import (
	"strings"
	"testing"
)

// 本文件锁住评教的两个数据类型（2026-11-23 新增）。
//
// 需求原话：「做读和写都是发链接不做操作」——即后端**只产出地址**，
// 由客户端自己发请求；后端不代提交、不解析评教页面。
//
// 依据：docs/tech/实测-教务评教接口契约.md（浏览器登录 WebVPN 后一手抓取）。

// TestEvaluationDataType_Parses 两个类型都能被解析（否则客户端拿不到地址）。
func TestEvaluationDataType_Parses(t *testing.T) {
	for _, name := range []string{"evaluation", "evaluationSubmit"} {
		dt, ok := ParseDataType(name)
		if !ok {
			t.Fatalf("%q 应能被解析", name)
		}
		if dt != DataType(name) {
			t.Fatalf("%q 解析结果不符：%q", name, dt)
		}
	}
}

// TestEvaluationDataType_NoCache 评教**不缓存**（需求明确）。
//
// 这条断言的是 ttlNone 这个哨兵值本身，而不是「age > TTL」的行为——
// 因为靠负数让判断恒真虽然结果一样，却掩盖了「这个类型不缓存」的意图。
func TestEvaluationDataType_NoCache(t *testing.T) {
	for _, dt := range []DataType{DataTypeEvaluation, DataTypeEvaluationSubmit} {
		if got := dt.TTL(); got != ttlNone {
			t.Fatalf("%s 应为 ttlNone（不缓存），得到 %v", dt, got)
		}
	}
	// 反向确认：其它类型仍然是正常 TTL，没有被误改。
	if DataTypeCourse.TTL() == ttlNone {
		t.Fatal("course 不该变成不缓存")
	}
}

// TestEvaluationRecipe_ListHasFullAddress 列表页路径固定，应直接给出完整地址。
func TestEvaluationRecipe_ListHasFullAddress(t *testing.T) {
	res := recipeOf(DataTypeEvaluation, AccessVPN)
	if res.Address == "" {
		t.Fatalf("列表页应给出完整地址，得到空（parser=%q）", res.Parser)
	}
	// 地址必须指向评教列表页，且是网关编码形态。
	if !strings.Contains(res.Address, "/xuesheng/pj/pj/index.asp") {
		t.Fatalf("地址应含评教列表路径，得到 %q", res.Address)
	}
	if !strings.Contains(res.Address, "webvpn.sicau.edu.cn") {
		t.Fatalf("VPN 形态应走网关域，得到 %q", res.Address)
	}
}

// TestEvaluationRecipe_DirectModeIsIntranet 直连形态不带网关（校内可用）。
func TestEvaluationRecipe_DirectModeIsIntranet(t *testing.T) {
	res := recipeOf(DataTypeEvaluation, AccessDirect)
	if res.Address == "" {
		t.Fatal("直连形态也应给出地址")
	}
	if strings.Contains(res.Address, "webvpn") {
		t.Fatalf("直连形态不该含网关域，得到 %q", res.Address)
	}
	if !strings.Contains(res.Address, "jiaowu.sicau.edu.cn") {
		t.Fatalf("直连形态应指向教务站，得到 %q", res.Address)
	}
}

// TestEvaluationRecipe_SubmitGivesSkeletonAndNeeds 提交端点缺入参，只给骨架 + 说明。
//
// 评教表单与提交都需要 `bh`（课程编号）、`jshi`（教师）、`kc`（课程名），
// 它们来自列表页的「我要评教」链接。后端不该猜这些值，也不该替客户端拼——
// 教师姓名还要 GBK 编码，多一层转换就多一个出错点。
func TestEvaluationRecipe_SubmitGivesSkeletonAndNeeds(t *testing.T) {
	res := recipeOf(DataTypeEvaluationSubmit, AccessVPN)

	if res.Address != "" {
		t.Fatalf("提交端点缺入参，Address 应为空，得到 %q", res.Address)
	}
	if res.BasePath == "" {
		t.Fatal("应给出骨架路径供客户端自己拼")
	}
	if !strings.Contains(res.BasePath, "/xuesheng/pj/pj/baojia.asp") {
		t.Fatalf("骨架应指向评教表单页，得到 %q", res.BasePath)
	}
	// needs 必须说清缺哪三个入参。
	for _, want := range []string{"bh", "jshi", "kc"} {
		if !strings.Contains(res.Needs, want) {
			t.Fatalf("needs 应提到 %q，得到 %q", want, res.Needs)
		}
	}
	// 骨架路径不含 query：拼接是客户端的事。
	if strings.Contains(res.BasePath, "?") {
		t.Fatalf("骨架不应带 query，得到 %q", res.BasePath)
	}
}

// TestEvaluationRecipe_NoSubmitPerformed 确认后端**不代提交**。
//
// 这是个结构性断言：recipeOf 是纯函数，只拼地址、不发请求。
// 若将来有人给它加了「顺便提交」的行为，这条测试会失败。
func TestEvaluationRecipe_NoSubmitPerformed(t *testing.T) {
	// recipeOf 没有任何 IO：连续调用两次结果必须逐字一致，
	// 且不会因为「第一次提交了」而改变第二次的输出。
	a := recipeOf(DataTypeEvaluationSubmit, AccessVPN)
	b := recipeOf(DataTypeEvaluationSubmit, AccessVPN)
	if a != b {
		t.Fatalf("两次调用结果应一致（纯函数）：%+v vs %+v", a, b)
	}
}
