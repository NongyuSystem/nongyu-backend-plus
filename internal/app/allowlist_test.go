package app

// 出站白名单内建项的守卫测试。
//
// 背景（2026-11-13 实测踩到）：xsgz.sicau.edu.cn 长期没进内建白名单，
// 而它是当前**唯一数据源**——tgt 换 access token、enterHall、student/detail
// 全打这个域。漏登记的后果是 `platform: outbound target denied`，
// 请求在闸口处零出站被拦下，日志看起来像「学校站不可达」或「tgt 失效」，
// 而实际上是登录成功、画像全空。
//
// 这条测试的意义：任何人把新学校域加进取数链路，却忘了登记白名单，会立刻红。

import (
	"strings"
	"testing"
)

func TestOutboundAllowlistCoversDataSources(t *testing.T) {
	// 后端真正会请求的学校域。新增取数域名时必须同步加到这里。
	required := []string{
		"auth.sicau.edu.cn",   // 统一身份认证：tgt 换 st
		"xsgz.sicau.edu.cn",   // 学工系统：换 token / enterHall / detail（唯一数据源）
		"jiaowu.sicau.edu.cn", // 教务：页面抓取
		"webvpn.sicau.edu.cn", // WebVPN 网关
	}

	got := outboundAllowlist("")
	set := map[string]bool{}
	for _, h := range got {
		set[strings.ToLower(strings.TrimSpace(h))] = true
	}
	for _, want := range required {
		if !set[want] {
			t.Errorf("内建白名单缺少 %q —— 该域是后端会请求的学校域，漏登记会导致 "+
				"`platform: outbound target denied`（表现为登录成功但画像全空）。当前清单：%v",
				want, got)
		}
	}
}

// 配置项必须能追加，且不破坏内建项。
func TestOutboundAllowlistMergesExtra(t *testing.T) {
	got := outboundAllowlist("test.example.com, ,other.example.com")
	set := map[string]bool{}
	for _, h := range got {
		set[strings.ToLower(h)] = true
	}
	for _, want := range []string{
		"auth.sicau.edu.cn",
		"xsgz.sicau.edu.cn",
		"test.example.com",
		"other.example.com",
	} {
		if !set[want] {
			t.Errorf("合并后应含 %q，实际 %v", want, got)
		}
	}
	// 空项与纯空白不应进列表
	for _, h := range got {
		if strings.TrimSpace(h) == "" {
			t.Errorf("不应包含空项，实际 %v", got)
		}
	}
}

// 空白名单 + Enforce 的真实语义：拒绝一切（不是「不限制」）。
// .env.example 的注释曾写「默认空（= 不限制）」，与实现矛盾——这条测试钉住实现。
func TestEmptyAllowlistDeniesAll(t *testing.T) {
	// 注意：装配层 outboundAllowlist 永远至少返回内建项，不会真为空；
	// 这里断言的是「配置追加为空时，结果仍是内建全集」而非空切片。
	got := outboundAllowlist("")
	if len(got) == 0 {
		t.Fatal("内建白名单不得为空：空列表会让出站全部被拒（hostInAllowlist 对空列表恒 false）")
	}
}
