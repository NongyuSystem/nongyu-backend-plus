package platform_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/platform"
)

// 身份域仓储的集成测试：真连 Postgres。
// 未配置 DATABASE_URL 时跳过，保证 go test ./... 不依赖环境。

func newIdentityStore(t *testing.T) *platform.PGIdentityStore {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过 Postgres 集成测试")
	}
	s, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsn})
	if err != nil {
		t.Fatalf("NewPGIdentityStore: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Ping(ctx); err != nil {
		t.Skipf("Postgres 不可达，跳过：%v", err)
	}
	// 清理本次可能残留的测试数据（学号前缀 9999）。
	cleanupTestUsers(t, s)
	t.Cleanup(func() {
		cleanupTestUsers(t, s)
		_ = s.Close()
	})
	return s
}

func cleanupTestUsers(t *testing.T, s *platform.PGIdentityStore) {
	t.Helper()
	ctx := context.Background()
	// 用固定前缀的学号定位测试用户，逐条删除（sessions/login_methods/profiles 有级联）。
	for _, no := range testStudentNos() {
		if u, err := s.ByStudentNo(ctx, no); err == nil {
			_, _ = s.RevokeByUserScope(ctx, u.ID, platform.ScopeApp)
			_, _ = s.RevokeByUserScope(ctx, u.ID, platform.ScopeAdmin)
			delUser(ctx, no)
		}
	}
}

func testStudentNos() []string {
	return []string{"999000001", "999000002", "999000003"}
}

// delUser 直接删 users 行（CASCADE 带走 profile 与 login_methods 与 sessions）。
func delUser(_ context.Context, studentNo string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return
	}
	defer pool.Close()
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE student_no = $1`, studentNo)
}

func TestUserRepo_CreateAndLookup(t *testing.T) {
	s := newIdentityStore(t)
	ctx := context.Background()
	no := "999000001"

	u, err := s.Create(ctx, no, platform.RoleUser)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if u.ID == 0 || u.StudentNo != no || u.Role != platform.RoleUser || u.Status != platform.UserActive {
		t.Fatalf("建出的用户字段不符：%+v", u)
	}

	// 重复学号 → ErrAlreadyExists
	if _, err := s.Create(ctx, no, platform.RoleUser); !platform.IsAlreadyExists(err) {
		t.Fatalf("重复学号应返回 ErrAlreadyExists，实际 %v", err)
	}

	// 按学号 / 按主键都能查到
	byNo, err := s.ByStudentNo(ctx, no)
	if err != nil || byNo.ID != u.ID {
		t.Fatalf("ByStudentNo: %+v err=%v", byNo, err)
	}
	byID, err := s.ByID(ctx, u.ID)
	if err != nil || byID.StudentNo != no {
		t.Fatalf("ByID: %+v err=%v", byID, err)
	}

	// 查不到 → ErrNotFound
	if _, err := s.ByStudentNo(ctx, "999999999"); !platform.IsNotFound(err) {
		t.Fatalf("不存在的学号应返回 ErrNotFound，实际 %v", err)
	}

	// 改状态
	if err := s.SetStatus(ctx, u.ID, platform.UserDisabled); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	got, _ := s.ByID(ctx, u.ID)
	if got.Status != platform.UserDisabled {
		t.Fatalf("状态未更新：%v", got.Status)
	}
}

func TestProfileRepo_UpsertAndRead(t *testing.T) {
	s := newIdentityStore(t)
	ctx := context.Background()
	u, err := s.Create(ctx, "999000002", platform.RoleUser)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	p := platform.Profile{
		UserID:        u.ID,
		Name:          "姚一飞",
		IDCardSealed:  []byte("sealed-idcard"),
		IDCardSuffix4: "1234",
		College:       "信息工程学院",
		Major:         "数据科学与大数据技术",
		ClassName:     "大数据202503",
		Grade:         "2025",
		QQ:            "10001",
	}
	if err := s.Upsert(ctx, p); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.ByUserID(ctx, u.ID)
	if err != nil {
		t.Fatalf("ByUserID: %v", err)
	}
	if got.Name != p.Name || got.College != p.College || got.IDCardSuffix4 != "1234" {
		t.Fatalf("画像读回不符：%+v", got)
	}
	if string(got.IDCardSealed) != "sealed-idcard" {
		t.Fatalf("身份证密文应原样存取，实际 %q", got.IDCardSealed)
	}

	// 再 Upsert 应覆盖而非报错
	p.QQ = "20002"
	if err := s.Upsert(ctx, p); err != nil {
		t.Fatalf("二次 Upsert: %v", err)
	}
	got, _ = s.ByUserID(ctx, u.ID)
	if got.QQ != "20002" {
		t.Fatalf("Upsert 应覆盖，实际 qq=%q", got.QQ)
	}

	// 不存在的画像 → ErrNotFound
	if _, err := s.ByUserID(ctx, 99999999); !platform.IsNotFound(err) {
		t.Fatalf("不存在的画像应返回 ErrNotFound，实际 %v", err)
	}
}

func TestLoginMethodRepo_BindAndLookup(t *testing.T) {
	s := newIdentityStore(t)
	ctx := context.Background()
	u, err := s.Create(ctx, "999000003", platform.RoleUser)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	m, err := s.Bind(ctx, platform.LoginMethod{
		UserID:     u.ID,
		Kind:       platform.LoginMailCode,
		Identifier: "999000003@sicau.edu.cn",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if m.ID == 0 || m.Kind != platform.LoginMailCode {
		t.Fatalf("绑定结果不符：%+v", m)
	}

	// 按标识反查
	byIdent, err := s.ByIdentifier(ctx, platform.LoginMailCode, "999000003@sicau.edu.cn")
	if err != nil || byIdent.UserID != u.ID {
		t.Fatalf("ByIdentifier: %+v err=%v", byIdent, err)
	}

	// 按 (user, kind) 查
	byUser, err := s.ByUserAndKind(ctx, u.ID, platform.LoginMailCode)
	if err != nil || byUser.ID != m.ID {
		t.Fatalf("ByUserAndKind: %+v err=%v", byUser, err)
	}

	// 同一 (kind, identifier) 绑给另一个用户 → ErrAlreadyExists
	u2, _ := s.Create(ctx, "999000001", platform.RoleUser)
	if _, err := s.Bind(ctx, platform.LoginMethod{
		UserID: u2.ID, Kind: platform.LoginMailCode, Identifier: "999000003@sicau.edu.cn",
	}); !platform.IsAlreadyExists(err) {
		t.Fatalf("重复标识应返回 ErrAlreadyExists，实际 %v", err)
	}

	// 凭据可写入与清除
	if err := s.UpdateSecret(ctx, u.ID, platform.LoginMailCode, []byte("sealed-pw")); err != nil {
		t.Fatalf("UpdateSecret: %v", err)
	}
	got, _ := s.ByUserAndKind(ctx, u.ID, platform.LoginMailCode)
	if string(got.SecretSealed) != "sealed-pw" {
		t.Fatalf("凭据未写入：%q", got.SecretSealed)
	}
	if err := s.UpdateSecret(ctx, u.ID, platform.LoginMailCode, nil); err != nil {
		t.Fatalf("清除凭据: %v", err)
	}
	got, _ = s.ByUserAndKind(ctx, u.ID, platform.LoginMailCode)
	if got.SecretSealed != nil {
		t.Fatalf("凭据应被清空，实际 %q", got.SecretSealed)
	}

	// 解绑
	if err := s.Unbind(ctx, u.ID, platform.LoginMailCode); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if _, err := s.ByUserAndKind(ctx, u.ID, platform.LoginMailCode); !platform.IsNotFound(err) {
		t.Fatalf("解绑后应 ErrNotFound，实际 %v", err)
	}
}

func TestSessionStore_CreateRevokeAndSingleDevice(t *testing.T) {
	s := newIdentityStore(t)
	ctx := context.Background()
	u, err := s.Create(ctx, "999000002", platform.RoleUser)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	sid := fmt.Sprintf("sess-test-%d", time.Now().UnixNano())
	sess := platform.Session{
		ID:        sid,
		UserID:    u.ID,
		Scope:     platform.ScopeApp,
		DeviceID:  "device-A",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := s.SessionByID(ctx, sid)
	if err != nil || got.UserID != u.ID || got.Scope != platform.ScopeApp {
		t.Fatalf("SessionByID: %+v err=%v", got, err)
	}
	if !got.Active(time.Now()) {
		t.Fatal("新会话应为活跃")
	}

	// 单设备顶替：吊销该用户 app 下全部会话
	n, err := s.RevokeByUserScope(ctx, u.ID, platform.ScopeApp)
	if err != nil {
		t.Fatalf("RevokeByUserScope: %v", err)
	}
	if n < 1 {
		t.Fatalf("应至少吊销 1 条，实际 %d", n)
	}
	got, _ = s.SessionByID(ctx, sid)
	if got.Active(time.Now()) {
		t.Fatal("吊销后会话不应再活跃")
	}

	// 幂等：再吊销不报错、影响 0 条
	n2, err := s.RevokeByUserScope(ctx, u.ID, platform.ScopeApp)
	if err != nil || n2 != 0 {
		t.Fatalf("重复吊销应幂等且影响 0 条，实际 n=%d err=%v", n2, err)
	}

	// 不存在的会话 → ErrNotFound
	if _, err := s.SessionByID(ctx, "no-such-session"); !platform.IsNotFound(err) {
		t.Fatalf("不存在会话应 ErrNotFound，实际 %v", err)
	}
}
