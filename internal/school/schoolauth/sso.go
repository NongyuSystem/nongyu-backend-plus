package schoolauth

// 本文件实现「一次登录覆盖多站点」的唯一解（2026-11-10 实测，见
// docs/tech/实测-川农一次登录覆盖多站点的唯一解.md）。
//
// 结论：**不需要逐站登录**。一张 go-admin-tgt 经统一认证即可覆盖所有
// 已注册站点，浏览器会自动完成 SSO；少数站点在此之上额外要一个自己的 token。
//
// 三类站点，三种取数方式（2026-11-10 逐站实测）：
//
//	① CAS 换票型（教务 jiaowu）
//	   GET auth/v1/cas/login?appid=<UUID> + Cookie: go-admin-tgt
//	   → 加密 data 解出 {st, tgt, user, callbackUrl}，拿 st 回投建会话取页面。
//	   随机 UUID 返回 code=500「未登录」——appid 必须已注册。
//
//	② 自有 Bearer 型（ywtb 一网通办）
//	   它完全不认 go-admin-tgt（401），需要自己的 Bearer "405500|xxx"。
//	   这是**身份证号的唯一来源**（教务档案页没有身份证）。
//
//	③ SSO 网关型（学工系统 xsgz）
//	   只靠 go-admin-tgt 跳 auth/sso/login?service=<站点>/sso 即自动登录，
//	   网关下发 main-app-access-token；后续请求带 H-Zove-Token 等头取数。
//	   它提供教务/ywtb 都没有的字段：宿舍地址、班主任、主责单位。
//
// 不属于统一认证范围：图书馆 OPAC（独立账号体系）、
// xsc/zjc/cwc/lib/ecology（资讯门户，无个人数据）、webvpn（VPN 网关）。
// 详见 SiteProfiles。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"nongyu-backend-plus/internal/platform"
)

// 已知站点在统一身份认证平台注册的 appid（UUID 形式）。
//
// 用名字去打 cas/login 只有 ywtb 会碰巧成功（它恰好被注册成那个名字），
// 其余必须用 UUID。新增站点时在此登记，未登记的站点请传 UUID。
var SiteAppID = map[string]string{
	"jiaowu": "b54a2f27-f7ba-4b91-bbf0-3b2b12244d14", // 教务
	"webvpn": "aea49a21-6b26-4e8f-bcfc-80e3de591c93", // 门户 / WebVPN
	"ywtb":   "ywtb",                                 // 特例：该平台允许用名字
}

// SiteKind 说明一个站点「能不能拿个人数据」，这决定了统一认证对它的价值。
//
// 2026-11-10 逐站实测结论（详见 docs/tech/实测-川农一次登录覆盖多站点的唯一解.md）：
// 用户给的 8 个站里，只有 3 个真能取个人数据；其余是资讯门户或独立账号体系。
// 「认证层全通」不等于「业务数据可取」——这个区分必须留在代码里，
// 否则后人会以为「8 个站都能抓」，实际抓不到。
type SiteKind string

const (
	// SitePersonal 有个人数据，且走统一认证：换票即可取数。
	SitePersonal SiteKind = "personal"
	// SitePortal 纯资讯门户：没有个人数据，认证与否都一样。
	SitePortal SiteKind = "portal"
	// SiteOwnAccount 有自己的账号体系（不认统一认证）：必须另行登录。
	SiteOwnAccount SiteKind = "own_account"
	// SiteGateway VPN 网关：它不提供数据，只用来访问内网站点。
	SiteGateway SiteKind = "gateway"
)

// SiteProfile 描述一个站点的性质与取数方式。
type SiteProfile struct {
	Kind SiteKind
	// Host 是站点主机名（便于日志与排查）。
	Host string
	// Note 说明「为什么是这个类别」，写给后来人看。
	Note string
}

// SiteProfiles 是逐站实测的站点目录。
//
// 用户原始清单的 8 个站全部收录；另加 opac（图书馆书目系统，因为它才是
// lib 站点里真正有个人数据的地方，且它不认统一认证）。
var SiteProfiles = map[string]SiteProfile{
	"jiaowu": {
		Kind: SitePersonal, Host: "jiaowu.sicau.edu.cn",
		Note: "教务：换票后三跳建会话可取 bjiben.asp（21 个学籍字段）",
	},
	"ywtb": {
		Kind: SitePersonal, Host: "ywtb.sicau.edu.cn",
		Note: "一网通办：需它自己的 Bearer，是身份证号的唯一来源",
	},
	"webvpn": {
		Kind: SiteGateway, Host: "webvpn.sicau.edu.cn",
		Note: "VPN 网关：用于访问内网站点，本身不提供个人数据",
	},
	"xsc": {
		Kind: SitePortal, Host: "xsc.sicau.edu.cn",
		Note: "党委学工部：纯资讯门户（部门概况/党建/学风/资助），无个人数据接口",
	},
	"zjc": {
		Kind: SitePortal, Host: "zjc.sicau.edu.cn",
		Note: "招生就业处：资讯门户；其下另有独立的招生/就业信息网",
	},
	"cwc": {
		Kind: SitePortal, Host: "cwc.sicau.edu.cn",
		Note: "财务处：首页提示「没有访问当前栏目的权限」，需额外授权，非统一认证范围",
	},
	"lib": {
		Kind: SitePortal, Host: "lib.sicau.edu.cn",
		Note: "图书馆门户：资讯为主；真正有个人数据的是其 opac 子系统",
	},
	"xsgz": {
		Kind: SitePersonal, Host: "xsgz.sicau.edu.cn",
		Note: "学工系统：SSO 后拿 main-app-access-token，GET /gateway/zovecenter-saip-xg-app-student/student/detail/<学号> 一次返回 13 组档案（含身份证/家庭/简历）",
	},
	"opac": {
		Kind: SiteOwnAccount, Host: "opac.sicau.edu.cn",
		Note: "图书馆书目系统：独立账号体系（username/password 表单），页内无 CAS/SSO 入口",
	},
	"ecology": {
		Kind: SitePortal, Host: "ecology.sicau.edu.cn",
		Note: "生态/学工一体化：可达（HTTPS 200），未发现统一认证取数入口",
	},
}

const (
	// ywtbHost 是 ywtb 一网通办站点。
	ywtbHost = "ywtb.sicau.edu.cn"
	// maxYWTBBody 是 ywtb 响应体上限：正常只有几百字节。
	maxYWTBBody = 1 << 20
)

// CASUser 是换票响应里携带的用户信息。
type CASUser struct {
	ID     int64  `json:"id"`
	Number string `json:"number"` // 学号
	Name   string `json:"name"`   // 姓名
	Phone  string `json:"phone"`
	Group  []struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Alias string `json:"alias"` // 「本科生」等
	} `json:"group"`
}

// CASResult 是一次换票的结果。
type CASResult struct {
	ST          string  // 给目标站建会话用的 service ticket
	TGT         string  // 响应里带回的新 tgt（可用于滚动续期）
	CallbackURL string  // 目标站的回调地址（主机名即目标站）
	User        CASUser // 身份（换票顺带给出，不必再取一次）
}

// ExchangeCAS 用 go-admin-tgt 为指定 appid 换票。
//
// appid 可以是 SiteAppID 里的站点名，也可以是直接给出的 UUID。
// cookieHeader 为整套同域 cookie；只给 tgt 时退化为只带 tgt（实测可行）。
func ExchangeCAS(ctx context.Context, doer platform.HTTPDoer, authBase, appid, tgt, cookieHeader string) (CASResult, error) {
	if doer == nil {
		return CASResult{}, fmt.Errorf("schoolauth: ExchangeCAS 需要 HTTPDoer")
	}
	if strings.TrimSpace(tgt) == "" {
		return CASResult{}, fmt.Errorf("schoolauth: 缺少 %s", goAdminTgtCookie)
	}
	if authBase == "" {
		authBase = "https://" + authHost
	}
	// 站点名 → UUID（未登记时按原样当作 UUID）
	if id, ok := SiteAppID[appid]; ok {
		appid = id
	}

	u := strings.TrimRight(authBase, "/") + "/api/v1/cas/login?appid=" + appid
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return CASResult{}, err
	}
	req.Header.Set("accept", "*/*")
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120")
	if strings.TrimSpace(cookieHeader) != "" {
		req.Header.Set("Cookie", cookieHeader)
	} else {
		req.Header.Set("Cookie", goAdminTgtCookie+"="+tgt)
	}

	resp, err := doer.Do(ctx, req)
	if err != nil {
		return CASResult{}, fmt.Errorf("%w：换票请求失败：%v", ErrTicketInfra, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxJiaowuBody))
	if err != nil {
		return CASResult{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return CASResult{}, fmt.Errorf("%w：换票返回 %d", ErrTicketInfra, resp.StatusCode)
	}

	var env ywtbEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return CASResult{}, fmt.Errorf("%w：换票响应解析失败：%v", ErrTicketInfra, err)
	}
	if env.Code != 200 {
		// 这是「学校侧明确拒绝」——tgt 无效或该 appid 未注册。
		return CASResult{}, fmt.Errorf("%w：换票业务码 %d（%s）", ErrTicketRejected, env.Code, env.Msg)
	}
	plain, err := DecryptYWTB(env.RequestID, env.Data)
	if err != nil {
		return CASResult{}, fmt.Errorf("%w：换票响应解密失败：%v", ErrTicketInfra, err)
	}

	var t struct {
		ST          string  `json:"st"`
		TGT         string  `json:"tgt"`
		CallbackURL string  `json:"callbackUrl"`
		User        CASUser `json:"user"`
	}
	if err := json.Unmarshal(plain, &t); err != nil {
		return CASResult{}, fmt.Errorf("%w：票据解析失败：%v", ErrTicketInfra, err)
	}
	if t.ST == "" {
		return CASResult{}, fmt.Errorf("%w：换票未返回 service ticket", ErrTicketInfra)
	}
	return CASResult{ST: t.ST, TGT: t.TGT, CallbackURL: t.CallbackURL, User: t.User}, nil
}

// YWTBIdentity 是 ywtb getUserInfo 解密后的完整身份。
//
// 这是**身份证号的唯一来源**；教务档案页没有身份证。
type YWTBIdentity struct {
	StudentNo string   `json:"number"`
	Name      string   `json:"name"`
	Gender    string   `json:"gender"`
	IDCard    string   `json:"idcard"` // 完整号
	Phone     string   `json:"phone"`
	College   string   `json:"unit"` // 所属单位 = 学院
	Birthday  string   `json:"birthday"`
	Email     string   `json:"email"`
	QQ        string   `json:"qq"`
	Wechat    string   `json:"wechat"`
	Avatar    string   `json:"avatar"`
	Groups    []string `json:"groups"`
	PassLevel int      `json:"pass_level"`
	Status    int      `json:"status"`
	ExpireAt  string   `json:"expire_at"`
}

// ywtbGetUserInfoPath 是取完整身份的接口路径。
//
// 坑：ywtb 前端 axios 的 baseURL 是 "/api"，所以完整路径是 /api 加上代码里的
// /m/v1/user/getUserInfo。直接打 /api/m/user/getUserInfo 会 404。
const ywtbGetUserInfoPath = "/api/m/v1/user/getUserInfo"

// FetchYWTBIdentity 用 ywtb 自己的 Bearer token 取完整身份（含身份证）。
//
// 注意：**ywtb 不认 go-admin-tgt**，带 tgt 会 401。这里的 bearer 必须是
// ywtb 自己的 token（形如 "405500|xxx"），浏览器里存于 localStorage 的
// lifeData.data.vuex_token。
func FetchYWTBIdentity(ctx context.Context, doer platform.HTTPDoer, baseURL, bearer string) (YWTBIdentity, error) {
	if doer == nil {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: FetchYWTBIdentity 需要 HTTPDoer")
	}
	if strings.TrimSpace(bearer) == "" {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: 缺少 ywtb Bearer token（它不是 go-admin-tgt）")
	}
	if baseURL == "" {
		baseURL = "https://" + ywtbHost
	}

	u := strings.TrimRight(baseURL, "/") + ywtbGetUserInfoPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader("{}"))
	if err != nil {
		return YWTBIdentity{}, err
	}
	req.Header.Set("accept", "*/*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearer))
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120")

	resp, err := doer.Do(ctx, req)
	if err != nil {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: ywtb 取身份请求失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxYWTBBody))
	if err != nil {
		return YWTBIdentity{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: ywtb 返回 %d（Bearer 可能已失效）", resp.StatusCode)
	}

	var env ywtbEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: ywtb 响应解析失败：%w", err)
	}
	if env.Code != 200 {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: ywtb 业务码 %d（%s）", env.Code, env.Msg)
	}
	plain, err := DecryptYWTB(env.RequestID, env.Data)
	if err != nil {
		return YWTBIdentity{}, err
	}
	var info YWTBIdentity
	if err := json.Unmarshal(plain, &info); err != nil {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: ywtb 明文解析失败：%w", err)
	}
	if strings.TrimSpace(info.StudentNo) == "" {
		return YWTBIdentity{}, fmt.Errorf("schoolauth: ywtb 明文缺少学号")
	}
	return info, nil
}

// ── ③ xsgz 学工系统（SSO 网关型）───────────────────────────────────

// XSGZStudentDetail 是学工系统的完整学生档案（2026-11-10 实测）。
//
// 端点：GET /gateway/zovecenter-saip-xg-app-student/student/detail/<学号>
// 一次返回 13 个分组，覆盖用户清单里几乎所有字段——包括教务档案页和
// ywtb 都给不了的身份证号、家庭住址、考生号、紧急联系人、教育经历。
//
// 设计：**每个分组同时保留原始 JSON**（Groups map），上游新增字段不会丢；
// 常用字段通过 TypedXxx 结构提取。这样既不会因上游加字段而崩，也不用把
// 上百个字段全定义成 Go 结构体。
type XSGZStudentDetail struct {
	// Groups 保留 13 个分组的完整原文，key 形如 "baseDto" / "examDto"。
	Groups map[string]json.RawMessage
	// Typed 是把常用字段解出来的便利视图（供上层直接读）。
	Typed XSGZTyped
}

// XSGZTyped 是从各分组提取出的常用字段（用户清单里的字段优先）。
type XSGZTyped struct {
	StudentNo          string
	OneCardNo          string
	StudentName        string
	Sex                string
	SexName            string
	Nationality        string
	IDCardNo           string // 身份证号（完整，上层立即加密）
	Birthday           string
	CollegeName        string
	SpecialtyName      string
	Grade              string
	ClassName          string
	CampusName         string
	DormAddress        string
	StudyYears         string
	InSchoolDate       string
	ExamineeNo         string
	Email              string
	MobilePhone        string
	Wechat             string
	QQ                 string
	FamilyAddress      string
	FamilyTelephone    string
	EmergencyContact   string
	EmergencyTelephone string
	NamePinyin         string
	Height             string
	Weight             string
	Age                int
}

// XSGZGroupKeys 是实测的 13 个分组名（顺序固定，便于遍历）。
var XSGZGroupKeys = []string{
	"baseDto", "studentStatusDto", "personalInfoDto", "contactInfoDto",
	"emergencyContactInfoDto", "examDto", "familyDto", "economyDto",
	"workUnitDto", "familyMemberList", "sociologyMemberList", "resumeList", "age",
}

// strField 从分组原文里安全取字符串字段：字段不存在、null、或为 number 时
// 都能取到字符串表示（number 用其字面量），绝不因类型不匹配报错。
func strField(g json.RawMessage, key string) string {
	if len(g) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(g, &m) != nil {
		return ""
	}
	raw, ok := m[key]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// 字符串直接去引号；其它类型（number/bool）用原始字面量
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return string(raw)
}

// parseXSGZDetail 把网关 data 解成 XSGZStudentDetail（分组原文 + 常用字段视图）。
func parseXSGZDetail(data json.RawMessage) (XSGZStudentDetail, error) {
	var groups map[string]json.RawMessage
	if err := json.Unmarshal(data, &groups); err != nil {
		return XSGZStudentDetail{}, fmt.Errorf("解析 data 分组失败：%w", err)
	}
	d := XSGZStudentDetail{Groups: groups}
	g := func(k string) json.RawMessage { return groups[k] }

	base, status, personal := g("baseDto"), g("studentStatusDto"), g("personalInfoDto")
	contact, emergency, exam := g("contactInfoDto"), g("emergencyContactInfoDto"), g("examDto")
	family := g("familyDto")

	t := XSGZTyped{
		StudentNo:          strField(base, "studentNo"),
		OneCardNo:          strField(base, "oneCardNo"),
		StudentName:        strField(base, "studentName"),
		Sex:                strField(base, "sex"),
		SexName:            strField(base, "sexName"),
		Nationality:        strField(base, "nationality"),
		IDCardNo:           strField(base, "idCardNo"),
		Birthday:           strField(base, "birthday"),
		CollegeName:        strField(status, "collegeName"),
		SpecialtyName:      strField(status, "specialtyName"),
		Grade:              strField(status, "grade"),
		ClassName:          strField(status, "className"),
		CampusName:         strField(status, "campusName"),
		DormAddress:        strField(status, "dormAddress"),
		StudyYears:         strField(status, "studyYears"),
		InSchoolDate:       strField(status, "inSchoolDate"),
		ExamineeNo:         strField(exam, "examineeNo"),
		Email:              strField(contact, "email"),
		MobilePhone:        strField(contact, "mobilePhone"),
		Wechat:             strField(contact, "wechat"),
		QQ:                 strField(contact, "qq"),
		FamilyAddress:      strField(family, "familyAddress"),
		FamilyTelephone:    strField(family, "familyTelephone"),
		EmergencyContact:   strField(emergency, "emergencyContact"),
		EmergencyTelephone: strField(emergency, "emergencyContactTelephone"),
		NamePinyin:         strField(personal, "namePinyin"),
		Height:             strField(personal, "height"),
		Weight:             strField(personal, "weight"),
	}
	if a := strField(groups["age"], ""); a == "" {
		var age int
		if json.Unmarshal(groups["age"], &age) == nil {
			t.Age = age
		}
	}
	d.Typed = t
	if t.StudentNo == "" {
		return XSGZStudentDetail{}, fmt.Errorf("响应缺少学号（baseDto.studentNo）")
	}
	return d, nil
}

const (
	xsgzHost = "xsgz.sicau.edu.cn"
	// xsgzDetailGateway 是「学生详情」端点所在的子应用网关。
	xsgzDetailGateway = "/gateway/zovecenter-saip-xg-app-student"
	xsgzAppKey        = "zovecenter-saip-xg-app-student"
	xsgzTenant        = "XG"
)

// xsgzHeaders 构造学工网关要求的请求头（2026-11-10 用浏览器完整 curl 实测确认）。
//
// 三处极易写错、写错就 403「API 接口资源未授权」或 500「验证权限异常」：
//   - H-Zove-Source 必须是 "20000"（子应用），不是主应用的 "20100"
//   - H-Zove-AppKey 必须是 "zovecenter-saip-xg-app-student"（子应用），不是 agg-pc
//   - H-Zove-UserLevel 必须带（学生为 "XS"），漏了就 403
func xsgzHeaders(token, userLevel string) map[string]string {
	if strings.TrimSpace(userLevel) == "" {
		userLevel = "XS"
	}
	return map[string]string{
		"H-Zove-Token":     strings.TrimSpace(token),
		"H-Zove-Source":    "20000",
		"H-Zove-TenantId":  xsgzTenant,
		"H-Zove-AppKey":    xsgzAppKey,
		"H-Zove-UserLevel": userLevel,
	}
}

// FetchXSGZStudentDetail 取学工系统的完整学生档案（一次请求多字段）。
//
// token 来自客户端完成统一登录后交来的 main-app-access-token（与 ywtb 交
// Bearer 是同一模式）；后端拿到后即可独立取数，不需要浏览器参与。
// studentNo 是学号，作为路径参数。
func FetchXSGZStudentDetail(ctx context.Context, doer platform.HTTPDoer, baseURL, token, studentNo string) (XSGZStudentDetail, error) {
	if doer == nil {
		return XSGZStudentDetail{}, fmt.Errorf("schoolauth: FetchXSGZStudentDetail 需要 HTTPDoer")
	}
	if strings.TrimSpace(token) == "" {
		return XSGZStudentDetail{}, fmt.Errorf("schoolauth: 缺少 xsgz access token（main-app-access-token）")
	}
	if strings.TrimSpace(studentNo) == "" {
		return XSGZStudentDetail{}, fmt.Errorf("schoolauth: 缺少学号")
	}
	// 学号只允许数字，防止路径注入（这是拼进 URL 的唯一外部输入）。
	for _, r := range studentNo {
		if r < '0' || r > '9' {
			return XSGZStudentDetail{}, fmt.Errorf("schoolauth: 学号只能包含数字")
		}
	}
	if baseURL == "" {
		baseURL = "https://" + xsgzHost
	}

	u := strings.TrimRight(baseURL, "/") + xsgzDetailGateway + "/student/detail/" + studentNo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return XSGZStudentDetail{}, err
	}
	req.Header.Set("accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120")
	req.Header.Set("Referer", strings.TrimRight(baseURL, "/")+"/student/personal/info")
	for k, v := range xsgzHeaders(token, "XS") {
		req.Header.Set(k, v)
	}

	resp, err := doer.Do(ctx, req)
	if err != nil {
		return XSGZStudentDetail{}, fmt.Errorf("schoolauth: xsgz 取档案请求失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return XSGZStudentDetail{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return XSGZStudentDetail{}, fmt.Errorf("schoolauth: xsgz 返回 %d（token 可能已失效或权限不足）", resp.StatusCode)
	}

	var env struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return XSGZStudentDetail{}, fmt.Errorf("schoolauth: xsgz 响应解析失败：%w", err)
	}
	if env.Code != "0" {
		return XSGZStudentDetail{}, fmt.Errorf("schoolauth: xsgz 业务码 %s（%s）", env.Code, env.Message)
	}
	return parseXSGZDetail(env.Data)
}

// ParseXSGZDetail 解析 detail 端点的 data 段（供落库/测试复用）。
func ParseXSGZDetail(data json.RawMessage) (XSGZStudentDetail, error) {
	return parseXSGZDetail(data)
}

// ── XSGZSource：唯一取身份源（学工系统）─────────────────────────────
//
// 为什么是它（2026-11-10 实测）：
//
//	xsgz 的 detail 端点一次返回 13 个分组，覆盖用户清单里几乎所有字段
//	（身份证号、家庭住址、考生号、紧急联系人、教育经历、家庭成员、经济情况…），
//	而教务 bjiben.asp 与 ywtb getUserInfo 都给不了这些。
//	因此**取数统一走 xsgz**；jiaowu / ywtb 的取数逻辑已 disable（见 app 装配层）。
//
// 身份确认仍用统一认证换票（拿学号），这是「一次登录」的落点：
// 客户端只做一次统一认证，之后所有取数都是后端的事。
type XSGZSource struct {
	doer      platform.HTTPDoer
	exchanger TicketExchanger
	// token 是客户端交来的 main-app-access-token。
	token string
}

// XSGZOptions 是 XSGZSource 的构造参数。
type XSGZOptions struct {
	Doer      platform.HTTPDoer
	Exchanger TicketExchanger
	// Token 是学工系统的 access token（客户端完成统一登录后交来）。
	Token string
}

// NewXSGZSource 构造学工取身份源。
func NewXSGZSource(o XSGZOptions) (*XSGZSource, error) {
	if o.Doer == nil {
		return nil, fmt.Errorf("schoolauth: XSGZSource 需要 HTTPDoer")
	}
	if o.Exchanger == nil {
		return nil, fmt.Errorf("schoolauth: XSGZSource 需要 TicketExchanger（用 tgt 换票确认学号）")
	}
	return &XSGZSource{doer: o.Doer, exchanger: o.Exchanger, token: strings.TrimSpace(o.Token)}, nil
}

// WithToken 设置学工系统的 access token。
func (s *XSGZSource) WithToken(t string) *XSGZSource {
	s.token = strings.TrimSpace(t)
	return s
}

// FetchIdentity 实现 AuthSource：换票确认身份 + 取完整档案。
func (s *XSGZSource) FetchIdentity(ctx context.Context, cred Credential) (RawIdentity, error) {
	// ① 换票：确认「这个人是谁」（学号）。tgt 已在客户端完成统一登录。
	tgt := cookieValue(cred, goAdminTgtCookie)
	if strings.TrimSpace(tgt) == "" {
		return RawIdentity{}, fmt.Errorf("schoolauth: 缺少 %s", goAdminTgtCookie)
	}
	ti, err := s.exchanger.ExchangeForIdentity(ctx, cred)
	if err != nil {
		return RawIdentity{}, err
	}
	if strings.TrimSpace(ti.StudentNo) == "" {
		return RawIdentity{}, fmt.Errorf("schoolauth: 换票响应未返回学号")
	}

	// ② 取完整档案（唯一数据源）。
	token := cred.XSGZToken
	if strings.TrimSpace(token) == "" {
		token = s.token
	}
	detail, err := FetchXSGZStudentDetail(ctx, s.doer, "", token, ti.StudentNo)
	if err != nil {
		// 档案取不到时**不阻断登录**：身份已由换票确认。
		// 换票响应里没有身份证（身份证只在校工 detail 里）。
		return RawIdentity{
			StudentNo: ti.StudentNo,
			Name:      ti.Name,
		}, nil
	}

	raw := RawIdentity{
		StudentNo:    detail.Typed.StudentNo,
		Name:         detail.Typed.StudentName,
		College:      detail.Typed.CollegeName,
		IDCardMasked: detail.Typed.IDCardNo,
		IDCardFull:   detail.Typed.IDCardNo,
	}
	// 专业/班级/年级/学号等也从这里来（不再是教务 GBK 页面）。
	raw.Major = detail.Typed.SpecialtyName
	raw.ClassName = detail.Typed.ClassName
	raw.Grade = detail.Typed.Grade
	return raw, nil
}

// FetchDetail 取完整档案（供上层落库用；AuthSource 拿不到 detail 原文）。
func (s *XSGZSource) FetchDetail(ctx context.Context, cred Credential) (XSGZStudentDetail, error) {
	tgt := cookieValue(cred, goAdminTgtCookie)
	if strings.TrimSpace(tgt) == "" {
		return XSGZStudentDetail{}, fmt.Errorf("schoolauth: 缺少 %s", goAdminTgtCookie)
	}
	ti, err := s.exchanger.ExchangeForIdentity(ctx, cred)
	if err != nil {
		return XSGZStudentDetail{}, err
	}
	token := cred.XSGZToken
	if strings.TrimSpace(token) == "" {
		token = s.token
	}
	return FetchXSGZStudentDetail(ctx, s.doer, "", token, ti.StudentNo)
}
