package schoolauth_test

// tgt 保管与续期的测试。
//
// 维护者决定（2026-11-09）：换票响应会带回新 tgt，因此服务端把 tgt 加密存起来，
// 10 天内复用并滚动续期，不必每次让客户端重交凭据。

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school/schoolauth"
)

// memTGTStore 是 TicketStore 的内存实现（测试用）。
type memTGTStore struct {
	data map[string]struct {
		sealed []byte
		exp    time.Time
	}
}

func newMemTGTStore() *memTGTStore {
	return &memTGTStore{data: map[string]struct {
		sealed []byte
		exp    time.Time
	}{}}
}

func (m *memTGTStore) Put(_ context.Context, userID string, sealed []byte, expiresAt time.Time) error {
	m.data[userID] = struct {
		sealed []byte
		exp    time.Time
	}{sealed, expiresAt}
	return nil
}

func (m *memTGTStore) Get(_ context.Context, userID string) ([]byte, time.Time, error) {
	v, ok := m.data[userID]
	if !ok {
		return nil, time.Time{}, platform.ErrNotFound
	}
	return v.sealed, v.exp, nil
}

func (m *memTGTStore) Delete(_ context.Context, userID string) error {
	delete(m.data, userID)
	return nil
}

func (m *memTGTStore) ListExpiringSoon(_ context.Context, before time.Time, limit int) ([]platform.TicketRef, error) {
	var out []platform.TicketRef
	for id, v := range m.data {
		if limit > 0 && len(out) >= limit {
			break
		}
		if !v.exp.IsZero() && v.exp.Before(before) {
			out = append(out, platform.TicketRef{UserID: id, ExpiresAt: v.exp})
		}
	}
	return out, nil
}

// 保存后的 tgt 必须加密（不能明文躺在存储里）。
func TestTGTManager_SaveEncrypts(t *testing.T) {
	store := newMemTGTStore()
	m, err := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:  store,
		Cipher: platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source: mustJiaowuSource(t),
		Clock:  platform.NewMockClock(time.Now()),
	})
	if err != nil {
		t.Fatalf("NewTGTManager: %v", err)
	}

	const raw = "201690:TGT:secret-value"
	if err := m.Save(context.Background(), "u1", raw); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 存储里的密文不得等于明文
	sealed, ok := store.data["u1"]
	if !ok {
		t.Fatal("应写入存储")
	}
	if string(sealed.sealed) == raw {
		t.Fatal("tgt 不得明文存储")
	}
	// 但能正确解回来
	got, _, err := m.Load(context.Background(), "u1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != raw {
		t.Fatalf("解出值不符：%q", got)
	}
}

// 本地有未临期的 tgt 时，直接返回它，不去换票。
func TestTGTManager_EnsureFreshUsesStoredWhenNotExpiring(t *testing.T) {
	store := newMemTGTStore()
	clock := platform.NewMockClock(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	// Source 用 nil doer 会让任何换票尝试失败——正好用来证明「没去换票」。
	m, _ := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:  store,
		Cipher: platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source: mustJiaowuSource(t),
		Clock:  clock,
	})
	ctx := context.Background()

	if err := m.Save(ctx, "u1", "201690:TGT:stored"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := m.EnsureFresh(ctx, "u1", "")
	if err != nil {
		t.Fatalf("EnsureFresh 应直接用本地 tgt，实际 %v", err)
	}
	if got != "201690:TGT:stored" {
		t.Fatalf("应返回本地 tgt，实际 %q", got)
	}
}

// 本地没有、客户端也没给 → 明确报错（不静默继续）。
func TestTGTManager_EnsureFreshFailsWithoutAnyTGT(t *testing.T) {
	store := newMemTGTStore()
	m, _ := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:  store,
		Cipher: platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source: mustJiaowuSource(t),
		Clock:  platform.NewMockClock(time.Now()),
	})
	_, err := m.EnsureFresh(context.Background(), "u1", "")
	if err == nil {
		t.Fatal("既无本地 tgt 又无客户端凭据应报错")
	}
}

// 过期时刻必须按配置的 TTL 写（默认 9 天）。
func TestTGTManager_SaveSetsTTL(t *testing.T) {
	store := newMemTGTStore()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	clock := platform.NewMockClock(now)
	m, _ := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:  store,
		Cipher: platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source: mustJiaowuSource(t),
		Clock:  clock,
	})
	ctx := context.Background()
	if err := m.Save(ctx, "u1", "t"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, exp, err := m.Load(ctx, "u1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := now.Add(9 * 24 * time.Hour)
	if !exp.Equal(want) {
		t.Fatalf("过期时刻应为 9 天后（%v），实际 %v", want, exp)
	}
}

// Clear 后本地就没有了。
func TestTGTManager_Clear(t *testing.T) {
	store := newMemTGTStore()
	m, _ := schoolauth.NewTGTManager(schoolauth.TGTManagerOptions{
		Store:  store,
		Cipher: platform.NewMockCipher("tgt-key-for-tests-32-bytes!!!!!"),
		Source: mustJiaowuSource(t),
		Clock:  platform.NewMockClock(time.Now()),
	})
	ctx := context.Background()
	_ = m.Save(ctx, "u1", "t")
	if err := m.Clear(ctx, "u1"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, _, err := m.Load(ctx, "u1"); !platform.IsNotFound(err) {
		t.Fatalf("Clear 后应不存在，实际 %v", err)
	}
}

func TestNewTGTManager_RequiresDeps(t *testing.T) {
	cases := []struct {
		name string
		o    schoolauth.TGTManagerOptions
	}{
		{"缺 Store", schoolauth.TGTManagerOptions{Cipher: platform.NewMockCipher("k"), Source: mustJiaowuSource(t)}},
		{"缺 Cipher", schoolauth.TGTManagerOptions{Store: newMemTGTStore(), Source: mustJiaowuSource(t)}},
		{"缺 Source", schoolauth.TGTManagerOptions{Store: newMemTGTStore(), Cipher: platform.NewMockCipher("k")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := schoolauth.NewTGTManager(c.o); err == nil {
				t.Fatal("缺依赖应在构造期报错")
			}
		})
	}
}

var _ = errors.Is

// mustJiaowuSource 构造一个不会真的发出请求的 JiaowuSource（doer 恒失败）。
// 用来证明「某些路径不该去换票」。
type failDoer struct{}

func (failDoer) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("测试中不应发起请求")
}

func mustJiaowuSource(t *testing.T) *schoolauth.JiaowuSource {
	t.Helper()
	s, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{Doer: failDoer{}})
	if err != nil {
		t.Fatalf("NewJiaowuSource: %v", err)
	}
	return s
}
