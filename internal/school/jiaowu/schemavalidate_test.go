package jiaowu

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSchemaValidator_AllDataTypesCompiled 确认 15 个受控类型都装载了 schema。
//
// 这是启动期 fail-fast 的验证：漏一份 schema 就意味着那类回传永远不会被校验，
// 而它不会有任何运行时症状——只有本用例能发现。
func TestSchemaValidator_AllDataTypesCompiled(t *testing.T) {
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("NewSchemaValidator 失败: %v", err)
	}

	got := v.DataTypes()
	want := KnownDataTypes()
	if len(got) != len(want) {
		t.Fatalf("schema 数量 = %d, 期望 %d", len(got), len(want))
	}

	seen := make(map[DataType]bool, len(got))
	for _, dt := range got {
		seen[dt] = true
	}
	for _, dt := range want {
		if !seen[dt] {
			t.Errorf("dataType %q 没有对应的已编译 schema", dt)
		}
	}
}

// goodSamples 是 15 类各自的最小合法回传体。
//
// 每一条都刻意只带 schema 要求的必填字段，用来验证「最小合法」确实能通过——
// 如果某类必填字段设错了，这里会立刻暴露。
var goodSamples = map[DataType]string{
	DataTypeCourse: `{"schemaVersion":1,"payload":[
		{"courseName":"Web设计与应用","weeks":"1-16","scheduleTime":"3-3,3-4","credit":"2"}]}`,
	DataTypeScore: `{"schemaVersion":1,"payload":[
		{"courseName":"高等数学","score":"90","credit":"4","term":"2023-2024-1"}]}`,
	DataTypeExam: `{"schemaVersion":1,"payload":[
		{"courseName":"高等数学","examTime":"【考试时间：2026-7-2】 第18周","examRoom":"雅安校区:10-A303","seatNumber":"17"}]}`,
	DataTypeProgress: `{"schemaVersion":1,"payload":[
		{"type":"必修","required":"105","earned":"90.5","diff":"-14.5","transfer":"/","progress":"86.19%"}]}`,
	DataTypeRank: `{"schemaVersion":1,"payload":{
		"studentId":"20230XXXXX","name":"张XX","majorRank":"114","weightedScore":"81.71"}}`,
	DataTypePlan: `{"schemaVersion":1,"payload":{
		"title":"物联网工程2023","courses":[
			{"courseCode":"1210004000","courseName":"C语言程序设计","weeklyHours":["40","","","","","","","","",""]}]}}`,
	DataTypePersonal: `{"schemaVersion":1,"payload":{
		"name":"张XX","studentId":"20230XXXXX","college":"信息工程学院","major":"大数据"}}`,
	DataTypeNotice: `{"schemaVersion":1,"payload":[
		{"title":"关于选课的通知","url":"https://jwnews.sicau.edu.cn/jwnews/view.asp?id=1396","date":"06-16"}]}`,
	DataTypeTeachingNotice: `{"schemaVersion":1,"payload":[
		{"title":"关于选课的通知","url":"https://jwnews.sicau.edu.cn/jwnews/view.asp?id=1396"}]}`,
	DataTypeCompetition: `{"schemaVersion":1,"payload":[
		{"title":"数学建模竞赛通知","url":"https://jwnews.sicau.edu.cn/jwnews/view.asp?id=2001"}]}`,
	DataTypeClassroomInfo: `{"schemaVersion":1,"payload":[
		{"index":"1","location":"10-A104","campus":"雅安","capacity":"188","classroomId":"abc"}]}`,
	DataTypeTeacherInfo: `{"schemaVersion":1,"payload":{
		"name":"张XX","department":"信息工程学院","title":"副教授"}}`,
	DataTypeTeacherCourse: `{"schemaVersion":1,"payload":{
		"teacherName":"张XX","semester":"2025-2026-2","slots":[
			{"dayOfWeek":1,"period":"上午","slot":"12节","courses":[
				{"courseName":"数据结构","weekPattern":"单","isExperiment":false,"isContinuous":true}]}]}}`,
	DataTypeClassroomCourse: `{"schemaVersion":1,"payload":{
		"classroomName":"10-A104","semester":"","slots":[
			{"dayOfWeek":3,"period":"上午","slot":"34节","courses":[
				{"courseName":"射频识别原理与应用","courseType":"必修","weekPattern":""}]}]}}`,
	DataTypeKaike: `{"schemaVersion":1,"payload":{
		"semester":"2025-2026-2","courses":[
			{"index":"1","courseName":"形势与政策","scheduleTime":"1-5,1-6","weekRange":"10-13"}]}}`,
}

// TestValidate_AcceptsGoodSamples 验证 15 类的最小合法体全部通过。
func TestValidate_AcceptsGoodSamples(t *testing.T) {
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("NewSchemaValidator 失败: %v", err)
	}

	for _, dt := range KnownDataTypes() {
		sample, ok := goodSamples[dt]
		if !ok {
			t.Errorf("dataType %q 缺少好样例，无法验证其 schema 是否可接受最小合法体", dt)
			continue
		}
		if err := v.Validate(dt, []byte(sample)); err != nil {
			t.Errorf("%s 的最小合法体被拒: %v", dt, err)
		}
	}
}

// TestValidate_RejectsBadSamples 是这道闸存在的理由：
// 每类至少一个坏样例必须被拒（#41 验收第 2 条）。
//
// 坏样例覆盖 #38 列出的 6 种失败形态中最容易漏进缓存的那几种。
func TestValidate_RejectsBadSamples(t *testing.T) {
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("NewSchemaValidator 失败: %v", err)
	}

	cases := []struct {
		name string
		dt   DataType
		body string
	}{
		{"课表缺 courseName（空壳行）", DataTypeCourse,
			`{"schemaVersion":1,"payload":[{"weeks":"1-16","scheduleTime":"3-3"}]}`},
		{"课表 courseName 空串", DataTypeCourse,
			`{"schemaVersion":1,"payload":[{"courseName":""}]}`},
		{"成绩缺 score", DataTypeScore,
			`{"schemaVersion":1,"payload":[{"courseName":"高等数学"}]}`},
		{"考试成绩与课名都缺（无法定位任何一行）", DataTypeExam,
			`{"schemaVersion":1,"payload":[{"examRoom":"10-A303"}]}`},
		{"排名缺 studentId", DataTypeRank,
			`{"schemaVersion":1,"payload":{"name":"张XX"}}`},
		{"学籍缺 name", DataTypePersonal,
			`{"schemaVersion":1,"payload":{"studentId":"20230XXXXX"}}`},
		{"通知缺 url", DataTypeNotice,
			`{"schemaVersion":1,"payload":[{"title":"某通知"}]}`},
		{"教室序号非数字", DataTypeClassroomInfo,
			`{"schemaVersion":1,"payload":[{"index":"一","location":"10-A104"}]}`},
		{"教师课表 dayOfWeek 越界", DataTypeTeacherCourse,
			`{"schemaVersion":1,"payload":{"teacherName":"张XX","semester":"","slots":[
				{"dayOfWeek":8,"period":"上午","slot":"12节","courses":[]}]}}`},
		{"教师课表 dayOfWeek 是字符串", DataTypeTeacherCourse,
			`{"schemaVersion":1,"payload":{"teacherName":"张XX","semester":"","slots":[
				{"dayOfWeek":"1","period":"上午","slot":"12节","courses":[]}]}}`},
		{"教室课表 courseType 不在枚举内", DataTypeClassroomCourse,
			`{"schemaVersion":1,"payload":{"classroomName":"10-A104","semester":"","slots":[
				{"dayOfWeek":1,"period":"上午","slot":"12节","courses":[
					{"courseName":"某课","courseType":"随便写的"}]}]}}`},
		{"weekPattern 用了旧的「单周」写法", DataTypeTeacherCourse,
			`{"schemaVersion":1,"payload":{"teacherName":"张XX","semester":"","slots":[
				{"dayOfWeek":1,"period":"上午","slot":"12节","courses":[
					{"courseName":"某课","weekPattern":"单周"}]}]}}`},
		{"skeleton 完全不是信封", DataTypeCourse, `{"result":[{"courseName":"某课"}]}`},
		{"schemaVersion 不是 1", DataTypeCourse, `{"schemaVersion":2,"payload":[]}`},
		{"payload 该是数组却给了对象", DataTypeCourse, `{"schemaVersion":1,"payload":{"courseName":"某课"}}`},
		{"payload 该是对象却给了数组", DataTypeRank, `{"schemaVersion":1,"payload":[]}`},
		{"根本不是 JSON", DataTypeCourse, `<html>登录超时</html>`},
		{"空体", DataTypeCourse, ``},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := v.Validate(tc.dt, []byte(tc.body)); err == nil {
				t.Fatalf("坏样例被接受了（这正是 #41 要挡的脏数据）: %s", tc.body)
			}
		})
	}
}

// TestValidate_NullPayloadAllowed 验证单对象/包装对象类允许 payload 为 null。
//
// #41 查明 rank/personal/teacherInfo 是 `T | null`，plan/kaike 等包装对象也可为 null
// （旧仓解析器找不到表时返回 null）。把 null 一律拒掉会误伤「确实没有数据」这一合法状态。
func TestValidate_NullPayloadAllowed(t *testing.T) {
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("NewSchemaValidator 失败: %v", err)
	}

	for _, dt := range []DataType{DataTypeRank, DataTypePersonal, DataTypeTeacherInfo, DataTypePlan, DataTypeKaike} {
		if err := v.Validate(dt, []byte(`{"schemaVersion":1,"payload":null}`)); err != nil {
			t.Errorf("%s 的 payload=null 被拒: %v", dt, err)
		}
	}

	// 但数组类不接受 null：它们的空状态是 []，不是 null。
	for _, dt := range []DataType{DataTypeCourse, DataTypeScore, DataTypeNotice} {
		if err := v.Validate(dt, []byte(`{"schemaVersion":1,"payload":null}`)); err == nil {
			t.Errorf("%s 的 payload=null 被接受了，但数组类的空状态应是 []", dt)
		}
	}
}

// TestValidate_RejectsOversizedCollection 验证数量护栏（#41 Q3 的防御性上限）。
func TestValidate_RejectsOversizedCollection(t *testing.T) {
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("NewSchemaValidator 失败: %v", err)
	}

	// course 的上限是 500 条，造 501 条。
	var sb strings.Builder
	sb.WriteString(`{"schemaVersion":1,"payload":[`)
	for i := 0; i < 501; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"courseName":"课程"}`)
	}
	sb.WriteString(`]}`)

	if err := v.Validate(DataTypeCourse, []byte(sb.String())); err == nil {
		t.Fatal("超过 500 条的课表被接受了，数量护栏失效")
	}
}

// TestParseDataType_OnlyAllowsWhitelist 验证 dataType 是受控词表。
func TestParseDataType_OnlyAllowsWhitelist(t *testing.T) {
	for _, name := range []string{"course", "score", "exam", "kaike", "classroomCourse"} {
		if _, ok := ParseDataType(name); !ok {
			t.Errorf("%q 是合法 dataType，却被拒", name)
		}
	}
	for _, name := range []string{"", "Course", "COURSE", "unknown", "../etc/passwd", "course ", "course\n"} {
		if _, ok := ParseDataType(name); ok {
			t.Errorf("%q 不是合法 dataType，却被接受", name)
		}
	}
}

// TestDataTypeTTL 验证 TTL 分组与 #32 决议一致。
func TestDataTypeTTL(t *testing.T) {
	const (
		tenMin    = 10 * time.Minute
		thirtyMin = 30 * time.Minute
		oneHour   = time.Hour
	)

	want := map[DataType]time.Duration{
		DataTypeNotice:          tenMin,
		DataTypeTeachingNotice:  tenMin,
		DataTypeCompetition:     tenMin,
		DataTypeExam:            thirtyMin,
		DataTypeCourse:          oneHour,
		DataTypeScore:           oneHour,
		DataTypeRank:            oneHour,
		DataTypePlan:            oneHour,
		DataTypeProgress:        oneHour,
		DataTypePersonal:        oneHour,
		DataTypeTeacherInfo:     oneHour,
		DataTypeClassroomInfo:   oneHour,
		DataTypeTeacherCourse:   oneHour,
		DataTypeClassroomCourse: oneHour,
		DataTypeKaike:           oneHour,
	}
	for dt, wantTTL := range want {
		if got := dt.TTL(); got != wantTTL {
			t.Errorf("%s 的 TTL = %v, 期望 %v", dt, got, wantTTL)
		}
	}
}

// TestSchemaFiles_MatchShapes 用解码后的信封交叉核对：数组类的 payload 是数组，
// 单对象/包装类的 payload 是对象。防止 schema 文件被复制粘贴改错。
func TestSchemaFiles_MatchShapes(t *testing.T) {
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("NewSchemaValidator 失败: %v", err)
	}

	for dt, sample := range goodSamples {
		var probe struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(sample), &probe); err != nil {
			t.Fatalf("%s 的好样例本身不是合法 JSON: %v", dt, err)
		}
		trimmed := strings.TrimSpace(string(probe.Payload))
		isArray := strings.HasPrefix(trimmed, "[")

		switch dt.Shape() {
		case shapeArray:
			if !isArray {
				t.Errorf("%s 被标为数组类，但好样例的 payload 不是数组", dt)
			}
		case shapeObject, shapeWrapper:
			if isArray {
				t.Errorf("%s 被标为对象类，但好样例的 payload 是数组", dt)
			}
		}

		// 无论哪种形状，好样例都必须能通过自己的 schema。
		if err := v.Validate(dt, []byte(sample)); err != nil {
			t.Errorf("%s 好样例校验失败: %v", dt, err)
		}
	}
}
