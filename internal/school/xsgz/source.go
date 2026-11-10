package xsgz

import (
	"context"
	"fmt"
	"strings"

	"nongyu-backend-plus/internal/platform"
)

// Exchanger 是「用 tgt 换票并顺带取身份」的能力（由 CAS 侧实现）。
//
// 本包只声明**最小接口**而不 import schoolauth：这样 xsgz 与 schoolauth
// 之间没有环，调用方在本包外把具体实现注入进来即可。
type Exchanger interface {
	// ExchangeForIdentity 用 tgt 换票并返回身份（学号 + 姓名）。
	ExchangeForIdentity(ctx context.Context, tgt string) (studentNo, name string, err error)
}

// Identity 是取身份的结果（本包自己的形状，不依赖上层类型）。
type Identity struct {
	StudentNo string
	Name      string
	College   string
	Major     string
	ClassName string
	Grade     string
	// IDCardFull 是完整身份证号（上层负责立即加密，明文不落库）。
	IDCardFull string
	Birthday   string
	// Detail 是完整档案（取到时有值，供落库回调使用）。
	Detail *StudentDetail
}

// Options 是 Source 的构造参数。
type Options struct {
	Doer      platform.HTTPDoer
	Exchanger Exchanger
	// Token 是固定的 access token（多为测试用；正常走 tgt 现换）。
	Token string
	// OnDetail 是取到完整档案后的回调，由装配层接上「落库」。
	// 设计成回调而不是在这里直接依赖存储：本包不该知道「数据存哪」。
	OnDetail func(ctx context.Context, detail StudentDetail) error
}

// Source 是学工系统的取身份源。
//
// token 的三种来源，按优先级：
//   - 外部直接给的 XSGZToken（最省一次往返）
//   - 后端用 tgt 现换（默认路径，客户端只交 tgt 时走它）
//   - 构造时注入的固定 token（测试用）
type Source struct {
	doer      platform.HTTPDoer
	exchanger Exchanger
	token     string
	onDetail  func(ctx context.Context, detail StudentDetail) error
	// decrypt 是换票明文的解密函数（由调用方注入 DecryptYWTB）。
	decrypt func(requestID, data string) ([]byte, error)
}

// NewSource 构造学工取身份源。
func NewSource(o Options, decrypt func(requestID, data string) ([]byte, error)) (*Source, error) {
	if o.Doer == nil {
		return nil, fmt.Errorf("xsgz: Source 需要 HTTPDoer")
	}
	if o.Exchanger == nil {
		return nil, fmt.Errorf("xsgz: Source 需要 Exchanger（用 tgt 换票确认学号）")
	}
	if decrypt == nil {
		return nil, fmt.Errorf("xsgz: Source 需要解密函数")
	}
	return &Source{
		doer:      o.Doer,
		exchanger: o.Exchanger,
		token:     strings.TrimSpace(o.Token),
		onDetail:  o.OnDetail,
		decrypt:   decrypt,
	}, nil
}

// WithToken 设置固定的 access token。
func (s *Source) WithToken(t string) *Source {
	s.token = strings.TrimSpace(t)
	return s
}

// FetchIdentity 换票确认身份 + 取完整档案。
//
// 档案取不到时**不阻断登录**：身份已由换票确认，返回只含学号+姓名的结果。
func (s *Source) FetchIdentity(ctx context.Context, tgt, xsgzToken string) (Identity, error) {
	if strings.TrimSpace(tgt) == "" {
		return Identity{}, fmt.Errorf("xsgz: 缺少 %s", goAdminTgtCookie)
	}

	// ① 换票：确认「这个人是谁」（学号）
	studentNo, name, err := s.exchanger.ExchangeForIdentity(ctx, tgt)
	if err != nil {
		return Identity{}, err
	}
	if strings.TrimSpace(studentNo) == "" {
		return Identity{}, fmt.Errorf("xsgz: 换票响应未返回学号")
	}

	// ② 取完整档案（唯一数据源）
	token := strings.TrimSpace(xsgzToken)
	if token == "" {
		if t, terr := FetchToken(ctx, s.doer, "", tgt, s.decrypt); terr == nil {
			token = t
		}
	}
	if token == "" {
		token = s.token
	}

	detail, err := FetchDetail(ctx, s.doer, "", token, studentNo)
	if err != nil {
		// 档案取不到时不阻断登录：身份已由换票确认。
		return Identity{StudentNo: studentNo, Name: name}, nil
	}

	// 落库：交给装配层注入的回调（失败不阻断登录）。
	if s.onDetail != nil {
		if derr := s.onDetail(ctx, detail); derr != nil {
			_ = derr
		}
	}

	d := detail
	return Identity{
		StudentNo:  d.Typed.StudentNo,
		Name:       d.Typed.StudentName,
		College:    d.Typed.CollegeName,
		Major:      d.Typed.SpecialtyName,
		ClassName:  d.Typed.ClassName,
		Grade:      d.Typed.Grade,
		IDCardFull: d.Typed.IDCardNo,
		Birthday:   d.Typed.Birthday,
		Detail:     &d,
	}, nil
}

// FetchDetail 取完整档案（供上层直接落库用）。
func (s *Source) FetchDetail(ctx context.Context, tgt, xsgzToken string) (StudentDetail, error) {
	if strings.TrimSpace(tgt) == "" {
		return StudentDetail{}, fmt.Errorf("xsgz: 缺少 %s", goAdminTgtCookie)
	}
	studentNo, _, err := s.exchanger.ExchangeForIdentity(ctx, tgt)
	if err != nil {
		return StudentDetail{}, err
	}
	token := strings.TrimSpace(xsgzToken)
	if token == "" {
		if t, terr := FetchToken(ctx, s.doer, "", tgt, s.decrypt); terr == nil {
			token = t
		}
	}
	if token == "" {
		token = s.token
	}
	return FetchDetail(ctx, s.doer, "", token, studentNo)
}
