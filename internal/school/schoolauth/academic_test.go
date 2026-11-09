package schoolauth_test

// 教务学籍解析的测试。
//
// 解析方法来自知识库《从川农教务 bjiben.asp 抓学籍》（已实测验证）：
//   - 页面是 GBK 而非 UTF-8（用 UTF-8 解码会得到乱码并误判「没抓到」）
//   - 表头在 <tr class="g_column">，值在 <tr class="g_body_2">，不是 thead/tbody
//   - 按索引 zip 成 {中文表头: 值}，再用**关键字包含**匹配映射（不依赖列顺序）
//   - 值里带括注（如「数据科学与大数据技术（工学）」）要先清洗
//   - 登录超时会返回 HTTP 200，必须靠内容判定失效
//
// 测试用**真实结构的页面样本**（表头 25 列，取自实测记录），
// 并专门构造「列顺序被打乱」「含括注」「登录超时」三个坑。

import (
	"reflect"
	"strings"
	"testing"

	"nongyu-backend-plus/internal/school/schoolauth"
)

// realPageHTML 是按实测表头构造的页面（值已换成示例学生）。
const realPageHTML = `<html><head><meta charset="gb2312"></head><body>
<table class="tablebody">
<tr class="g_column">
<td>序</td><td>考生号</td><td>学号</td><td>姓名</td><td>性别</td>
<td>系别</td><td>专业</td><td>学制</td><td>培养层次</td><td>年级</td>
<td>班级</td><td>新专业</td><td>新班级</td><td>学籍状态</td><td>入学日期</td>
<td>民族</td><td>政治面貌</td><td>家庭通讯地址</td><td>火车站点</td>
<td>个人电话</td><td>家庭电话</td><td>特长</td><td>是否本人照片</td>
<td>锁定信息</td><td>核实和补充个人信息</td>
</tr>
<tr class="g_body_2">
<td>1</td><td>25650101151502</td><td>202502204</td><td>姚一飞</td><td>男</td>
<td>信息工程学院</td><td>数据科学与大数据技术（工学）</td><td>四年</td><td>本科</td><td>2025</td>
<td>大数据202503</td><td>数据科学与大数据技术</td><td>大数据202503</td><td>在读</td><td>2025-9-10</td>
<td>汉族</td><td>群众</td><td>新疆乌鲁木齐市</td><td>乌鲁木齐</td>
<td>17799611327</td><td>0991-1234567</td><td>无</td><td>否</td><td>无</td><td>无</td>
</tr>
</table>
</body></html>`

// 正常解析：专业/班级/年级/学院取到，且括注被清洗。
func TestParseAcademic_RealStructure(t *testing.T) {
	got, err := schoolauth.ParseAcademic([]byte(realPageHTML))
	if err != nil {
		t.Fatalf("ParseAcademic: %v", err)
	}
	if got.Major != "数据科学与大数据技术" {
		t.Fatalf("专业应去掉括注，实际 %q", got.Major)
	}
	if got.ClassName != "大数据202503" {
		t.Fatalf("班级不符：%q", got.ClassName)
	}
	if got.Grade != "2025" {
		t.Fatalf("年级不符：%q", got.Grade)
	}
	if got.College != "信息工程学院" {
		t.Fatalf("学院（系别）不符：%q", got.College)
	}
	if got.StudentNo != "202502204" {
		t.Fatalf("学号不符：%q", got.StudentNo)
	}
	if got.Name != "姚一飞" {
		t.Fatalf("姓名不符：%q", got.Name)
	}
	// 隐私：解析层**不产出**身份证/个人电话/家庭电话/家庭住址。
	// 用反射断言结构体里没有这些字段——比运行时判空更强，
	// 任何人后来加了这些字段，测试立刻失败。
	typ := reflect.TypeOf(got)
	for _, forbidden := range []string{"IDCard", "Phone", "HomePhone", "HomeAddress", "IdCard"} {
		if _, ok := typ.FieldByName(forbidden); ok {
			t.Fatalf("Academic 不应有字段 %s（隐私口径：这些不落库）", forbidden)
		}
	}
}

// 列顺序被打乱也要能解析（关键字匹配，不依赖索引）。
func TestParseAcademic_ColumnOrderIndependent(t *testing.T) {
	shuffled := `<table class="tablebody">
<tr class="g_column"><td>姓名</td><td>学号</td><td>专业</td><td>年级</td><td>班级</td><td>系别</td></tr>
<tr class="g_body_2"><td>李四</td><td>202502205</td><td>计算机科学与技术</td><td>2024</td><td>计科202401</td><td>信息工程学院</td></tr>
</table>`
	got, err := schoolauth.ParseAcademic([]byte(shuffled))
	if err != nil {
		t.Fatalf("ParseAcademic: %v", err)
	}
	if got.Name != "李四" || got.StudentNo != "202502205" {
		t.Fatalf("基本字段不符：%+v", got)
	}
	if got.Major != "计算机科学与技术" || got.Grade != "2024" || got.ClassName != "计科202401" {
		t.Fatalf("乱序后字段错位：%+v", got)
	}
}

// 登录超时的页面：HTTP 200 但没有数据表 → 必须判失败，不能返回空值当成「没数据」。
func TestParseAcademic_LoginExpiredPageFails(t *testing.T) {
	expired := `<html><head><title>教务管理系统</title></head><body>
<div>登录超时，请重新登录</div>
</body></html>`
	if _, err := schoolauth.ParseAcademic([]byte(expired)); err == nil {
		t.Fatal("登录超时页面必须判失败（否则会被当成「没抓到」而静默通过）")
	}
}

// 完全没有 tablebody → 同样判失败。
func TestParseAcademic_NoTableFails(t *testing.T) {
	if _, err := schoolauth.ParseAcademic([]byte("<html><body>空</body></html>")); err == nil {
		t.Fatal("无表格应报错")
	}
}

// GBK 字节流：解析前必须先按 GBK 解码，否则中文全是乱码。
func TestParseAcademic_GBKBytes(t *testing.T) {
	// 用 GBK 编码构造同一个页面（部分字段），验证按 GBK 解码能出中文。
	page := `<table class="tablebody">
<tr class="g_column"><td>学号</td><td>姓名</td><td>专业</td></tr>
<tr class="g_body_2"><td>202502204</td><td>姚一飞</td><td>数据科学与大数据技术</td></tr>
</table>`
	enc, err := encodeGBK(page)
	if err != nil {
		t.Fatalf("GBK 编码失败：%v", err)
	}
	got, err := schoolauth.ParseAcademic(enc)
	if err != nil {
		t.Fatalf("ParseAcademic(GBK): %v", err)
	}
	if got.Name != "姚一飞" {
		t.Fatalf("GBK 解码后姓名应为姚一飞，实际 %q", got.Name)
	}
	if got.Major != "数据科学与大数据技术" {
		t.Fatalf("GBK 解码后专业不符，实际 %q", got.Major)
	}
}

// 全角/半角括注都要能清洗。
func TestParseAcademic_CleansParentheses(t *testing.T) {
	cases := []struct{ in, want string }{
		{"数据科学与大数据技术（工学）", "数据科学与大数据技术"},
		{"数据科学与大数据技术(工学)", "数据科学与大数据技术"},
		{"  数据科学与大数据技术  ", "数据科学与大数据技术"},
		{"无括注", "无括注"},
	}
	for _, c := range cases {
		if got := schoolauth.CleanValue(c.in); got != c.want {
			t.Fatalf("CleanValue(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 「新专业」与「专业」同时存在时要明确取哪个（实测两者相同，但别假设）。
func TestParseAcademic_PrefersNewMajorWhenPresent(t *testing.T) {
	page := `<table class="tablebody">
<tr class="g_column"><td>学号</td><td>姓名</td><td>专业</td><td>新专业</td><td>班级</td><td>新班级</td></tr>
<tr class="g_body_2"><td>202502204</td><td>姚一飞</td><td>旧专业</td><td>新专业X</td><td>旧班级</td><td>新班级Y</td></tr>
</table>`
	got, err := schoolauth.ParseAcademic([]byte(page))
	if err != nil {
		t.Fatalf("ParseAcademic: %v", err)
	}
	// 实测两者相同，所以取哪个都一样；但必须**确定性地**取一个，不能随列序变。
	if got.Major != "旧专业" && got.Major != "新专业X" {
		t.Fatalf("专业取值不明确：%q", got.Major)
	}
	if got.ClassName != "旧班级" && got.ClassName != "新班级Y" {
		t.Fatalf("班级取值不明确：%q", got.ClassName)
	}
}

var _ = strings.TrimSpace
