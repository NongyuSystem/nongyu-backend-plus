package objectstore_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/platform/objectstore"
)

// 本测试连真实 MinIO；未配 MINIO_ENDPOINT 时 SKIP（CI 无 MinIO 也能跑）。
//
// 环境变量：
//
//	MINIO_ENDPOINT   形如 127.0.0.1:9000（不带 scheme）
//	MINIO_ACCESS_KEY
//	MINIO_SECRET_KEY
//	MINIO_BUCKET（默认 nongyu-test）
//	MINIO_USE_SSL（"true" 时走 https）
func newTestStore(t *testing.T) *objectstore.Store {
	t.Helper()
	endpoint := os.Getenv("MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("未配置 MINIO_ENDPOINT，跳过真实 MinIO 测试")
	}
	bucket := os.Getenv("MINIO_BUCKET")
	if bucket == "" {
		bucket = "nongyu-test"
	}
	s, err := objectstore.New(context.Background(), objectstore.Options{
		Endpoint:  endpoint,
		AccessKey: os.Getenv("MINIO_ACCESS_KEY"),
		SecretKey: os.Getenv("MINIO_SECRET_KEY"),
		Bucket:    bucket,
		UseSSL:    os.Getenv("MINIO_USE_SSL") == "true",
	})
	if err != nil {
		t.Fatalf("构造 MinIO 客户端失败：%v", err)
	}
	return s
}

// TestMinIO_PutStatDeleteRoundTrip 覆盖四个方法的主路径。
func TestMinIO_PutStatDeleteRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	key := "test/" + time.Now().Format("20060102150405.000000000") + ".txt"
	body := "hello minio"

	info, err := s.Put(ctx, key, strings.NewReader(body), int64(len(body)), "text/plain")
	if err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if info.Key != key {
		t.Fatalf("返回的 key 不符：%q", info.Key)
	}
	if info.Size != int64(len(body)) {
		t.Fatalf("size 不符：%d", info.Size)
	}

	st, err := s.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat 失败：%v", err)
	}
	if st.Size != int64(len(body)) {
		t.Fatalf("Stat 的 size 不符：%d", st.Size)
	}

	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	// 删完再 Stat 应为「不存在」
	if _, err := s.Stat(ctx, key); err != platform.ErrObjectNotFound {
		t.Fatalf("删除后 Stat 应返回 ErrObjectNotFound，得到 %v", err)
	}
}

// TestMinIO_DeleteMissingReturnsNotFound 锁住「不存在要能区分」这条接口约定。
//
// MinIO 的 RemoveObject 对不存在的对象也返回 nil（S3 幂等语义），
// 若直接透传，调用方无法区分「删掉了」与「本来就没有」。
func TestMinIO_DeleteMissingReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	err := s.Delete(ctx, "test/definitely-not-exists-"+time.Now().Format("20060102150405"))
	if err != platform.ErrObjectNotFound {
		t.Fatalf("删除不存在的对象应返回 ErrObjectNotFound，得到 %v", err)
	}
}

// TestMinIO_PresignGetProducesURL 只验证能签出 URL 且含签名参数。
//
// 注意：**不校验 URL 真能下载**——那需要发 HTTP 请求，且 MinIO 的
// 「预签名是否真不校验存储侧身份」尚未在本仓实测
// （见 docs/tech/决策-MinIO接入落点.md 的未验证假设）。
func TestMinIO_PresignGetProducesURL(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	key := "test/presign-" + time.Now().Format("20060102150405.000000000") + ".txt"
	body := "presign me"
	if _, err := s.Put(ctx, key, strings.NewReader(body), int64(len(body)), "text/plain"); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	t.Cleanup(func() { _ = s.Delete(ctx, key) })

	u, err := s.PresignGet(ctx, key, 5*time.Minute)
	if err != nil {
		t.Fatalf("预签名失败：%v", err)
	}
	if !strings.Contains(u, "X-Amz-Signature") {
		t.Fatalf("URL 应含签名参数，得到 %q", u)
	}
	if !strings.Contains(u, "X-Amz-Expires") {
		t.Fatalf("URL 应含过期参数，得到 %q", u)
	}
}

// TestNew_RejectsMissingConfig 锁住「配置缺失要在构造期失败」。
func TestNew_RejectsMissingConfig(t *testing.T) {
	ctx := context.Background()
	// 缺 Endpoint
	if _, err := objectstore.New(ctx, objectstore.Options{Bucket: "b"}); err == nil {
		t.Fatal("缺 Endpoint 应报错")
	}
	// 缺 Bucket
	if _, err := objectstore.New(ctx, objectstore.Options{Endpoint: "127.0.0.1:9000"}); err == nil {
		t.Fatal("缺 Bucket 应报错")
	}
	// 带 scheme 的 Endpoint 是常见配置错误，也应报错
	if _, err := objectstore.New(ctx, objectstore.Options{
		Endpoint: "http://127.0.0.1:9000", Bucket: "b",
	}); err == nil {
		t.Fatal("Endpoint 带 scheme 应报错（minio-go 约定不带 scheme）")
	}
}
