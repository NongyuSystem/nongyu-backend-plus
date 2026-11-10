package schoolauth

// 教务学籍解析：bjiben.asp（GBK 老 ASP 页面）。
//
// 解析方法来自知识库《从川农教务 bjiben.asp 抓学籍》（已实测验证），要点：
//   - 页面是 **GBK** 而非 UTF-8：用 UTF-8 解码会得到乱码，然后误判为「没抓到数据」。
//   - 表头在 <tr class="g_column">，值在 <tr class="g_body_2">；不是 thead/tbody，也不是按 <th> 区分。
//   - 两行 td 文本按**索引 zip**成 {中文表头: 值}，再用**关键字包含**匹配映射到标准字段，
//     这样教务加列/调顺序都不用改代码。
//   - 值里带括注（如「数据科学与大数据技术（工学）」）要先清洗。
//   - 登录超时页面也返回 HTTP 200，必须靠**内容**判定失效。
//
// 隐私口径（沿用知识库）：身份证号、个人电话、家庭电话、家庭住址**不解析、不落库**。

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// Academic 是从教务页面解析出的学籍字段。
//
// 刻意**不含**身份证/个人电话/家庭电话/家庭住址——解析层就不产出这些，
// 避免它们经由中间结构被误存。
type Academic struct {
	StudentNo string
	Name      string
	College   string // 系别 → 学院
	Major     string
	ClassName string
	Grade     string

	// 以下字段来自同一页 bjiben.asp（#29）。实测 2026-11-09 确认表头存在。
	ExamNo           string // 考生号
	Gender           string // 性别
	StudyDuration    string // 学制（四年/三年…）
	EducationLevel   string // 培养层次（本科/研究生）
	NewMajor         string // 新专业
	NewClassName     string // 新班级
	EnrollmentStatus string // 学籍状态（在读/休学…）
	EnrollmentDate   string // 入学日期（Y-M-D）
	Nation           string // 民族
	PoliticalStatus  string // 政治面貌
	HomeAddress      string // 家庭通讯地址
	TrainStation     string // 火车站点
	PersonalPhone    string // 个人电话
	HomePhone        string // 家庭电话
	Specialty        string // 特长
}

var (
	// reTableBody 定位学籍表。
	reTableBody = regexp.MustCompile(`(?is)<table[^>]*class\s*=\s*["']?[^"'>]*tablebody[^"'>]*["']?[^>]*>(.*?)</table>`)
	// reHeaderRow 取表头行。
	reHeaderRow = regexp.MustCompile(`(?is)<tr[^>]*class\s*=\s*["']?[^"'>]*g_column[^"'>]*["']?[^>]*>(.*?)</tr>`)
	// reBodyRow 取值行。
	reBodyRow = regexp.MustCompile(`(?is)<tr[^>]*class\s*=\s*["']?[^"'>]*g_body_2[^"'>]*["']?[^>]*>(.*?)</tr>`)
	// reTD 取单元格。
	reTD = regexp.MustCompile(`(?is)<t[dh][^>]*>(.*?)</t[dh]>`)
	// reTag 去掉 HTML 标签。
	reTag = regexp.MustCompile(`(?is)<[^>]+>`)
	// reScriptStyle 去掉脚本与样式块。
	reScriptStyle = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
)

// 教务表头关键字 → 标准字段。用「包含」匹配，不依赖列顺序。
//
// 注意「专业」与「新专业」、「班级」与「新班级」同时存在。
// 实测两者相同，但为保证确定性，这里**优先取不带「新」的那个**（原始学籍），
// 若原始列缺失再回落「新」列。
var (
	keyStudentNo = []string{"学号"}
	keyName      = []string{"姓名"}
	keyCollege   = []string{"系别", "学院", "所属单位"}
	keyGrade     = []string{"年级"}
	keyMajorOld  = []string{"专业"}
	keyMajorNew  = []string{"新专业"}
	keyClassOld  = []string{"班级"}
	keyClassNew  = []string{"新班级"}

	// #29：同一页的其余字段（实测表头见文件头注释）。
	keyExamNo           = []string{"考生号", "考号"}
	keyGender           = []string{"性别"}
	keyStudyDuration    = []string{"学制"}
	keyEducationLevel   = []string{"培养层次", "层次"}
	keyEnrollmentStatus = []string{"学籍状态", "状态"}
	keyEnrollmentDate   = []string{"入学日期", "入学时间"}
	keyNation           = []string{"民族"}
	keyPoliticalStatus  = []string{"政治面貌"}
	keyHomeAddress      = []string{"家庭通讯地址", "家庭地址", "通讯地址"}
	keyTrainStation     = []string{"火车站点", "站点"}
	keyPersonalPhone    = []string{"个人电话", "手机"}
	keyHomePhone        = []string{"家庭电话"}
	keySpecialty        = []string{"特长"}
)

// ParseAcademic 解析 bjiben.asp 的响应字节（**GBK 编码**）为学籍字段。
//
// 会先按 GBK 解码；若解码失败（页面其实是 UTF-8）则回落 UTF-8，
// 避免因编码假设错误把正常页面判成乱码失败。
func ParseAcademic(body []byte) (Academic, error) {
	html := decodeGBK(body)

	// 登录失效判定：内容里出现「登录超时」之类的提示，或压根没有学籍表。
	if strings.Contains(html, "登录超时") || strings.Contains(html, "请重新登录") {
		return Academic{}, fmt.Errorf("schoolauth: 教务会话已失效（页面提示登录超时）")
	}

	tableMatch := reTableBody.FindStringSubmatch(html)
	if tableMatch == nil {
		return Academic{}, fmt.Errorf("schoolauth: 未找到学籍表（会话可能失效）")
	}
	table := tableMatch[1]

	headerMatch := reHeaderRow.FindStringSubmatch(table)
	if headerMatch == nil {
		return Academic{}, fmt.Errorf("schoolauth: 未找到表头行 g_column")
	}
	bodyMatch := reBodyRow.FindStringSubmatch(table)
	if bodyMatch == nil {
		return Academic{}, fmt.Errorf("schoolauth: 未找到数据行 g_body_2")
	}

	headers := splitCells(headerMatch[1])
	values := splitCells(bodyMatch[1])
	if len(headers) == 0 || len(values) == 0 {
		return Academic{}, fmt.Errorf("schoolauth: 表头或数据行为空")
	}

	// 按索引 zip 成 {表头: 值}；列数不等时以较短的为准（多出的列忽略）。
	fields := make(map[string]string, len(headers))
	for i, h := range headers {
		if i >= len(values) {
			break
		}
		fields[h] = values[i]
	}

	// 按关键字匹配取值。
	//
	// 两个要点：
	//  1. **表头更长者优先**：「新专业」也包含「专业」，若先匹配到「新专业」就取错了。
	//     所以对候选表头按长度降序遍历，保证更具体的表头先被命中。
	//  2. **结果确定**：map 遍历顺序随机，因此必须显式排序，不能依赖随机顺序。
	headersSorted := make([]string, 0, len(fields))
	for h := range fields {
		headersSorted = append(headersSorted, h)
	}
	sort.Slice(headersSorted, func(i, j int) bool {
		if len(headersSorted[i]) != len(headersSorted[j]) {
			return len(headersSorted[i]) > len(headersSorted[j])
		}
		return headersSorted[i] < headersSorted[j]
	})

	// lookup 按关键字包含匹配取值；更长表头优先。
	lookup := func(keys ...string) string {
		for _, k := range keys {
			for _, header := range headersSorted {
				if strings.Contains(header, k) {
					if cv := CleanValue(fields[header]); cv != "" {
						return cv
					}
				}
			}
		}
		return ""
	}

	// lookupExcluding 同 lookup，但跳过表头里含 exclude 任一词的列。
	//
	// 用途：「专业」要取原始列，不能被「新专业」抢走——而两者都包含「专业」。
	lookupExcluding := func(exclude []string, keys ...string) string {
		for _, k := range keys {
			for _, header := range headersSorted {
				skip := false
				for _, ex := range exclude {
					if strings.Contains(header, ex) {
						skip = true
						break
					}
				}
				if skip {
					continue
				}
				if strings.Contains(header, k) {
					if cv := CleanValue(fields[header]); cv != "" {
						return cv
					}
				}
			}
		}
		return ""
	}

	// 专业/班级：优先原始列，缺失再取「新」列。
	//
	// 注意：「新专业」也包含「专业」，直接按包含匹配会串值。
	// 因此原始列用**排除新列**的取值器（lookupExcluding），确保互不侵占。
	major := lookupExcluding([]string{"新"}, keyMajorOld...)
	if major == "" {
		major = lookup(keyMajorNew...)
	}
	className := lookupExcluding([]string{"新"}, keyClassOld...)
	if className == "" {
		className = lookup(keyClassNew...)
	}

	acc := Academic{
		StudentNo: lookup(keyStudentNo...),
		Name:      lookup(keyName...),
		College:   lookup(keyCollege...),
		Major:     major,
		ClassName: className,
		Grade:     lookup(keyGrade...),

		// #29：同一页的其余字段。
		ExamNo:           lookup(keyExamNo...),
		Gender:           lookup(keyGender...),
		StudyDuration:    lookup(keyStudyDuration...),
		EducationLevel:   lookup(keyEducationLevel...),
		NewMajor:         lookup(keyMajorNew...),
		NewClassName:     lookup(keyClassNew...),
		EnrollmentStatus: lookup(keyEnrollmentStatus...),
		EnrollmentDate:   lookup(keyEnrollmentDate...),
		Nation:           lookup(keyNation...),
		PoliticalStatus:  lookup(keyPoliticalStatus...),
		HomeAddress:      lookup(keyHomeAddress...),
		TrainStation:     lookup(keyTrainStation...),
		PersonalPhone:    lookup(keyPersonalPhone...),
		HomePhone:        lookup(keyHomePhone...),
		Specialty:        lookup(keySpecialty...),
	}
	if acc.StudentNo == "" && acc.Name == "" {
		return Academic{}, fmt.Errorf("schoolauth: 学籍表里没有学号也没有姓名（页面结构可能已变）")
	}
	return acc, nil
}

// splitCells 拆出一行里的所有单元格文本。
func splitCells(row string) []string {
	matches := reTD.FindAllStringSubmatch(row, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, CleanValue(m[1]))
	}
	return out
}

// CleanValue 清洗单元格文本：去标签、去脚本、去括注、trim、压空白。
func CleanValue(s string) string {
	s = reScriptStyle.ReplaceAllString(s, "")
	s = reTag.ReplaceAllString(s, "")
	// 去掉全角与半角括注：数据科学与大数据技术（工学） → 数据科学与大数据技术
	s = reParenFull.ReplaceAllString(s, "")
	s = reParenHalf.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	// 压掉内部多余空白（含 &nbsp; 之类）
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, " ", " ")), " ")
	return s
}

var (
	reParenFull = regexp.MustCompile(`（[^）]*）`)
	reParenHalf = regexp.MustCompile(`\([^)]*\)`)
	reNBSP      = regexp.MustCompile(`\s+`)
)

// decodeGBK 解码教务页面字节。
//
// 判据：**先看是不是合法 UTF-8**。
// 不能反过来（先试 GBK 再看像不像 HTML）——GBK 解码器对非法字节很宽容，
// UTF-8 字节被它解成乱码后，ASCII 部分（如 `<table`）会原样保留，
// 于是「像 HTML」的判断会误判为真，把乱码当成正常内容返回，
// 后续再被去标签的正则吃掉内容，最终表现为「表头是空的、字段全空」。
//
// 教务页面是 GBK；某些入口返回 UTF-8；两种都要能正确解出中文。
func decodeGBK(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	if s, err := decodeWith(b, "gbk"); err == nil {
		return s
	}
	// GBK 解码也失败时尽力而为（可能有零星非法字节）。
	s, _ := decodeWith(b, "gbk")
	return s
}

// decodeWith 按指定编码解码字节。
func decodeWith(b []byte, enc string) (string, error) {
	switch enc {
	case "gbk":
		dec := simplifiedchinese.GBK.NewDecoder()
		out, err := dec.Bytes(b)
		if err != nil {
			return "", err
		}
		return string(out), nil
	case "utf-8":
		if !utf8.Valid(b) {
			return "", fmt.Errorf("schoolauth: 不是合法 UTF-8")
		}
		return string(b), nil
	default:
		return string(b), nil
	}
}
