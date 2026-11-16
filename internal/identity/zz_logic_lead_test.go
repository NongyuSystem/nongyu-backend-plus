package identity_test

// 业务逻辑漏洞审计（lead 独占写入文件）。
//
// L2：身份证后四位 + 姓名拼音首字母登录（方式④）**没有任何失败次数限制**。
//
// 为什么这是真漏洞而不是理论风险：
//   - 该方式的"凭据"是「学号 + 身份证后四位 + 姓名拼音首字母」。
//   - 对同一届同学这三项都是**半公开**的：学号 9 位有规律可枚举；身份证后四位
//     只有 10^4 种可能，且同地区同龄人前 14 位常相同；拼音首字母可由姓名推出。
//   - LoginByIDCard 用 constantTimeEqual 比较两个因子（防的是**时序**侧信道），
//     但**没有**任何失败次数计数、递增延迟或账号锁定。
//   - 唯一外层防护是「按 IP 的全局限流」（RATE_LIMIT_PER_MINUTE），它不是按账号
//     计的，攻击者换 IP / 分布式即可对同一学号无限重试。
//
// 后果：对已知学号枚举后四位（配合首字母）即可接管账号。
//
// 安全期望：连续失败 N 次后，该账号的该登录方式应被临时限制
// （CodeTooManyAttempts），且计数按**账号**维度而非仅按 IP。

import (
	"context"
	"testing"
	"time"

	"nongyu-backend-plus/internal/identity"
	"nongyu-backend-plus/internal/identity/login"
	"nongyu-backend-plus/internal/platform"
)

// ---------- 最小 fake（只为把 LoginByIDCard 跑起来） ----------

type l2Clock struct{}

func (l2Clock) Now() time.Time { return time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC) }

type l2Users struct{ u platform.User }

func (r l2Users) Create(context.Context, string, platform.Role) (platform.User, error) {
	return r.u, nil
}
func (r l2Users) ByStudentNo(_ context.Context, no string) (platform.User, error) {
	if no == r.u.StudentNo {
		return r.u, nil
	}
	return platform.User{}, platform.ErrNotFound
}
func (r l2Users) ByID(_ context.Context, id int64) (platform.User, error) {
	if id == r.u.ID {
		return r.u, nil
	}
	return platform.User{}, platform.ErrNotFound
}
func (r l2Users) SetStatus(context.Context, int64, platform.UserStatus) error { return nil }
func (r l2Users) TouchLogin(context.Context, int64, string, string) error     { return nil }

type l2Profiles struct{ p platform.Profile }

func (r l2Profiles) Upsert(context.Context, platform.Profile) error { return nil }
func (r l2Profiles) ByUserID(_ context.Context, id int64) (platform.Profile, error) {
	if id == r.p.UserID {
		return r.p, nil
	}
	return platform.Profile{}, platform.ErrNotFound
}

type l2Methods struct{}

func (l2Methods) Bind(_ context.Context, m platform.LoginMethod) (platform.LoginMethod, error) {
	return m, nil
}
func (l2Methods) ByIdentifier(context.Context, platform.LoginMethodKind, string) (platform.LoginMethod, error) {
	return platform.LoginMethod{}, platform.ErrNotFound
}
func (l2Methods) ByUserAndKind(context.Context, int64, platform.LoginMethodKind) (platform.LoginMethod, error) {
	return platform.LoginMethod{}, platform.ErrNotFound
}
func (l2Methods) UpdateSecret(context.Context, int64, platform.LoginMethodKind, []byte) error {
	return nil
}
func (l2Methods) Unbind(context.Context, int64, platform.LoginMethodKind) error { return nil }

// l2Initials 固定返回 ZS，代表"姓名已由拼音库算出"。
type l2Initials struct{}

func (l2Initials) Initials(string) (string, error) { return "ZS", nil }

// TestZZLogic2_IDCardLoginBruteForceable
//
// 断言：对同一学号连续用**错误**的后四位尝试多次后，必须出现 CodeTooManyAttempts。
// 当前实现下永远是 CodeBadCredentials → 本测试 RED，证明可无限爆破。
func TestZZLogic2_IDCardLoginBruteForceable(t *testing.T) {
	ctx := context.Background()
	const studentNo = "202600001"

	svc := login.New(login.Options{
		Users:    l2Users{u: platform.User{ID: 1, StudentNo: studentNo, Role: platform.RoleUser, Status: platform.UserActive}},
		Profiles: l2Profiles{p: platform.Profile{UserID: 1, Name: "张三", IDCardSuffix4: "2614"}},
		Methods:  l2Methods{},
		Clock:    l2Clock{},
		Initials: l2Initials{},
	})

	const attempts = 30
	var last identity.Code
	for i := 0; i < attempts; i++ {
		// 错误后四位 + 正确首字母（攻击者通常已知姓名）。
		_, err := svc.LoginByIDCard(ctx, studentNo, "0000", "ZS")
		if err == nil {
			t.Fatalf("第 %d 次：错误后四位竟然登录成功", i+1)
		}
		last = identity.CodeOf(err)
		if last == identity.CodeTooManyAttempts {
			t.Logf("GREEN：第 %d 次失败后触发限流，无法继续爆破", i+1)
			return
		}
	}

	t.Fatalf("RED：对学号 %s 连续 %d 次错误的后四位尝试，每次都是 %q，从未限流/锁定。"+
		"身份证后四位只有 10^4 种可能，攻击者可换 IP 无限重试直到命中并接管账号；"+
		"外层仅「按 IP」全局限流，缺少**按账号**的失败计数与递增延迟。",
		studentNo, attempts, last)
}
