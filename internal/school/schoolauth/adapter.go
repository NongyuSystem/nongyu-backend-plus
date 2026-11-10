package schoolauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ---- from adapter.go ----
// ErrAuthFailed 表示学校认证被拒（凭据无效或已过期）。
var ErrAuthFailed = errors.New("schoolauth: 学校认证失败")

// SessionCookie 是客户端交上来的一条会话 cookie。
//
// 实测（2026-11-09）：**单枚 `go-admin-tgt` 就足以换票**（code=200）。
// 早前「必须整套 cookie」的结论对当前平台已不成立；整套 cookie 仍可原样透传，
// 但不再是前置条件。
type SessionCookie struct {
	Name  string
	Value string
}

// Credential 是客户端交上来的学校会话凭据。
//
// 客户端先用自己那套方式完成学校登录（账密/微信/短信，都在客户端侧），
// 然后把拿到的 **`go-admin-tgt`** 交上来；本适配器用它换票取身份，
// **不接触学生密码**。
//
// 只有这一种凭据形态。曾有一个 ywtb `BearerToken` 字段，已按维护者指示删除：
// 真实链路上客户端不会有它，两条路径并存只会让「该交什么」含糊不清。
type Credential struct {
	// Cookies 是整套会话 cookie（可只含 go-admin-tgt）。
	Cookies []SessionCookie
	// RawCookie 是原始 Cookie 头（如 "a=1; b=2"）。给了 Cookies 时可省略。
	RawCookie string
	// UserID 是这份凭据归属的农屿用户（用于按用户保管 tgt）。
	// 零值表示「还不知道是谁」——此时无法使用服务端保管的 tgt，
	// 只能用本次交来的凭据做一次性取数。
	UserID int64
}

// String 实现 fmt.Stringer，**故意不输出 cookie 值**，防止被日志打印出去。
func (c Credential) String() string {
	names := make([]string, 0, len(c.Cookies))
	for _, ck := range c.Cookies {
		names = append(names, ck.Name)
	}
	return fmt.Sprintf("Credential{cookies=%d, names=[%s]}", len(c.Cookies), strings.Join(names, ","))
}

// CookieHeader 把凭据还原成 Cookie 请求头的值。
func (c Credential) CookieHeader() string {
	if c.RawCookie != "" {
		return c.RawCookie
	}
	parts := make([]string, 0, len(c.Cookies))
	for _, ck := range c.Cookies {
		parts = append(parts, ck.Name+"="+ck.Value)
	}
	return strings.Join(parts, "; ")
}

// FullCookieHeader 返回**整套** cookie 头，并确保包含 go-admin-tgt。
//
// 为什么需要它：实测换票必须带整套同域 cookie，只带 go-admin-tgt 会报「未登录」。
// 因此换票/续期都应当用它，而不是自己拼一个只有 tgt 的头。
func (c Credential) FullCookieHeader(tgt string) string {
	base := strings.TrimSpace(c.CookieHeader())
	if strings.TrimSpace(tgt) == "" {
		return base
	}
	if cookieValue(c, goAdminTgtCookie) != "" {
		// 已有 tgt（可能是新的），保持其它 cookie 不变，替换 tgt 值。
		return replaceCookieValue(base, goAdminTgtCookie, tgt)
	}
	if base == "" {
		return goAdminTgtCookie + "=" + tgt
	}
	return base + "; " + goAdminTgtCookie + "=" + tgt
}

// replaceCookieValue 在一个 Cookie 头里替换指定 cookie 的值；不存在则追加。
func replaceCookieValue(header, name, value string) string {
	parts := strings.Split(header, ";")
	found := false
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		kv := strings.SplitN(p, "=", 2)
		if len(kv) == 2 && strings.TrimSpace(kv[0]) == name {
			parts[i] = " " + name + "=" + value
			found = true
			break
		}
	}
	out := strings.TrimSpace(strings.Join(parts, ";"))
	if !found {
		if out == "" {
			return name + "=" + value
		}
		return out + "; " + name + "=" + value
	}
	return out
}

// Empty 判断凭据是否为空。
// Empty 判断是否「没有任何可用凭据」。
//
// 判据就是 cookie：真实链路上客户端登录后交来的只有 go-admin-tgt。
func (c Credential) Empty() bool {
	return strings.TrimSpace(c.CookieHeader()) == ""
}

// Identity 是解析出来的学校身份。
type Identity struct {
	StudentNo string
	Name      string
	// College 取自认证结果里的「所属单位」。
	College string
	// IDCardSuffix4 从证件号取末四位（掩码或完整号均可）。
	IDCardSuffix4 string
	// IDCardFull 是完整身份证号；由上层立即加密，不在本层留存。
	// 空值表示上游只给了掩码（如某些入口）。
	IDCardFull string

	// 以下三项来自教务，抓取失败时为空。
	Major     string
	ClassName string
	Grade     string

	// ── 以下来自教务档案页 bjiben.asp（issue #29）──────────────────
	ExamNo           string
	Gender           string
	StudyDuration    string
	EducationLevel   string
	NewMajor         string
	NewClassName     string
	EnrollmentStatus string
	EnrollmentDate   string
	Nation           string
	PoliticalStatus  string
	HomeAddress      string
	TrainStation     string
	PersonalPhone    string
	HomePhone        string
	Specialty        string

	// ── 以下来自 ywtb getUserInfo（issue #29，需 ywtb 自己的 Bearer）──
	Birthday        string
	AvatarSourceURL string

	// ProfileIncomplete 为真表示教务源没抓到（登录仍成功）。
	ProfileIncomplete bool
}

// AuthSource 是「用会话凭据取身份」的外部能力，由 ywtb 源实现。
//
// 刻意接收完整的 Credential 而不是一个 cookie 字符串：
// 实测 ywtb 需要 **Bearer token + laravel_session cookie 两者同时**，
// 只传 cookie 会在真实环境 401。接口形状必须反映这个事实，
// 否则实现方会写出「只带 cookie」的代码而测试照样通过。
type AuthSource interface {
	// FetchIdentity 用客户端交上来的凭据取认证身份。
	FetchIdentity(ctx context.Context, cred Credential) (RawIdentity, error)
}

// AcademicSource 是从教务取学籍的能力，由 school/jiaowu 实现。
type AcademicSource interface {
	// FetchAcademic 用客户端凭据取专业/班级/年级（教务那一路是另一套会话）。
	FetchAcademic(ctx context.Context, cred Credential) (RawAcademic, error)
}

// RawIdentity 是认证源返回的原始字段（未做掩码解析）。
type RawIdentity struct {
	StudentNo string
	Name      string
	// College 对应页面上的「所属单位」。
	College string
	// IDCardMasked 是证件号原值：API 返回的是**完整号**（页面显示才是掩码）。
	// 字段名保留 Masked 是为了兼容既有测试；上层应按「可能是完整号」处理。
	IDCardMasked string
	// IDCardFull 是完整身份证号（与 IDCardMasked 同源；显式命名便于上层加密存储）。
	IDCardFull string
	// Birthday 是生日（issue #29）；ywtb 提供，用于推算年龄。
	Birthday string
	// AvatarSourceURL 是 ywtb 的头像地址（issue #29）。
	AvatarSourceURL string
}

// RawAcademic 是教务源返回的学籍字段。
type RawAcademic struct {
	Major     string
	ClassName string
	Grade     string

	// 以下来自同一页 bjiben.asp（issue #29）。
	College          string
	ExamNo           string
	Gender           string
	StudyDuration    string
	EducationLevel   string
	NewMajor         string
	NewClassName     string
	EnrollmentStatus string
	EnrollmentDate   string
	Nation           string
	PoliticalStatus  string
	HomeAddress      string
	TrainStation     string
	PersonalPhone    string
	HomePhone        string
	Specialty        string

	// RefreshedTGT 是换票响应里带回的**新** go-admin-tgt。
	// 实测：调用换票接口时响应里直接给出 tgt 字段，可用于后续换票与续期，
	// 因此不需要每次都让客户端重新登录。
	RefreshedTGT string
}

// Options 是适配器构造参数。
type Options struct {
	Auth     AuthSource
	Academic AcademicSource
	// AcademicTimeout 是教务抓取的单次超时；<=0 时用 8s。
	// 教务慢不应该把登录拖死——超时就按「画像不完整」处理。
	AcademicTimeout time.Duration
	// Logger 可为空。
	Logger Logger
}

// Logger 是最小日志接口，避免把 slog 直接带进业务包。
type Logger interface {
	Warn(msg string, args ...any)
}

// Authenticator 实现 identity/login.SchoolAuthenticator。
type Authenticator struct {
	auth     AuthSource
	academic AcademicSource
	timeout  time.Duration
	logger   Logger
}

// New 构造适配器。
func New(o Options) (*Authenticator, error) {
	if o.Auth == nil {
		return nil, fmt.Errorf("schoolauth: 需要 AuthSource")
	}
	if o.AcademicTimeout <= 0 {
		o.AcademicTimeout = 8 * time.Second
	}
	return &Authenticator{
		auth:     o.Auth,
		academic: o.Academic,
		timeout:  o.AcademicTimeout,
		logger:   o.Logger,
	}, nil
}

// Authenticate 用客户端凭据换取学校身份。
//
// 失败语义：
//   - 凭据为空或认证源拒绝 → 返回 ErrAuthFailed（由 login 子域转成 bad_credentials）；
//   - 认证成功但教务抓取失败 → **不报错**，返回 ProfileIncomplete=true。
func (a *Authenticator) Authenticate(ctx context.Context, cred Credential) (Identity, error) {
	if cred.Empty() {
		return Identity{}, ErrAuthFailed
	}
	raw, err := a.auth.FetchIdentity(ctx, cred)
	if err != nil {
		return Identity{}, ErrAuthFailed
	}
	if strings.TrimSpace(raw.StudentNo) == "" {
		// 认证源没给出学号，等同于认证失败——没有学号就无法确定是谁。
		return Identity{}, ErrAuthFailed
	}

	// 完整号优先；若上游只给了掩码，则从掩码取后四位。
	fullID := raw.IDCardFull
	if fullID == "" {
		fullID = raw.IDCardMasked
	}
	ident := Identity{
		StudentNo:     raw.StudentNo,
		Name:          raw.Name,
		College:       raw.College,
		IDCardSuffix4: Suffix4FromMasked(fullID),
		IDCardFull:    raw.IDCardFull,
		// ywtb 字段（issue #29）
		Birthday:        raw.Birthday,
		AvatarSourceURL: raw.AvatarSourceURL,
	}

	// 教务：超时或失败都只影响画像完整性，不影响登录。
	if a.academic == nil {
		ident.ProfileIncomplete = true
		return ident, nil
	}
	actx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	ac, aerr := a.academic.FetchAcademic(actx, cred)
	if aerr != nil {
		ident.ProfileIncomplete = true
		if a.logger != nil {
			// 只记错误类别，不记 cookie。
			a.logger.Warn("教务学籍抓取失败，画像将不完整",
				"studentNo", raw.StudentNo, "err", aerr.Error())
		}
		return ident, nil
	}
	ident.Major = ac.Major
	ident.ClassName = ac.ClassName
	ident.Grade = ac.Grade

	// 教务档案页其余字段（issue #29）。
	ident.ExamNo = ac.ExamNo
	ident.Gender = ac.Gender
	ident.StudyDuration = ac.StudyDuration
	ident.EducationLevel = ac.EducationLevel
	ident.NewMajor = ac.NewMajor
	ident.NewClassName = ac.NewClassName
	ident.EnrollmentStatus = ac.EnrollmentStatus
	ident.EnrollmentDate = ac.EnrollmentDate
	ident.Nation = ac.Nation
	ident.PoliticalStatus = ac.PoliticalStatus
	ident.HomeAddress = ac.HomeAddress
	ident.TrainStation = ac.TrainStation
	ident.PersonalPhone = ac.PersonalPhone
	ident.HomePhone = ac.HomePhone
	ident.Specialty = ac.Specialty

	// 院校字段：教务的「系别」比认证结果的「所属单位」更准，优先采用。
	if ac.College != "" {
		ident.College = ac.College
	}

	if ident.Major == "" && ident.ClassName == "" && ident.Grade == "" {
		// 抓到了但字段全空，同样视为不完整。
		ident.ProfileIncomplete = true
	}
	return ident, nil
}

// maskedTail 匹配掩码证件号末尾的四位数字，例如 `650****2614`。
var maskedTail = regexp.MustCompile(`(\d{4})$`)

// Suffix4FromMasked 从证件号（完整或掩码）取后四位。
//
// 实测：统一身份认证页面上显示 `650****2614`，只保留前 3 与后 4。
// 因此**完整号拿不到**，只能取后四位；这也是 ADR-0002 决定只存后四位的原因。
func Suffix4FromMasked(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	m := maskedTail.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

// 编译期断言：适配器要能接受 http.Cookie。
var _ = func(c *http.Cookie) SessionCookie {
	return SessionCookie{Name: c.Name, Value: c.Value}
}

// ---- from bridge.go ----
// 本文件把适配器的结果接到身份域需要的形状上，而不让 schoolauth 直接 import identity。
//
// 做法：用**泛型化的小接口**做桥——装配层把 login.SchoolCredential 的字段
// 以参数形式传进来，适配器不依赖 identity 包的具体类型。
// 这样 school 与 identity 两个域之间没有直接 import；桥接逻辑放在装配层
// （internal/app），符合「业务域不互相 import」的约束。

// DomainIdentity 是身份域能接受的身份形状。
// 由装配层把 schoolauth.Identity 映射成 identity/login.SchoolIdentity。
type DomainIdentity struct {
	StudentNo     string
	Name          string
	College       string
	IDCardSuffix4 string
	// IDCardFull 是完整号（空表示只有掩码）。由装配层加密后写入画像。
	IDCardFull        string
	Major             string
	ClassName         string
	Grade             string
	ProfileIncomplete bool
}

// AsDomain 把适配器结果转成身份域形状。
func (i Identity) AsDomain() DomainIdentity {
	return DomainIdentity{
		StudentNo:         i.StudentNo,
		Name:              i.Name,
		College:           i.College,
		IDCardSuffix4:     i.IDCardSuffix4,
		IDCardFull:        i.IDCardFull,
		Major:             i.Major,
		ClassName:         i.ClassName,
		Grade:             i.Grade,
		ProfileIncomplete: i.ProfileIncomplete,
	}
}

// 已删除（#25 / Spec M10）：原 httpSource / HTTPSourceOptions / NewHTTPSource 骨架实现。
//
// 删除理由：
//  1. **零生产消费者**：全仓 grep 确认只有它自己的测试在用；装配层实际注入的是
//     YWTBSource（ywtb.go，基于 Bearer + laravel_session 的真实协议）。
//  2. **它是「等实测确认后再填」的占位**：ParseIdentity / ParseAcademic 需外部注入，
//     而真实协议早已通过 ywtb 与教务三跳链路实测确认并落地，占位已无意义。
//  3. **注释与代码曾长期不一致**（标着「当前为骨架」），留在仓库里只会误导后续接手者
//     以为「还有一条待补的第二取数路径」。
//
// 若将来真的需要第二条取数路径，应基于实测协议新写实现，而不是复活这个占位骨架。

// ---- from doer_contract.go ----
// 本文件记录 Cheng 教务取数对 HTTPDoer 的**行为契约**。
//
// 为什么单独写一份：这条约束踩过一次，症状极其误导。
//
// 实测（2026-11-09，真实网络）：教务建立会话是 3 跳重定向
//
//	caslogin.asp → check_cas.asp?sign=.. → note.asp → index1.asp
//
// 而 `ASPSESSIONID*` 这个会话 cookie 是在**中间跳**的 302 上下发的。
//
// 如果 HTTPDoer 的实现启用了自动跟随（`http.Client` 的默认行为，
// 即 `CheckRedirect` 返回 nil），且没有配置 `cookiejar`，则：
//   - 客户端自己跟完了所有跳转；
//   - 中间跳的 Set-Cookie 被丢弃（没有 jar 就没处存）；
//   - 调用方拿到的是**最终响应**（200、无 Set-Cookie）。
//
// JiaowuSource 据此判断「教务未下发会话 cookie」，报错退出——
// 而真实原因跟 cookie 毫无关系，是「跳转被代理跟完了」。
//
// 因此 HTTPDoer 的实现必须满足：
//
//  1. **不自动跟随 3xx**：返回跳转响应本身，由调用方决定是否跟随。
//     这也是出站闸口（ADR-0001）的既定要求：3xx 要能被看见与拦截，
//     而不是被传输层悄悄吃掉。
//  2. **不自行管理 cookie**：Cookie 由调用方显式传入请求头，
//     响应里的 Set-Cookie 由调用方显式收集（resp.Cookies()）。
//     闸口本来就会剥离 Set-Cookie（ADR-0001 第 5 条）——
//     因此本条在闸口下表现为「实现方需要把 cookie 交回给调用方」。
//
// 若后续给 HTTPDoer 换成真实实现，务必在构造 http.Client 时设置：
//
//	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
//
// 否则本包会在第一步就误判失败。

// HTTPDoerContract 仅作文档占位，说明 JiaowuSource 对 HTTPDoer 的行为要求。
// 详见本文件顶部说明。
const HTTPDoerContract = "must-not-follow-redirects"
