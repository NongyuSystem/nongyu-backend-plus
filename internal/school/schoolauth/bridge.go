package schoolauth

// 本文件把适配器接到身份域的接口上，而不让 schoolauth import identity。
//
// 做法：用**泛型化的小接口**做桥——装配层把 login.SchoolCredential 的字段
// 用参数形式传进来，适配器不依赖 identity 包的具体类型。
//
// 这样 school 与 identity 两个域之间没有直接 import，
// 桥接逻辑放在装配层（internal/app），符合「业务域不互相 import」的约束。

import (
	"context"
	"fmt"

	"nongyu-backend-plus/internal/platform"
)

// DomainIdentity 是身份域能接受的身份形状。
// 由装配层把 schoolauth.Identity 映射成 identity/login.SchoolIdentity。
type DomainIdentity struct {
	StudentNo         string
	Name              string
	College           string
	IDCardSuffix4     string
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
		Major:             i.Major,
		ClassName:         i.ClassName,
		Grade:             i.Grade,
		ProfileIncomplete: i.ProfileIncomplete,
	}
}

// httpSource 是基于 HTTPDoer 的 AuthSource / AcademicSource 骨架实现。
//
// **当前为骨架**：真实抓取需要实测确认
//   - 用整套 cookie 请求哪个地址能拿到账户信息；
//   - 请求教务 bjiben.asp 前是否需要先用 cookie 换教务会话。
//
// 因此这里只实现「发请求 + 校验状态码」的骨架，解析函数留成可注入的
// ParseIdentity / ParseAcademic，实测确认结构后填进去即可，
// 不必改动调用方与测试。
type httpSource struct {
	doer          platform.HTTPDoer
	identityURL   string
	academicURL   string
	parseIdentity func([]byte) (RawIdentity, error)
	parseAcademic func([]byte) (RawAcademic, error)
}

// HTTPSourceOptions 是 HTTP 源实现的构造参数。
type HTTPSourceOptions struct {
	Doer          platform.HTTPDoer
	IdentityURL   string
	AcademicURL   string
	ParseIdentity func([]byte) (RawIdentity, error)
	ParseAcademic func([]byte) (RawAcademic, error)
}

// NewHTTPSource 构造基于 HTTPDoer 的双源实现（同时满足 AuthSource 与 AcademicSource）。
func NewHTTPSource(o HTTPSourceOptions) (*httpSource, error) {
	if o.Doer == nil {
		return nil, fmt.Errorf("schoolauth: 需要 HTTPDoer")
	}
	if o.ParseIdentity == nil {
		return nil, fmt.Errorf("schoolauth: 需要 ParseIdentity（实测确认响应结构后注入）")
	}
	return &httpSource{
		doer:          o.Doer,
		identityURL:   o.IdentityURL,
		academicURL:   o.AcademicURL,
		parseIdentity: o.ParseIdentity,
		parseAcademic: o.ParseAcademic,
	}, nil
}

// FetchIdentity 实现 AuthSource。
func (s *httpSource) FetchIdentity(ctx context.Context, cookieHeader string) (RawIdentity, error) {
	req, err := newCookieRequest(ctx, s.identityURL, cookieHeader)
	if err != nil {
		return RawIdentity{}, err
	}
	body, err := fetch(ctx, s.doer, req)
	if err != nil {
		return RawIdentity{}, err
	}
	return s.parseIdentity(body)
}

// FetchAcademic 实现 AcademicSource。
func (s *httpSource) FetchAcademic(ctx context.Context, cookieHeader string) (RawAcademic, error) {
	if s.academicURL == "" || s.parseAcademic == nil {
		return RawAcademic{}, fmt.Errorf("schoolauth: 教务源未配置")
	}
	req, err := newCookieRequest(ctx, s.academicURL, cookieHeader)
	if err != nil {
		return RawAcademic{}, err
	}
	body, err := fetch(ctx, s.doer, req)
	if err != nil {
		return RawAcademic{}, err
	}
	return s.parseAcademic(body)
}
