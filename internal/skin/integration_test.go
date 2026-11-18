// Package skin_test 的真实环境集成测试。
//
// 与其它包的 PG 集成测试同风格：未设置 DATABASE_URL / MINIO_ENDPOINT 时
// t.Skip，纯单测环境（CI 无 PG/MinIO）不依赖它们。
//
// 覆盖用户明确要求的组合：**真 MinIO 存包体 + 真 Postgres 存元数据/全局单指针**。
// 用独立 schema + 迁移建表（与生产同源），包体写到真实 bucket 后用唯一 packId
// 并在 t.Cleanup 里删掉，不污染既有数据。
package skin_test

import (
	"context"
	"errors"
	skinstore "nongyu-backend-plus/internal/skin/store"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/platform/objectstore"
	pkgx "nongyu-backend-plus/internal/platform/pgx"
	"nongyu-backend-plus/internal/skin"
)

// newSkinPGPool 建一个指向独立 schema 的连接池，并在其中执行全部迁移。
// 未配 DATABASE_URL 时 skip；库不可达时 skip（不让环境问题变成假红）。
func newSkinPGPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("DATABASE_URL")
	if base == "" {
		t.Skip("未设置 DATABASE_URL，跳过 Postgres 集成测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Skipf("Postgres 不可达，跳过：%v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("Postgres 不可达，跳过：%v", err)
	}

	schema := "t_skin_" + skinTestSchemaName(t.Name())
	if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoteIdent(schema)+" CASCADE"); err != nil {
		admin.Close()
		t.Fatalf("清理旧 schema %s：%v", schema, err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoteIdent(schema)); err != nil {
		admin.Close()
		t.Fatalf("建 schema %s：%v", schema, err)
	}

	migDSN, err := pkgx.WithSearchPath(base, schema)
	if err != nil {
		admin.Close()
		t.Fatalf("拼迁移 DSN：%v", err)
	}
	// 在隔离 schema 上跑权威迁移（含本域的 0007_skins.sql），不手写 DDL。
	if err := pkgx.Migrate(ctx, migDSN); err != nil {
		admin.Close()
		t.Fatalf("在 schema %s 上执行迁移：%v", schema, err)
	}

	pool, err := pgxpool.New(ctx, migDSN)
	if err != nil {
		admin.Close()
		t.Fatalf("建测试连接池：%v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		c, cc := context.WithTimeout(context.Background(), 15*time.Second)
		defer cc()
		_, _ = admin.Exec(c, "DROP SCHEMA IF EXISTS "+quoteIdent(schema)+" CASCADE")
		admin.Close()
	})
	return pool
}

// newSkinMinioStore 连真实 MinIO。未配 MINIO_ENDPOINT 时 skip；
// bucket 不存在时 skip 并给出可操作提示（本仓 MinIO 实现刻意不自动建桶）。
func newSkinMinioStore(t *testing.T) *objectstore.Store {
	t.Helper()
	endpoint := os.Getenv("MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("未设置 MINIO_ENDPOINT，跳过 MinIO 集成测试")
	}
	bucket := os.Getenv("MINIO_BUCKET")
	if bucket == "" {
		bucket = "nongyu"
	}
	s, err := objectstore.New(context.Background(), objectstore.Options{
		Endpoint:  endpoint,
		AccessKey: os.Getenv("MINIO_ACCESS_KEY"),
		SecretKey: os.Getenv("MINIO_SECRET_KEY"),
		Bucket:    bucket,
		UseSSL:    os.Getenv("MINIO_USE_SSL") == "true",
	})
	if err != nil {
		t.Skipf("MinIO 不可用（%s，bucket=%s），跳过：%v", endpoint, bucket, err)
	}
	return s
}

func skinTestSchemaName(name string) string {
	lower := strings.ToLower(name)
	var b strings.Builder
	for _, r := range lower {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := b.String()
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// TestIntegration_RealMinioAndPostgres_FullRoundTrip 是「真 MinIO + 真 Postgres」
// 的端到端验收：上传 → 目录 → 下载字节与 sha256 校验 → 激活/回滚语义。
func TestIntegration_RealMinioAndPostgres_FullRoundTrip(t *testing.T) {
	pool := newSkinPGPool(t)
	store := newSkinMinioStore(t)
	ctx := context.Background()

	// 唯一 packId，避免与真实 bucket 里的既有对象冲突；Cleanup 用上传返回的
	// 实际 ObjectKey 删除（key 含随机段，不能靠 packId/version 再推导一次）。
	packID := "e2e-skin-" + time.Now().Format("20060102150405")
	var objKey string
	t.Cleanup(func() {
		if objKey != "" {
			_ = store.Delete(context.Background(), objKey)
		}
	})

	entries := []testEntry{{path: "icons/home.png", body: []byte("e2e-png-payload")}}
	manifest := buildManifest(packID, entries, nil)
	raw := packWith(t, manifest, zipFile{name: "icons/home.png", body: []byte("e2e-png-payload")})

	svc := skin.New(skin.Options{
		Repo:        skinstore.NewSkinPGRepo(pool, time.Now),
		ObjectStore: store,
	})

	// 1) 上传并立即激活
	p, err := svc.Upload(ctx, skin.UploadInput{
		PackBytes: raw, Version: "1.0.0", CreatedBy: "e2e-admin", Activate: true,
	})
	if err != nil {
		t.Fatalf("真实上传失败：%v", err)
	}
	objKey = p.ObjectKey

	// 2) 目录接口指向它
	cat, err := svc.Catalog(ctx, skin.CatalogBasePath)
	if err != nil {
		t.Fatalf("目录读取失败：%v", err)
	}
	if !cat.HasPack || cat.PackID != packID || cat.Version != "1.0.0" {
		t.Fatalf("目录不符：%+v", cat)
	}
	if cat.PackSHA256 != skin.Sha256Hex(raw) {
		t.Fatalf("目录 packSha256 不符")
	}

	// 3) 从真实 MinIO 下载，字节与整包 sha256 必须一致
	meta, rc, err := svc.Download(ctx, packID, "1.0.0")
	if err != nil {
		t.Fatalf("真实下载失败：%v", err)
	}
	defer rc.Close()
	got, err := readAll(rc)
	if err != nil {
		t.Fatalf("读下载流失败：%v", err)
	}
	if skin.Sha256Hex(got) != cat.PackSHA256 || len(got) != int(meta.PackSize) {
		t.Fatalf("下载字节与上传不一致（size=%d sha=%s）", len(got), skin.Sha256Hex(got))
	}

	// 4) 版本不可变：重复上传同版本 → conflict
	if _, err := svc.Upload(ctx, skin.UploadInput{
		PackBytes: raw, Version: "1.0.0",
	}); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("重复同版本应返回 conflict，实际 %v", err)
	}

	// 5) PG 指针与外键语义：激活不存在的版本 → not_found
	if err := svc.Activate(ctx, packID, "9.9.9"); err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("激活不存在版本应 not_found，实际 %v", err)
	}

	// 6) Repo 层直接核对 PG 落库
	repo := skinstore.NewSkinPGRepo(pool, time.Now)
	p2, err := repo.GetByRef(ctx, packID, "1.0.0")
	if err != nil {
		t.Fatalf("PG GetByRef 失败：%v", err)
	}
	if p2.ObjectKey != objKey || p2.PackSHA256 != cat.PackSHA256 {
		t.Fatalf("PG 落库字段不符：%+v", p2)
	}
	cur, err := repo.Current(ctx)
	if err != nil || cur.PackID != packID {
		t.Fatalf("PG Current 不符：%+v err=%v", cur, err)
	}

	// 7) 对象删除后：元数据在、对象缺失 → Download 映射 404（而不是 500）
	if err := store.Delete(ctx, objKey); err != nil {
		t.Fatalf("清理对象失败：%v", err)
	}
	_, _, err = svc.Download(ctx, packID, "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("对象缺失应映射 not_found，实际 %v", err)
	}
	if errors.Is(err, platform.ErrObjectNotFound) {
		t.Fatal("不应把对象存储哨兵错误直接泄漏给上层（应映射成领域错误）")
	}
}
