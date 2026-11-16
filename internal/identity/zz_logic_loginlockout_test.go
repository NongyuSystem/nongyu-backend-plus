package identity_test

// 登录失败限流的回归测试（lead 独占写入）。
//
// 覆盖两条断言：
//   T1：农屿账密登录（LoginByPassword）同样有按账号失败限流 —— RED 已实证过，
//       修复后第 6 次失败触发 TooManyAttempts。
//   T2：注入跨实例 Counter 时，两个**独立构造**的 Service 共享计数
//       （模拟多副本部署：实例 A 打 5 次失败，实例 B 立即被锁）。

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"nongyu-backend-plus/internal/identity"
	"nongyu-backend-plus/internal/identity/login"
	"nongyu-backend-plus/internal/platform"
)

// ---------- 最小 fake（只为把两种登录跑起来） ----------

type lfClock struct{ t time.Time }

func (c lfClock) Now() time.Time { return c.t }

type lfUsers struct{ u platform.User }

func (r lfUsers) Create(context.Context, string, platform.Role) (platform.User, error) {
	return r.u, nil
}
func (r lfUsers) ByStudentNo(_ context.Context, no string) (platform.User, error) {
	if no == r.u.StudentNo {
		return r.u, nil
	}
	return platform.User{}, platform.ErrNotFound
}
func (r lfUsers) ByID(_ context.Context, id int64) (platform.User, error) {
	if id == r.u.ID {
		return r.u, nil
	}
	return platform.User{}, platform.ErrNotFound
}
func (r lfUsers) SetStatus(context.Context, int64, platform.UserStatus) error { return nil }
func (r lfUsers) TouchLogin(context.Context, int64, string, string) error     { return nil }

type lfProfiles struct{}

func (lfProfiles) Upsert(context.Context, platform.Profile) error { return nil }
func (lfProfiles) ByUserID(context.Context, int64) (platform.Profile, error) {
	return platform.Profile{}, platform.ErrNotFound
}

// lfMethods 只对 password 方式返回一条「已设置」的记录（用明文当密文，
// 配合下面的 identity Secrets 让 Open 原样返回）。
type lfMethods struct{}

func (lfMethods) Bind(_ context.Context, m platform.LoginMethod) (platform.LoginMethod, error) {
	return m, nil
}
func (lfMethods) ByIdentifier(context.Context, platform.LoginMethodKind, string) (platform.LoginMethod, error) {
	return platform.LoginMethod{}, platform.ErrNotFound
}
func (lfMethods) ByUserAndKind(_ context.Context, _ int64, k platform.LoginMethodKind) (platform.LoginMethod, error) {
	if k == platform.LoginPassword {
		return platform.LoginMethod{Kind: k, SecretSealed: []byte("right-password")}, nil
	}
	return platform.LoginMethod{}, platform.ErrNotFound
}
func (lfMethods) UpdateSecret(context.Context, int64, platform.LoginMethodKind, []byte) error {
	return nil
}
func (lfMethods) Unbind(context.Context, int64, platform.LoginMethodKind) error { return nil }

// lfSecrets 是恒等"加密"：Seal/Open 原样返回，让测试无需真 AES。
type lfSecrets struct{}

func (lfSecrets) Seal(p []byte) ([]byte, error) { return p, nil }
func (lfSecrets) Open(s []byte) ([]byte, error) { return s, nil }

// memCounter 是实现 login.Counter 的内存原子计数器（模拟 Redis 自增）。
// 用它来验证「两个独立 Service 共享同一份计数」= 跨实例。
type memCounter struct {
	mu   sync.Mutex
	data map[string]int64
}

func newMemCounter() *memCounter { return &memCounter{data: map[string]int64{}} }

func (m *memCounter) IncrBy(_ context.Context, key string, delta int64, _ time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] += delta
	return m.data[key], nil
}

func (m *memCounter) Get(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[key], nil
}

// newLoginSvc 构造一个指向固定用户的 login.Service。counter 为 nil 时走进程内兜底。
func newLoginSvc(t *testing.T, studentNo string, counter login.Counter) *login.Service {
	t.Helper()
	return login.New(login.Options{
		Users:       lfUsers{u: platform.User{ID: 1, StudentNo: studentNo, Role: platform.RoleUser, Status: platform.UserActive}},
		Profiles:    lfProfiles{},
		Methods:     lfMethods{},
		Clock:       lfClock{t: time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)},
		PasswordBox: lfSecrets{},
		IDCardBox:   lfSecrets{},
		FailCounter: counter,
	})
}

// TestZZLogic3_PasswordLoginRateLimited 断言农屿账密登录同样有按账号限流。
func TestZZLogic3_PasswordLoginRateLimited(t *testing.T) {
	const studentNo = "202600002"
	svc := newLoginSvc(t, studentNo, nil)
	ctx := context.Background()

	for i := 0; i < 30; i++ {
		_, err := svc.LoginByPassword(ctx, studentNo, "wrong-password")
		if err == nil {
			t.Fatalf("第 %d 次：错误密码竟然登录成功", i+1)
		}
		if identity.CodeOf(err) == identity.CodeTooManyAttempts {
			t.Logf("GREEN：农屿账密第 %d 次失败后触发限流", i+1)
			return
		}
	}
	t.Fatalf("RED：农屿账密连打 30 次错误密码，从未限流——与身份证登录同样的爆破面")
}

// TestZZLogic4_SharedCounterAcrossInstances 断言跨实例共享计数。
func TestZZLogic4_SharedCounterAcrossInstances(t *testing.T) {
	const studentNo = "202600003"
	shared := newMemCounter()
	ctx := context.Background()

	// 模拟两个副本：各自独立构造 Service，但共享同一个外置计数器。
	instanceA := newLoginSvc(t, studentNo, shared)
	instanceB := newLoginSvc(t, studentNo, shared)

	// 实例 A 打满 5 次失败。
	for i := 0; i < 5; i++ {
		_, err := instanceA.LoginByPassword(ctx, studentNo, "wrong-password")
		if err == nil {
			t.Fatalf("实例A 第 %d 次：错误密码竟然成功", i+1)
		}
	}

	// 实例 B 的第一次尝试就必须被锁（证明计数是跨实例的，不是各算各的）。
	_, err := instanceB.LoginByPassword(ctx, studentNo, "wrong-password")
	if err == nil {
		t.Fatal("实例B：应已被锁")
	}
	if got := identity.CodeOf(err); got != identity.CodeTooManyAttempts {
		t.Fatalf("RED：实例A 打满 5 次失败后，实例B 仍能继续尝试（返回 %q）。"+
			"说明计数没有跨实例共享——多副本部署时攻击者可在实例间分摊尝试次数。", got)
	}
	t.Logf("GREEN：实例A 打满 5 次失败后，实例B 立即返回 %q —— 计数跨实例共享", identity.CodeTooManyAttempts)

	_ = fmt.Sprint() // 保持 fmt 使用，便于后续扩展日志
}
