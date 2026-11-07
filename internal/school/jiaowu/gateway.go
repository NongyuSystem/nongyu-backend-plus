package jiaowu

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
)

// 出站闸口（ADR-0001 第 1、5 条 / 架构落点草图 §4）。
//
// GuardedDoer 是「后端有没有权发请求」的唯一强制点：白名单校验、不跟随指向真站的
// 302、剥离上游 Set-Cookie 都在这里一次做完。Service 只调用 Do、只转述错误，
// 绝不重复判断——这样 ADR 的每条边界都只有一个可执行位置。
//
// 它包住 platform.HTTPDoer（骨架期是 MockHTTPDoer），对业务域仍以接口形式出现；
// 后续把强制逻辑下沉到 platform 实现层时，只需让 GuardedDoer 退化成透传即可。

const webvpnHost = "webvpn.sicau.edu.cn"

// allowedHosts 是出站目标的内建白名单，与 school 的逻辑目标名一一对应。
// 调用方只能传逻辑目标名（由 school.Resolve 校验），这里再做一次 host 级兜底，
// 形成「逻辑名 → 白名单 host」的双层结构性保证。
var allowedHosts = map[string]struct{}{
	"jiaowu.sicau.edu.cn": {},
	"auth.sicau.edu.cn":   {},
	webvpnHost:            {},
}

// GuardedDoer 把出站策略钉死在唯一闸口上。
type GuardedDoer struct {
	inner platform.HTTPDoer
}

// newGuardedDoer 包住底层出站实现。
func newGuardedDoer(inner platform.HTTPDoer) *GuardedDoer {
	return &GuardedDoer{inner: inner}
}

// Do 是唯一的出站入口：先校验白名单，再调用底层一次，最后按 ADR 处理响应。
// 任何被拒绝的请求都不会触达底层 doer（Requests() 保持 0）。
func (g *GuardedDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, &school.Error{Code: school.CodeTargetDenied}
	}
	if !hostAllowed(req.URL) {
		return nil, &school.Error{Code: school.CodeTargetDenied}
	}

	resp, err := g.inner.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return resp, nil
	}

	// 3xx：Location 指向 webvpn 域内视为会话过期；指向真站（或任何非 webvpn 域）
	// 一律禁止跟随，返回 blocked_redirect。这里只发出过一次底层请求。
	if isRedirect(resp.StatusCode) {
		loc := resp.Header.Get("Location")
		if target, perr := url.Parse(loc); perr == nil && target.Host != "" {
			if isWebvpnHost(target.Host) {
				return nil, &school.Error{Code: school.CodeSessionExpired}
			}
			return nil, &school.Error{Code: school.CodeBlockedRedirect}
		}
		// 相对 Location 或解析失败：保守起见同样不跟随。
		return nil, &school.Error{Code: school.CodeBlockedRedirect}
	}

	// 上游 Set-Cookie 一律不回显给调用方。票据下发由后续 Login 接口单独负责，
	// 与这里的「代取」是两条独立路径（ADR-0001 第 5 条）。
	resp.Header.Del("Set-Cookie")
	return resp, nil
}

func hostAllowed(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	_, ok := allowedHosts[host]
	return ok
}

func isWebvpnHost(host string) bool {
	return strings.EqualFold(strings.ToLower(host), webvpnHost)
}

func isRedirect(status int) bool {
	return status >= 300 && status < 400
}
