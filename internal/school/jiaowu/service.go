package jiaowu

import (
	"context"
	"io"
	"net/http"
	"strings"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
)

// Service 是教务子域的服务。Address 只返回地址；Fetch 由后端经出站闸口代取。
//
// doer 是出站闸口（*GuardedDoer 实现 platform.HTTPDoer 接口）。Address 本身不
// 使用它，但服务结构已经「接住」这份能力；进程无状态：Service 没有任何可变字段。
type Service struct {
	doer *GuardedDoer
}

// New 构造 Service。装配层把 platform 的能力通过接口注入进来；
// 业务子域不 import app / server / config，也不自行构造具体实现。
// 签名与 #13 保持一致（仍是 platform.HTTPDoer），内部包一层出站闸口，
// 因此 app.New 里 jiaowu.New(doer) 一行无需改动即获得 Fetch 与闸口能力。
func New(doer platform.HTTPDoer) *Service {
	return &Service{doer: newGuardedDoer(doer)}
}

// AddressResult 是 Address 的输出：逻辑目标名 + 客户端可直接使用的编码地址。
type AddressResult struct {
	Target  string `json:"target"`
	Address string `json:"address"`
}

// Address 只返回地址，绝不发起任何出站请求。
//
// 白名单闸门是 school.Resolve：调用方只能传逻辑目标名，未知名字 / 把 URL 当名字 /
// 把 host 当名字，一律以 *school.Error{Code: CodeTargetDenied} 拒绝。闸门在任何
// 出站之前生效（本入口压根不碰 doer），因此「零出站」对成功与拒绝两条路径都成立。
//
// path 是可选查询条件（形如 /xxx 或 /xxx?k=v），以明文挂在编码地址后面；它只能
// 成为白名单 host 上的路径，无法替换目标 host —— 结构性 SSRF 保证仍然成立。
func (s *Service) Address(_ context.Context, name school.TargetName, path string) (AddressResult, error) {
	tgt, err := school.Resolve(name)
	if err != nil {
		return AddressResult{}, err
	}

	// 从「已校验过的直连形态」出发，拼上可选 path，再交给纯函数 Encode 产出
	// 经网关的编码地址（含固定 KEY_HEX 前缀）。Resolve + Encode 两步都在本入口内
	// 显式走一遍，不绕过白名单、不读任何外部状态、不发请求。
	raw := tgt.Direct.String() + normalizePath(path)
	encoded, err := school.Encode(raw)
	if err != nil {
		return AddressResult{}, err
	}

	return AddressResult{Target: string(tgt.Name), Address: encoded}, nil
}

// normalizePath 把可选查询条件规范成挂在直连地址后面的 path(+query) 段：
// 空值不动；缺前导斜杠时补上，保证它只能成为白名单 host 上的路径而非新目标。
func normalizePath(path string) string {
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

// FetchResult 是 Fetch 的输出：上游状态码与响应体（已由闸口剥掉 Set-Cookie）。
type FetchResult struct {
	Status int
	Body   []byte
}

// Fetch 由后端经唯一出站闸口代取，并返回结果。
//
// 与 Address 的分工：Address 只给地址、客户端自己请求；Fetch 是「需要后端代做」
// 时的那条路。二者共用同一个 school.Resolve 白名单闸门——未知名在触达出站之前
// 就被以 CodeTargetDenied 拒绝，底层零请求。
//
// 302 / Set-Cookie 的处理全部在 GuardedDoer（实现层）完成，本方法只转述错误：
// CodeBlockedRedirect、CodeSessionExpired 由调用方（handler）映射为不同响应。
func (s *Service) Fetch(ctx context.Context, name school.TargetName, path string) (FetchResult, error) {
	tgt, err := school.Resolve(name)
	if err != nil {
		return FetchResult{}, err
	}

	// 从已校验过的经网关形态出发，拼上可选的明文 path。
	raw := tgt.Vpn.String() + normalizePath(path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return FetchResult{}, err
	}

	resp, err := s.doer.Do(ctx, req)
	if err != nil {
		return FetchResult{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return FetchResult{}, err
	}
	return FetchResult{Status: resp.StatusCode, Body: body}, nil
}
