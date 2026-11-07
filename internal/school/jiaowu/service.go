package jiaowu

import (
	"context"
	"strings"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
)

// Service 是教务子域的服务。本票只暴露 Address 一个入口。
//
// 字段刻意保持最小：doer 是出站闸口（platform.HTTPDoer 接口），Address 本身不
// 使用它，但服务结构已经「接住」这份能力，后续 Fetch/换票票直接复用即可；
// 届时新增 tickets/cipher/clock 等字段并扩展 New 的构造参数，不改调用方形状。
// 进程无状态：Service 没有任何可变字段。
type Service struct {
	doer platform.HTTPDoer
}

// New 构造 Service。装配层把 platform 的能力通过接口注入进来；
// 业务子域不 import app / server / config，也不自行构造具体实现。
func New(doer platform.HTTPDoer) *Service {
	return &Service{doer: doer}
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
