package announcement

import (
	"testing"
	"time"
)

// 这一层是纯函数单测：不启 HTTP、不碰仓储，只喂数据给可见性判定。
// 依据 docs/specs/spec-六类常见功能.md 的 Testing Decisions ①。

func ts(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func published(a *Announcement) *Announcement {
	a.Status = StatusPublished
	a.PublishedAt = ts("2026-11-01T00:00:00Z")
	// 默认给「全员」：零值 Audience 的 Scope 是空串，按 fail-closed
	// 会被判定为不命中（这是刻意的——未知范围不该默认放行）。
	if a.Audience.Scope == "" {
		a.Audience = Audience{Scope: AudienceAll}
	}
	return a
}

var now = *ts("2026-11-12T00:00:00Z")

func TestListable_StatusGating(t *testing.T) {
	all := Viewer{}

	draft := &Announcement{Title: "t", Status: StatusDraft}
	if Listable(draft, all, now) {
		t.Fatal("草稿不应出现在读者列表")
	}
	revoked := published(&Announcement{Title: "t"})
	revoked.Status = StatusRevoked
	if Listable(revoked, all, now) {
		t.Fatal("已撤回不应出现在读者列表")
	}
	if Readable(revoked, all) {
		t.Fatal("已撤回不应可读：撤回的语义是「当作没发过」")
	}
	if !Listable(published(&Announcement{Title: "t"}), all, now) {
		t.Fatal("已发布的全员公告应可列")
	}
}

func TestListable_Deleted(t *testing.T) {
	a := published(&Announcement{Title: "t"})
	a.DeletedAt = ts("2026-11-02T00:00:00Z")
	if Listable(a, Viewer{}, now) || Readable(a, Viewer{}) {
		t.Fatal("软删的公告既不可列也不可读")
	}
}

func TestListable_ExpiryHidesListButKeepsDetail(t *testing.T) {
	a := published(&Announcement{Title: "t"})
	a.ExpiresAt = ts("2026-11-05T00:00:00Z")

	if Listable(a, Viewer{}, now) {
		t.Fatal("过期公告不应出现在默认列表")
	}
	if !Readable(a, Viewer{}) {
		t.Fatal("过期公告详情仍应可查，否则读者无法核对之前看过的通知")
	}
}

func TestListable_EffectiveFromInFuture(t *testing.T) {
	a := published(&Announcement{Title: "t"})
	a.EffectiveFrom = ts("2026-12-01T00:00:00Z")
	if Listable(a, Viewer{}, now) {
		t.Fatal("尚未生效的公告不应出现在列表")
	}
}

func TestAudience_All_VisibleToAnonymous(t *testing.T) {
	a := published(&Announcement{Title: "t"})
	a.Audience = Audience{Scope: AudienceAll}
	if !Listable(a, Viewer{}, now) {
		t.Fatal("全员公告应对未登录读者可见（公开读的前提）")
	}
}

func TestAudience_Role(t *testing.T) {
	a := published(&Announcement{Title: "t"})
	a.Audience = Audience{Scope: AudienceRole, Roles: []int{1, 2}}

	if Listable(a, Viewer{}, now) {
		t.Fatal("按角色定向的公告对未登录读者不可见（fail-closed）")
	}
	if !Listable(a, Viewer{Authenticated: true, Role: 1}, now) {
		t.Fatal("role=1 命中应可见")
	}
	if Listable(a, Viewer{Authenticated: true, Role: 0}, now) {
		t.Fatal("role=0 未命中应不可见")
	}
}

func TestAudience_CollegeGrade(t *testing.T) {
	a := published(&Announcement{Title: "t"})
	a.Audience = Audience{Scope: AudienceCollegeGrade, College: "计算机学院", Grade: "2023"}

	cases := []struct {
		name string
		v    Viewer
		want bool
	}{
		{"两边都命中", Viewer{Authenticated: true, College: "计算机学院", Grade: "2023"}, true},
		{"学院不命中", Viewer{Authenticated: true, College: "外国语学院", Grade: "2023"}, false},
		{"年级不命中", Viewer{Authenticated: true, College: "计算机学院", Grade: "2022"}, false},
		{"画像缺失按不命中", Viewer{Authenticated: true}, false},
		{"未登录按不命中", Viewer{College: "计算机学院", Grade: "2023"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Listable(a, tc.v, now); got != tc.want {
				t.Fatalf("Listable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAudience_OnlyOneDimension(t *testing.T) {
	// 只按年级定向：学院维度不限制。
	a := published(&Announcement{Title: "t"})
	a.Audience = Audience{Scope: AudienceCollegeGrade, Grade: "2023"}

	if !Listable(a, Viewer{Authenticated: true, College: "任何学院", Grade: "2023"}, now) {
		t.Fatal("只按年级定向时应忽略学院差异")
	}
	if Listable(a, Viewer{Authenticated: true, College: "任何学院", Grade: "2022"}, now) {
		t.Fatal("年级不匹配应不可见")
	}
}

func TestAudience_UnknownScopeFailsClosed(t *testing.T) {
	a := published(&Announcement{Title: "t"})
	a.Audience = Audience{Scope: "something_else"}
	if Listable(a, Viewer{Authenticated: true, Role: 2}, now) {
		t.Fatal("未知受众范围必须不命中，不能默认放行")
	}
}

func TestValidate_CreateInput(t *testing.T) {
	cases := []struct {
		name string
		a    *Announcement
		ok   bool
	}{
		{"合法全员", &Announcement{Title: "t", Body: "b", Audience: Audience{Scope: AudienceAll}}, true},
		{"空标题", &Announcement{Title: "", Body: "b", Audience: Audience{Scope: AudienceAll}}, false},
		{"空正文", &Announcement{Title: "t", Body: "", Audience: Audience{Scope: AudienceAll}}, false},
		{"role 缺角色列表", &Announcement{Title: "t", Body: "b", Audience: Audience{Scope: AudienceRole}}, false},
		{"college_grade 两维都空", &Announcement{Title: "t", Body: "b", Audience: Audience{Scope: AudienceCollegeGrade}}, false},
		{"all 却带条件", &Announcement{Title: "t", Body: "b", Audience: Audience{Scope: AudienceAll, College: "x"}}, false},
		{"失效早于生效", &Announcement{
			Title: "t", Body: "b", Audience: Audience{Scope: AudienceAll},
			EffectiveFrom: ts("2026-11-10T00:00:00Z"),
			ExpiresAt:     ts("2026-11-01T00:00:00Z"),
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validate(tc.a)
			if tc.ok && err != nil {
				t.Fatalf("期望通过，得到错误：%v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("期望被拒，但通过了")
			}
		})
	}
}

func TestCursor_RoundTrip(t *testing.T) {
	// 游标必须能无损往返：它要跨请求携带 (pinned, publishedAt, id) 三元组。
	orig := PageCursor{Pinned: true, PublishedAt: time.UnixMilli(1700000000123).UTC(), ID: 42}
	enc := encodeCursor(orig)
	got, err := decodeCursor(enc)
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	if got.Pinned != orig.Pinned || got.ID != orig.ID || !got.PublishedAt.Equal(orig.PublishedAt) {
		t.Fatalf("往返不一致：got=%+v want=%+v", got, orig)
	}
}

func TestCursor_InvalidRejected(t *testing.T) {
	for _, raw := range []string{"!!!not-base64!!!", "Zm9v", "cHwwfHg=="} {
		if _, err := decodeCursor(raw); err == nil {
			t.Fatalf("非法游标 %q 应被拒", raw)
		}
	}
}

func TestCursor_EmptyMeansFirstPage(t *testing.T) {
	c, err := decodeCursor("")
	if err != nil {
		t.Fatalf("空游标不应报错：%v", err)
	}
	if !c.Zero() {
		t.Fatal("空游标应解析为零值（从最新开始）")
	}
}
