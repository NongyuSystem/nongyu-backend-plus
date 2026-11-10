package schoolauth

// 本文件实现「取学校身份」的唯一路径：**用 go-admin-tgt 换票，从换票响应读身份**。
//
// 实测协议（2026-11-09，见 docs/tech/实测-川农CAS真实链路-tgt万能凭据.md）：
//
//	GET https://auth.sicau.edu.cn/api/v1/cas/login?appid=<教务appid>
//	Cookie: go-admin-tgt=<客户端登录后拿到的 tgt>
//
//	响应：{"requestId":"...","code":200,"msg":"登录成功","data":"<加密 base64>"}
//	解密（DecryptYWTB）后的明文：
//	  {"callbackUrl":"...","tgt":"...","st":"...",
//	   "user":{"id":201690,"number":"202500001","name":"张三",
//	           "phone":"...","group":[{"alias":"本科生"}]},...}
//
// 关键事实：**go-admin-tgt 是万能凭据**。客户端在学校统一身份认证登录后
// 只会拿到这张 cookie，不会有 ywtb 的 Bearer token。后端拿它换票，
// 一次请求同时得到 st（给教务）与 user（身份），不需要再走任何 Bearer 路径。
//
// 本文件曾有一条「Bearer + getUserInfo」的备选路径，已按维护者指示**删除**：
// 两条路径并存会让「客户端到底该交什么」变得含糊，实测证明 tgt 单独就够。
//
// 代价（已知且接受）：换票响应**没有学院与身份证**。
// 学院/身份证只在那条已删除的 getUserInfo 里有；若将来确需，应另立规格
// 走教务档案页取，而不是复活 Bearer 路径。

import (
	"context"
	"fmt"
	"strings"

	"nongyu-backend-plus/internal/platform"
)

// TicketIdentity 是「换票顺带取到的身份」。
//
// 换票响应明文里就带 user，所以这条路径不需要额外的凭据。
// 缺点：**没有学院与身份证**，由上层按画像不完整语义处理。
type TicketIdentity struct {
	StudentNo string
	Name      string
	Phone     string
	Category  string // 「本科生」等，来自 user.group[].alias
	// Birthday 与 AvatarURL 不来自换票响应，留空即可；
	// 需要它们的场合请用 FetchYWTBIdentity（要 Bearer）。
	Birthday  string
	AvatarURL string
}

// TicketExchanger 是「用 tgt 换票并顺带取身份」的能力。
//
// 由 JiaowuSource 实现（它本就要换票拿 st，一次请求两用）。
// 抽成接口而不是直接依赖 JiaowuSource，是为了保持源之间不硬耦合。
type TicketExchanger interface {
	// ExchangeForIdentity 用 tgt 换票并返回身份。
	ExchangeForIdentity(ctx context.Context, cred Credential) (TicketIdentity, error)
}

// YWTBOptions 是取身份源的构造参数。
type YWTBOptions struct {
	// Doer 是出站闸口（白名单、超时、3xx 拦截都在它里面）。
	// 保留它是为了让本类型仍能独立构造（例如健康检查/诊断场景）。
	Doer platform.HTTPDoer
	// BaseURL 默认 https://ywtb.sicau.edu.cn；当前取身份不走 ywtb，保留以兼容构造。
	BaseURL string
	// Exchanger **必需**：真正负责用 tgt 换票并取身份。
	Exchanger TicketExchanger
}

// YWTBSource 实现 AuthSource。
//
// 名字保留 `YWTB` 是因为它在身份域里的角色仍是「学校侧身份源」；
// 但实现上它已经**不再直连 ywtb 的 getUserInfo**，只通过换票拿身份。
type YWTBSource struct {
	doer      platform.HTTPDoer
	baseURL   string
	exchanger TicketExchanger
}

// NewYWTBSource 构造取身份源。
func NewYWTBSource(o YWTBOptions) (*YWTBSource, error) {
	if o.Doer == nil {
		return nil, fmt.Errorf("schoolauth: YWTBSource 需要 HTTPDoer")
	}
	if o.Exchanger == nil {
		return nil, fmt.Errorf("schoolauth: YWTBSource 需要 TicketExchanger（用 tgt 换票取身份）")
	}
	base := o.BaseURL
	if base == "" {
		base = "https://ywtb.sicau.edu.cn"
	}
	return &YWTBSource{doer: o.Doer, baseURL: strings.TrimRight(base, "/"), exchanger: o.Exchanger}, nil
}

// FetchIdentity 实现 AuthSource：用 go-admin-tgt 换票并读回身份。
//
// 这是**唯一**的取身份路径。客户端在学校统一身份认证登录后交来的就是这张 cookie。
func (s *YWTBSource) FetchIdentity(ctx context.Context, cred Credential) (RawIdentity, error) {
	tgt := cookieValue(cred, goAdminTgtCookie)
	if strings.TrimSpace(tgt) == "" {
		return RawIdentity{}, fmt.Errorf("schoolauth: 缺少 %s（需客户端在统一身份认证登录后交来）", goAdminTgtCookie)
	}

	ti, err := s.exchanger.ExchangeForIdentity(ctx, cred)
	if err != nil {
		return RawIdentity{}, err
	}
	if strings.TrimSpace(ti.StudentNo) == "" {
		return RawIdentity{}, fmt.Errorf("schoolauth: 换票响应未返回学号，无法确定身份")
	}
	return RawIdentity{
		StudentNo: ti.StudentNo,
		Name:      ti.Name,
		// 学院与身份证在换票响应里没有 —— 留空，由上层标画像不完整。
	}, nil
}
