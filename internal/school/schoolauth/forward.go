package schoolauth

import (
	"context"
	"fmt"
	"strings"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/cas"
	"nongyu-backend-plus/internal/school/xsgz"
)

// 本文件是 schoolauth 对 cas / xsgz 两个子包的**转发层**。
//
// 拆分背景（2026-11-10）：sso.go 曾是 833 行、三十个顶层声明的混合体，
// 里面塞了三套互不相干的协议——CAS 换票（auth 域）、ywtb 取身份、
// 学工取档案（xsgz 域）——再加一张站点登记表。改动任何一个都很难看清边界。
//
// 现在按协议域拆成两个包：
//
//	internal/school/cas   → CAS 换票、ywtb、站点登记表
//	internal/school/xsgz  → 学工 token / enterHall / detail
//
// 两个子包**都不 import schoolauth**（端口类型各自定义最小接口），
// 因此没有循环依赖。本文件保留原有导出名做转发，调用方（app.go 等）
// 一行都不用改。

// ── 站点登记（来自 cas 包）────────────────────────────────────────

// SiteAppID 是已知站点在统一身份认证平台注册的 appid。
var SiteAppID = cas.SiteAppID

// SiteKind / SiteProfile / SiteProfiles / SitePersonal 等原样转发，
// 让既有代码里的 schoolauth.SiteProfiles 继续可用。
type (
	SiteKind    = cas.SiteKind
	SiteProfile = cas.SiteProfile
)

const (
	SitePersonal   = cas.SitePersonal
	SitePortal     = cas.SitePortal
	SiteOwnAccount = cas.SiteOwnAccount
	SiteGateway    = cas.SiteGateway
)

// SiteProfiles 是逐站实测的站点目录（见 cas 包注释）。
var SiteProfiles = cas.SiteProfiles

// ── CAS 换票（来自 cas 包）────────────────────────────────────────

type (
	CASUser   = cas.User
	CASResult = cas.Result
)

// ExchangeCAS 用 go-admin-tgt 为指定 appid 换票。
func ExchangeCAS(ctx context.Context, doer platform.HTTPDoer, authBase, appid, tgt, cookieHeader string) (CASResult, error) {
	return cas.Exchange(ctx, doer, authBase, appid, tgt, cookieHeader, DecryptYWTB)
}

// ── ywtb（来自 cas 包）────────────────────────────────────────────

type YWTBIdentity = cas.YWTBIdentity

// FetchYWTBIdentity 用 ywtb 自己的 Bearer token 取完整身份（含身份证）。
func FetchYWTBIdentity(ctx context.Context, doer platform.HTTPDoer, baseURL, bearer string) (YWTBIdentity, error) {
	return cas.FetchYWTBIdentity(ctx, doer, baseURL, bearer, DecryptYWTB)
}

// ── 学工（来自 xsgz 包）──────────────────────────────────────────

type (
	XSGZStudentDetail = xsgz.StudentDetail
	XSGZTyped         = xsgz.Typed
	XSGZProfileData   = xsgz.ProfileData
)

// XSGZGroupKeys 是实测的 13 个分组名。
var XSGZGroupKeys = xsgz.GroupKeys

// ParseXSGZDetail 解析 detail 端点的 data 段。
func ParseXSGZDetail(data []byte) (XSGZStudentDetail, error) {
	return xsgz.ParseDetail(data)
}

// FetchXSGZToken 用统一的 go-admin-tgt 换出学工系统的 access token。
func FetchXSGZToken(ctx context.Context, doer platform.HTTPDoer, authBase, tgt string) (string, error) {
	return xsgz.FetchToken(ctx, doer, authBase, tgt, DecryptYWTB)
}

// EnterXSGZHall 是取 detail 之前必须完成的两步（详见 xsgz 包注释）。
func EnterXSGZHall(ctx context.Context, doer platform.HTTPDoer, baseURL, token string) error {
	return xsgz.EnterHall(ctx, doer, baseURL, token)
}

// FetchXSGZStudentDetail 取学工系统的完整学生档案（内部自动 enterHall）。
func FetchXSGZStudentDetail(ctx context.Context, doer platform.HTTPDoer, baseURL, token, studentNo string) (XSGZStudentDetail, error) {
	return xsgz.FetchDetail(ctx, doer, baseURL, token, studentNo)
}

// ToProfileData 把 detail 展开成落库用的纯数据。
//
// 注意这里写成普通函数而非方法：XSGZStudentDetail 是 xsgz.StudentDetail 的
// 类型别名，Go 不允许给非本包定义的类型加方法。调用方写成
// schoolauth.ToProfileData(detail) 即可；xsgz.StudentDetail 上仍有同名方法。
func ToProfileData(d XSGZStudentDetail) XSGZProfileData {
	return d.ToProfileData()
}

// 本文件把 xsgz 包的取数能力适配成 schoolauth 的 AuthSource 端口。
//
// 为什么需要适配：xsgz 包刻意不 import schoolauth（避免环），所以它的
// Exchanger 接口形状是 (tgt) → (studentNo, name)，而 schoolauth 这边的
// TicketExchanger 形状是 (Credential) → (TicketIdentity)。两边语义相同、
// 形状不同，适配放在这一层做，两个子包都不用知道对方。

// XSGZOptions 是 XSGZSource 的构造参数。
type XSGZOptions struct {
	Doer      platform.HTTPDoer
	Exchanger TicketExchanger
	// Token 是学工系统的 access token（客户端完成统一登录后交来；可空）。
	Token string
	// OnDetail 是取到完整档案后的回调，由装配层接上「落库」。
	// 回调失败不阻断登录（身份已由换票确认），但会记录日志。
	OnDetail func(ctx context.Context, detail XSGZStudentDetail) error
}

// XSGZSource 实现 AuthSource：换票确认身份 + 取学工完整档案。
type XSGZSource struct {
	inner *xsgz.Source
}

// NewXSGZSource 构造学工取身份源。
func NewXSGZSource(o XSGZOptions) (*XSGZSource, error) {
	if o.Exchanger == nil {
		return nil, fmt.Errorf("schoolauth: XSGZSource 需要 TicketExchanger（用 tgt 换票确认学号）")
	}
	inner, err := xsgz.NewSource(xsgz.Options{
		Doer:      o.Doer,
		Exchanger: exchangerAdapter{e: o.Exchanger},
		Token:     o.Token,
		OnDetail: func(ctx context.Context, d xsgz.StudentDetail) error {
			if o.OnDetail == nil {
				return nil
			}
			return o.OnDetail(ctx, d)
		},
	}, DecryptYWTB)
	if err != nil {
		return nil, fmt.Errorf("schoolauth: %w", err)
	}
	return &XSGZSource{inner: inner}, nil
}

// WithToken 设置固定的 access token。
func (s *XSGZSource) WithToken(t string) *XSGZSource {
	s.inner.WithToken(t)
	return s
}

// FetchIdentity 实现 AuthSource。
func (s *XSGZSource) FetchIdentity(ctx context.Context, cred Credential) (RawIdentity, error) {
	tgt := cookieValue(cred, goAdminTgtCookie)
	if strings.TrimSpace(tgt) == "" {
		return RawIdentity{}, fmt.Errorf("schoolauth: 缺少 %s", goAdminTgtCookie)
	}
	id, err := s.inner.FetchIdentity(ctx, tgt, cred.XSGZToken)
	if err != nil {
		return RawIdentity{}, err
	}
	return RawIdentity{
		StudentNo:    id.StudentNo,
		Name:         id.Name,
		College:      id.College,
		Major:        id.Major,
		ClassName:    id.ClassName,
		Grade:        id.Grade,
		IDCardMasked: id.IDCardFull,
		IDCardFull:   id.IDCardFull,
		Birthday:     id.Birthday,
	}, nil
}

// FetchDetail 取完整档案（供上层直接落库用）。
func (s *XSGZSource) FetchDetail(ctx context.Context, cred Credential) (XSGZStudentDetail, error) {
	tgt := cookieValue(cred, goAdminTgtCookie)
	if strings.TrimSpace(tgt) == "" {
		return XSGZStudentDetail{}, fmt.Errorf("schoolauth: 缺少 %s", goAdminTgtCookie)
	}
	return s.inner.FetchDetail(ctx, tgt, cred.XSGZToken)
}

// exchangerAdapter 把 schoolauth 的 TicketExchanger 适配成 xsgz.Exchanger。
type exchangerAdapter struct {
	e TicketExchanger
}

// ExchangeForIdentity 用 tgt 换票并返回学号与姓名。
func (a exchangerAdapter) ExchangeForIdentity(ctx context.Context, tgt string) (string, string, error) {
	ti, err := a.e.ExchangeForIdentity(ctx, Credential{RawCookie: goAdminTgtCookie + "=" + tgt})
	if err != nil {
		return "", "", err
	}
	return ti.StudentNo, ti.Name, nil
}
