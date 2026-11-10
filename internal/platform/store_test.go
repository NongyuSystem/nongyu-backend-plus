package platform_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/platform"
)

// ---- from store_test.go ----
// ---- from pg_identity_test.go ----
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
		Name:          "张三",
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

// ---- from testschema_test.go ----
func TestTestSchemaName_DeterministicAndValid(t *testing.T) {
	a := platform.TestSchemaName("nongyu-backend-plus/internal/identity")
	b := platform.TestSchemaName("nongyu-backend-plus/internal/identity")
	if a != b {
		t.Fatalf("同包名应得到同名 schema：%q vs %q", a, b)
	}
	if len(a) > 63 {
		t.Fatalf("schema 名不得超过 63：%q (%d)", a, len(a))
	}
	if strings.ContainsAny(a, "/-.") {
		t.Fatalf("schema 名不得含路径符号：%q", a)
	}
	if !strings.HasPrefix(a, "t_") {
		t.Fatalf("应以 t_ 开头：%q", a)
	}

	// 不同包必须得到不同 schema——这是隔离的前提。
	other := platform.TestSchemaName("nongyu-backend-plus/internal/identity/flow")
	if other == a {
		t.Fatalf("不同包应得到不同 schema：%q", a)
	}
}

func TestWithSearchPath(t *testing.T) {
	got, err := platform.WithSearchPath("postgres://u:p@h:5432/db?sslmode=disable", "t_x")
	if err != nil {
		t.Fatalf("WithSearchPath: %v", err)
	}
	if !strings.Contains(got, "search_path=t_x") && !strings.Contains(got, "search_path=t%5Fx") {
		t.Fatalf("应带上 search_path，实际 %q", got)
	}
	if !strings.Contains(got, "sslmode=disable") {
		t.Fatalf("原有参数不应丢失，实际 %q", got)
	}
}

// 真的建 schema 并验证隔离：同一条学号在两个 schema 里互不冲突。
func TestEnsureTestSchema_IsolatesData(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过 Postgres 集成测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dsnA, err := platform.EnsureTestSchema(ctx, dsn, "nongyu-backend-plus/testpkg/a")
	if err != nil {
		t.Fatalf("EnsureTestSchema(a): %v", err)
	}
	dsnB, err := platform.EnsureTestSchema(ctx, dsn, "nongyu-backend-plus/testpkg/b")
	if err != nil {
		t.Fatalf("EnsureTestSchema(b): %v", err)
	}
	if dsnA == dsnB {
		t.Fatal("两个包的 DSN 必须不同（隔离的前提）")
	}

	storeA, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsnA})
	if err != nil {
		t.Fatalf("storeA: %v", err)
	}
	defer storeA.Close()
	storeB, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsnB})
	if err != nil {
		t.Fatalf("storeB: %v", err)
	}
	defer storeB.Close()

	const no = "555000001"
	// 先在 A 里删掉可能残留
	if u, err := storeA.ByStudentNo(ctx, no); err == nil {
		_ = storeA.SetStatus(ctx, u.ID, platform.UserActive)
	}
	if u, err := storeB.ByStudentNo(ctx, no); err == nil {
		_ = storeB.SetStatus(ctx, u.ID, platform.UserActive)
	}

	// 两个 schema 各建一条同学号用户，不应冲突。
	if _, err := storeA.Create(ctx, no, platform.RoleUser); err != nil && !platform.IsAlreadyExists(err) {
		t.Fatalf("storeA.Create: %v", err)
	}
	if _, err := storeB.Create(ctx, no, platform.RoleUser); err != nil && !platform.IsAlreadyExists(err) {
		t.Fatalf("storeB.Create（同学号不应冲突）: %v", err)
	}

	// 在 A 里删除，不应影响 B。
	ua, err := storeA.ByStudentNo(ctx, no)
	if err != nil {
		t.Fatalf("storeA.ByStudentNo: %v", err)
	}
	if err := storeA.DeleteUser(ctx, ua.ID); err != nil {
		t.Fatalf("storeA.DeleteUser: %v", err)
	}
	if _, err := storeA.ByStudentNo(ctx, no); !platform.IsNotFound(err) {
		t.Fatalf("A 里应已删除，实际 %v", err)
	}
	if _, err := storeB.ByStudentNo(ctx, no); err != nil {
		t.Fatalf("B 里的数据不应被 A 的删除影响，实际 %v", err)
	}
}

// ---- from testschema_consistency_test.go ----
// issue #23：三份 DDL 的一致性守护。
//
// 背景：仓库里有三份「建表形状」——
//   ① migrations/0001_audit_log.sql（权威，运维手工执行）
//   ② migrations/0002_identity.sql（权威，被 //go:embed 嵌入二进制）
//   ③ internal/platform/testschema_ddl.go 的 identityDDLStatements()（测试专用手写）
//
// ①② 是同一份权威源，本票已用 embed 让程序实际执行它们。③ 是测试隔离机制
// （每个包独立 schema）用的手写副本，票要求「不要改测试隔离机制」，故本测试
// **不改**它，而是把它产生的 schema 与权威迁移产生的 schema 做结构比对：
//   - 表集合、列集合、列类型、可空性、默认值、主键/唯一/检查/外键约束必须一致；
//   - 二级索引（CREATE INDEX）刻意排除：③ 一个都没建，而 ①② 建了 6 个，
//     这是**已知且被接受**的差异——索引只影响查询性能，不影响仓储行为正确性，
//     测试隔离 schema 不需要它们。报告里会明确写出这个差异。
//
// 这样将来有人改了迁移的列/约束却忘了同步手写副本（或反之），测试立刻变红，
// 不会让漂移悄悄扩大。

// parseAndStripSearchPath 去掉 DSN 里的 search_path，得到「原始库级 DSN」。
// TestMain 会往 DATABASE_URL 里塞本包隔离 schema，这里需要不带它的根 DSN
// 才能另建两个比对用的 schema。
func parseAndStripSearchPath(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("解析 DSN 失败：%w", err)
	}
	q := u.Query()
	q.Del("search_path")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// seqDefaultRe 匹配 nextval('<schema>.<seq>'::regclass) 里的 schema 前缀。
var seqDefaultRe = regexp.MustCompile(`'[^']*\.([a-zA-Z0-9_]+_seq)'::regclass`)

func normalizeDefault(def string) string {
	return seqDefaultRe.ReplaceAllString(def, `'$1'::regclass`)
}

// schemaQualifierRe 匹配 FK 定义里 REFERENCES <schema>.<table> 的 schema 前缀。
var schemaQualifierRe = regexp.MustCompile(`REFERENCES [a-zA-Z0-9_]+\.`)

func normalizeConstraint(def string) string {
	return schemaQualifierRe.ReplaceAllString(def, "REFERENCES ")
}

// schemaColumns 读一个 schema 里全部「表.列」的形状签名。
func schemaColumns(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT table_name, column_name, data_type, is_nullable,
		       COALESCE(column_default, ''), COALESCE(character_maximum_length::text, '')
		  FROM information_schema.columns
		 WHERE table_schema = $1
		 ORDER BY table_name, ordinal_position`, schema)
	if err != nil {
		t.Fatalf("查 %s 的列失败：%v", schema, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tbl, col, typ, nullable, def, maxlen string
		if err := rows.Scan(&tbl, &col, &typ, &nullable, &def, &maxlen); err != nil {
			t.Fatalf("扫列失败：%v", err)
		}
		// bigserial 的默认值是 nextval('<schema>.<seq>'::regclass)，里面带 schema 名，
		// 两个比对 schema 必然不同。把「任意 schema 前缀」归一掉，只留序列本身，
		// 否则会把「同名序列」误报成列定义不一致。
		def = normalizeDefault(def)
		out = append(out, strings.Join([]string{tbl, col, typ, nullable, def, maxlen}, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历列失败：%v", err)
	}
	return out
}

// schemaConstraints 读主键/唯一/检查/外键约束（不含索引），按名字排序。
func schemaConstraints(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT c.relname, con.conname, con.contype, pg_get_constraintdef(con.oid)
		  FROM pg_constraint con
		  JOIN pg_class c ON c.oid = con.conrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = $1 AND con.contype IN ('p','u','c','f')
		 ORDER BY c.relname, con.conname`, schema)
	if err != nil {
		t.Fatalf("查 %s 的约束失败：%v", schema, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var rel, name, typ, def string
		if err := rows.Scan(&rel, &name, &typ, &def); err != nil {
			t.Fatalf("扫约束失败：%v", err)
		}
		out = append(out, fmt.Sprintf("%s|%s|%s|%s", rel, name, typ, normalizeConstraint(def)))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历约束失败：%v", err)
	}
	sort.Strings(out)
	return out
}

// TestTestSchemaDDL_MatchesMigrationsStructurally 断言手写测试 DDL 与权威迁移
// 在「表 + 列形状 + 约束」上一致（索引差异见文件头说明，刻意排除）。
func TestTestSchemaDDL_MatchesMigrationsStructurally(t *testing.T) {
	base := os.Getenv("DATABASE_URL")
	if base == "" {
		t.Skip("未设置 DATABASE_URL，跳过 DDL 一致性集成测试")
	}
	// 本包 TestMain 已把 DATABASE_URL 改写为带 search_path 的隔离 DSN，
	// 这里需要**不带 search_path 的原始 DSN** 才能另建 schema。
	u, err := parseAndStripSearchPath(base)
	if err != nil {
		t.Fatalf("解析 DSN：%v", err)
	}
	root := u

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, root)
	if err != nil {
		t.Fatalf("建管理连接失败：%v", err)
	}
	defer pool.Close()

	const migSchema = "t_ddl_cmp_migrations"
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+migSchema+" CASCADE"); err != nil {
		t.Fatalf("清理 schema %s：%v", migSchema, err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+migSchema); err != nil {
		t.Fatalf("建 schema %s：%v", migSchema, err)
	}
	t.Cleanup(func() {
		c, cc := context.WithTimeout(context.Background(), 10*time.Second)
		defer cc()
		_, _ = pool.Exec(c, "DROP SCHEMA IF EXISTS "+migSchema+" CASCADE")
		_, _ = pool.Exec(c, "DROP SCHEMA IF EXISTS "+platform.TestSchemaName("nongyu-backend-plus/testpkg/ddlcompare")+" CASCADE")
	})

	sep := "?"
	if strings.Contains(root, "?") {
		sep = "&"
	}
	migDSN := root + sep + "search_path=" + migSchema
	if err := platform.Migrate(ctx, migDSN); err != nil {
		t.Fatalf("在比对 schema 上执行权威迁移失败：%v", err)
	}
	// 测试隔离机制：EnsureTestSchema 派生自己的 schema 名，这里用固定包名，
	// 但它会自己 CREATE SCHEMA；为可比，先删掉它即将用的那个名字再调。
	pkgPath := "nongyu-backend-plus/testpkg/ddlcompare"
	derived := platform.TestSchemaName(pkgPath)
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+derived+" CASCADE"); err != nil {
		t.Fatalf("清理派生 schema：%v", err)
	}
	testDSN, err := platform.EnsureTestSchema(ctx, root, pkgPath)
	if err != nil {
		t.Fatalf("EnsureTestSchema（手写测试 DDL）失败：%v", err)
	}
	if !strings.Contains(testDSN, derived) {
		t.Fatalf("EnsureTestSchema 应使用派生 schema %q，实际 DSN %q", derived, testDSN)
	}

	gotMigCols := schemaColumns(t, ctx, pool, migSchema)
	gotTestCols := schemaColumns(t, ctx, pool, derived)
	if strings.Join(gotMigCols, "\n") != strings.Join(gotTestCols, "\n") {
		t.Fatalf("手写测试 DDL 与权威迁移的【列形状】不一致：\n--- migrations ---\n%s\n--- testschema ---\n%s",
			strings.Join(gotMigCols, "\n"), strings.Join(gotTestCols, "\n"))
	}

	gotMigCons := schemaConstraints(t, ctx, pool, migSchema)
	gotTestCons := schemaConstraints(t, ctx, pool, derived)
	if strings.Join(gotMigCons, "\n") != strings.Join(gotTestCons, "\n") {
		t.Fatalf("手写测试 DDL 与权威迁移的【约束】不一致：\n--- migrations ---\n%s\n--- testschema ---\n%s",
			strings.Join(gotMigCons, "\n"), strings.Join(gotTestCons, "\n"))
	}

	// 显式钉住「已知差异」：权威迁移额外建 6 个二级索引（0001 三个 + 0002 三个），
	// 测试 DDL 一个都没建。只数「不属于任何约束」的索引，排除 PK/UNIQUE 自带索引，
	// 才是可读的 6 vs 0。索引只影响查询性能、不影响仓储行为正确性，测试隔离
	// schema 不需要它们，故为被接受的差异；数量变化只提示复核，不判失败。
	countSecondary := func(s string) int {
		var n int
		err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_indexes i
			 WHERE i.schemaname = $1
			   AND NOT EXISTS (
			     SELECT 1 FROM pg_constraint con
			      WHERE con.conname = i.indexname
			        AND con.connamespace = (SELECT oid FROM pg_namespace WHERE nspname = $1)
			   )`, s).Scan(&n)
		if err != nil {
			t.Fatalf("数 %s 的二级索引失败：%v", s, err)
		}
		return n
	}
	migIdx, testIdx := countSecondary(migSchema), countSecondary(derived)
	if migIdx != 6 || testIdx != 0 {
		t.Logf("提示：二级索引数量变化（migrations=%d, testschema=%d，基线 6/0），请复核报告里的「已知差异」结论", migIdx, testIdx)
	}
}

// ---- from testmain_test.go ----
// TestMain 把 DATABASE_URL 指向本包专属 schema，避免与其它包的集成测试互相干扰。
// 详见 internal/identity/testmain_test.go 的说明。

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if isolated, err := platform.EnsureTestSchema(ctx, dsn, "nongyu-backend-plus/internal/platform"); err == nil {
			_ = os.Setenv("DATABASE_URL", isolated)
		}
		cancel()
	}
	os.Exit(m.Run())
}

// ---- from migrate_test.go ----
// issue #23 / Spec 验收第 3 条（迁移可执行且幂等）的验收测试。
//
// seam：公共能力 platform.Migrate(ctx, dsn)。测试只通过这个入口驱动「迁移」这一
// 外部能力，不读取 migrations/ 文件、不调用内部函数——实现换成版本表也能存活。
//
// 期望值来自独立事实源：migrations/ 下两份 SQL 里的**表名字面量**（0001 audit_log /
// 0002 users、user_profiles、login_methods、sessions），不是从被实现代码里重算出来的。
// 观察通道是 information_schema（迁移的全部语义就是「库里多了这些表」），
// 用 pgxpool 直连查询，与被实现的 Migrate 内部如何拼 SQL 无关。
//
// 环境：TestMain 会把 DATABASE_URL 改写为本包专属 schema（testmain_test.go），
// 因此这里的「空库」是「本包独立 schema 里的空表集」，与其它包并行互不干扰。
// 未设置 DATABASE_URL 时跳过（纯单测环境不依赖 Postgres）。

// migrationTables 是 migrations/*.sql 里声明的全部表名（独立事实源：SQL 字面量）。
var migrationTables = []string{
	"audit_log",
	"login_methods",
	"sessions",
	// 0004：学工系统（xsgz）完整档案，唯一数据源对应的四张表
	"student_family_members",
	"student_profile_groups",
	"student_profiles",
	"student_resumes",
	"user_profiles",
	"users",
}

// migrationDSN 返回本包隔离后的 DSN（TestMain 已改写 DATABASE_URL）。
func migrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过 Postgres 迁移集成测试")
	}
	return dsn
}

// migrationSchema 从隔离 DSN 的 search_path 参数读出本包的 schema 名。
func migrationSchema(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("解析 DSN 失败：%v", err)
	}
	s := u.Query().Get("search_path")
	if s == "" {
		t.Fatalf("隔离 DSN 应带 search_path，实际 %q", dsn)
	}
	return s
}

// migrationPool 建一条独立的直连（与被实现的 Migrate 内部连接池无关）。
func migrationPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("建直连失败：%v", err)
	}
	return pool
}

// dropMigrationTables 清空本包 schema 里的迁移表，模拟「空库」。
// 用 CASCADE 一次带走依赖对象；这些表只由本包测试使用，删干净是安全且必要的。
func dropMigrationTables(t *testing.T, dsn string) {
	t.Helper()
	pool := migrationPool(t, dsn)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, name := range migrationTables {
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+name+" CASCADE"); err != nil {
			t.Fatalf("清理表 %s 失败：%v", name, err)
		}
	}
}

// existingMigrationTables 返回当前 schema 里已存在的迁移表名（升序）。
// 走 information_schema 系统目录，是被测迁移行为的可观察结果，不依赖实现内部。
func existingMigrationTables(t *testing.T, dsn, schema string) []string {
	t.Helper()
	pool := migrationPool(t, dsn)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := pool.Query(ctx,
		`SELECT table_name FROM information_schema.tables
		  WHERE table_schema = $1 AND table_type = 'BASE TABLE'`, schema)
	if err != nil {
		t.Fatalf("查 information_schema 失败：%v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("扫表名失败：%v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表名失败：%v", err)
	}
	sort.Strings(got)
	return got
}

// TestMigrate_BuildsTablesOnEmptySchema 是验收第 3 条的前半：
// 在空库（本包独立 schema、迁移表全不存在）上执行一次 Migrate，
// migrations/ 里声明的全部表必须被建出来。
func TestMigrate_BuildsTablesOnEmptySchema(t *testing.T) {
	dsn := migrationDSN(t)
	schema := migrationSchema(t, dsn)
	dropMigrationTables(t, dsn)

	if before := existingMigrationTables(t, dsn, schema); len(before) != 0 {
		t.Fatalf("前置条件失败：空库不应有迁移表，实际 %v", before)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := platform.Migrate(ctx, dsn); err != nil {
		t.Fatalf("空库首次 Migrate 应成功：%v", err)
	}

	got := existingMigrationTables(t, dsn, schema)
	for _, want := range migrationTables {
		if !contains(got, want) {
			t.Fatalf("Migrate 后应存在表 %q，实际表集 %v", want, got)
		}
	}
}

// TestMigrate_IdempotentOnSecondRun 是验收第 3 条的后半：
// 连续执行第二次 Migrate 不报错，且表集不变——「部署不必手工敲 SQL」的前提。
// 还在两次执行之间写入一条数据，确认幂等重跑不会把已有数据抹掉。
func TestMigrate_IdempotentOnSecondRun(t *testing.T) {
	dsn := migrationDSN(t)
	schema := migrationSchema(t, dsn)
	dropMigrationTables(t, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := platform.Migrate(ctx, dsn); err != nil {
		t.Fatalf("首次 Migrate：%v", err)
	}

	// 在两次迁移之间放一条用户数据：幂等重跑不得丢数据。
	store, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsn})
	if err != nil {
		t.Fatalf("建连接失败：%v", err)
	}
	const studentNo = "620230001"
	if _, err := store.Create(ctx, studentNo, platform.RoleUser); err != nil {
		t.Fatalf("写入测试用户失败：%v", err)
	}
	// 用例结束删掉这条数据，不给同 schema 的其它测试留残留（表本身保留，
	// 它们与 EnsureTestSchema 建的形态一致，后续测试可继续用）。
	t.Cleanup(func() {
		s, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsn})
		if err != nil {
			return
		}
		defer s.Close()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if u, err := s.ByStudentNo(c, studentNo); err == nil {
			_ = s.DeleteUser(c, u.ID)
		}
	})
	_ = store.Close()

	// 第二次执行：不报错。
	if err := platform.Migrate(ctx, dsn); err != nil {
		t.Fatalf("第二次 Migrate 应幂等不报错：%v", err)
	}

	got := existingMigrationTables(t, dsn, schema)
	if strings.Join(got, ",") != strings.Join(migrationTables, ",") {
		t.Fatalf("幂等重跑后表集应与首次一致，期望 %v，实际 %v", migrationTables, got)
	}

	// 数据仍在：确认幂等不是靠「删表重建」实现的。
	store2, err := platform.NewPGIdentityStore(platform.PGOptions{DSN: dsn})
	if err != nil {
		t.Fatalf("建复查连接失败：%v", err)
	}
	defer store2.Close()
	if _, err := store2.ByStudentNo(ctx, studentNo); err != nil {
		t.Fatalf("幂等重跑后用户应仍存在：%v", err)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
