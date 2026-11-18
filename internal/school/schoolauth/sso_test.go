package schoolauth_test

// 把「8 个站到底能不能拿数据」的实测结论钉死在测试里（2026-11-10）。
// 维护者最初预期「一次登录后 8 个站都能用」，逐站实测后发现只有 3 个
// 真有个人数据。不留断言，后人很容易重新假设「认证通了 = 数据能抓」。
//
// 来源说明：本段在原拼接文件里用 `// ── from sso_test.go ──` 标注，
// 但标记形式与其他段不同（带 `──` 装饰），拆分时按 sso_test.go 还原。

import (
	"testing"

	"nongyu-backend-plus/internal/school/schoolauth"
)

func TestSiteProfiles_CoversAllRequestedSites(t *testing.T) {
	// 维护者原始清单的 8 个站必须都在目录里，一个都不能漏。
	want := []string{"jiaowu", "ywtb", "xsc", "zjc", "cwc", "lib", "webvpn", "ecology"}
	for _, name := range want {
		if _, ok := schoolauth.SiteProfiles[name]; !ok {
			t.Errorf("站点目录缺 %q（维护者清单要求覆盖）", name)
		}
	}
}

func TestSiteProfiles_OnlyThreeHavePersonalData(t *testing.T) {
	// 实测结论：教务 / ywtb / xsgz 三个站有个人数据；
	// webvpn 是网关（不提供数据）；其余是门户或独立账号体系。
	personal := map[string]bool{"jiaowu": true, "ywtb": true, "xsgz": true}
	for name, p := range schoolauth.SiteProfiles {
		got := p.Kind == schoolauth.SitePersonal
		if got != personal[name] {
			t.Errorf("%s 的类别是 %q，与实测结论不符（有个人数据的只有 jiaowu/ywtb/xsgz）", name, p.Kind)
		}
	}
}

func TestSiteProfiles_GatewayIsNotData(t *testing.T) {
	p := schoolauth.SiteProfiles["webvpn"]
	if p.Kind != schoolauth.SiteGateway {
		t.Fatalf("webvpn 应为网关（只用于访问内网），实际 %q", p.Kind)
	}
}

func TestSiteProfiles_OPACHasOwnAccount(t *testing.T) {
	// 图书馆 OPAC 是唯一确认「有个人数据但不认统一认证」的系统。
	p := schoolauth.SiteProfiles["opac"]
	if p.Kind != schoolauth.SiteOwnAccount {
		t.Fatalf("opac 应为独立账号体系，实际 %q", p.Kind)
	}
}

func TestSiteProfiles_EveryEntryHasNote(t *testing.T) {
	for name, p := range schoolauth.SiteProfiles {
		if p.Note == "" {
			t.Errorf("%s 缺少 Note（必须写明为什么归到这个类别）", name)
		}
		if p.Host == "" {
			t.Errorf("%s 缺少 Host", name)
		}
	}
}
