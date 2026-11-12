// Package objectstore 是 ObjectStore 接缝的 MinIO 实现。
//
// **全仓唯一允许 import S3 SDK 的包**（README 架构约束第 2、4 条 +
// docs/tech/决策-MinIO接入落点.md）。业务域只认 platform.ObjectStore 的
// 四个方法，不该知道底层是 MinIO 还是别的；把 SDK 收在这一层，
// 将来换实现（S3、OSS、本地磁盘）只需替换本包。
//
// 为什么单独子包而不是塞进 platform 根包：platform 根包已经被大量业务代码
// import，若它直接依赖 minio-go，SDK 的传递依赖会进入每一个业务包。
// 单独子包让「谁在用 SDK」一目了然，也让 go.mod 里的依赖边界清晰。
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"nongyu-backend-plus/internal/platform"
)

// Options 是构造 MinIO 客户端的配置。
type Options struct {
	// Endpoint 形如 "127.0.0.1:9000"（**不带 scheme**，minio-go 的约定）。
	// 带 scheme 会导致 SDK 把整个字符串当主机名，报 DNS 解析失败。
	Endpoint string
	// AccessKey / SecretKey 是凭据。
	AccessKey string
	SecretKey string
	// Bucket 是桶名。单桶 + 前缀分区（posts/…），不按域分桶——
	// 分桶会带来跨桶权限与配额管理成本，而前缀已经够隔离。
	Bucket string
	// UseSSL 为真时走 https。
	UseSSL bool
	// Region 可空（MinIO 会自报）。
	Region string
}

// Store 是 platform.ObjectStore 的 MinIO 实现。
type Store struct {
	client *minio.Client
	bucket string
}

// 编译期确认：本实现满足接缝。
var _ platform.ObjectStore = (*Store)(nil)

// New 构造 MinIO 实现。
//
// 构造期只做客户端创建与**桶存在性检查**，不做网络写入；
// 桶不存在时**直接返回错误而不是自动创建**：自动建桶会让「配置写错桶名」
// 变成「悄悄多了一个空桶」，运维很难发现。桶应由部署时的初始化步骤创建。
func New(ctx context.Context, opts Options) (*Store, error) {
	if opts.Endpoint == "" {
		return nil, errors.New("objectstore: Endpoint 必填（形如 127.0.0.1:9000，不带 scheme）")
	}
	if opts.Bucket == "" {
		return nil, errors.New("objectstore: Bucket 必填")
	}
	// 早失败：带 scheme 的 Endpoint 是常见配置错误，SDK 的报错很难懂。
	if _, err := url.Parse("//" + opts.Endpoint); err != nil {
		return nil, fmt.Errorf("objectstore: Endpoint 格式非法：%w", err)
	}

	client, err := minio.New(opts.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(opts.AccessKey, opts.SecretKey, ""),
		Secure: opts.UseSSL,
		Region: opts.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("objectstore: MinIO 客户端创建失败：%w", err)
	}

	// 桶存在性检查：连不上或桶不存在都拒绝启动，避免「服务起来了但上传全失败」。
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	exists, err := client.BucketExists(checkCtx, opts.Bucket)
	if err != nil {
		return nil, fmt.Errorf("objectstore: 无法访问 MinIO（%s）：%w", opts.Endpoint, err)
	}
	if !exists {
		return nil, fmt.Errorf("objectstore: 桶 %q 不存在，请先创建（本实现不自动建桶）", opts.Bucket)
	}

	return &Store{client: client, bucket: opts.Bucket}, nil
}

// Put 写入一个对象。
//
// size 会传给 SDK 作为对象大小：它同时是**长度校验**——SDK 若发现实际
// 读到的字节数与声明不符会报错，这正是我们要的「声称 5MB 实际 50MB」防护。
func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (platform.ObjectInfo, error) {
	info, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return platform.ObjectInfo{}, fmt.Errorf("objectstore: 写入 %q 失败：%w", key, err)
	}
	return platform.ObjectInfo{
		Key:         info.Key,
		Size:        info.Size,
		ContentType: contentType,
		ETag:        info.ETag,
	}, nil
}

// PresignGet 生成限时只读 URL。
//
// **未在本仓对 MinIO 实测**：预签名行为依据 AWS S3 文档与 minio-go 的实现，
// 但「MinIO 是否真的不校验存储侧身份」这一点属未验证假设
// （见 docs/tech/决策-MinIO接入落点.md）。接入真实 MinIO 前应实测一次。
func (s *Store) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := s.client.PresignedGetObject(ctx, s.bucket, key, ttl, nil)
	if err != nil {
		// MinIO 对不存在的对象预签名不报错（签名不校验存在性），
		// 所以这里只可能是配置/网络问题。
		return "", fmt.Errorf("objectstore: 预签名 %q 失败：%w", key, err)
	}
	return u.String(), nil
}

// Delete 删除一个对象。
//
// MinIO 的 RemoveObject **对不存在的对象也返回 nil**（S3 语义是幂等删除）。
// 但接口约定「不存在返回 ErrObjectNotFound」，所以这里先 Stat 再删——
// 多一次往返，换来调用方可以区分「删掉了」与「本来就没有」。
// 附件清理是旁路，这点开销可接受。
func (s *Store) Delete(ctx context.Context, key string) error {
	if _, err := s.Stat(ctx, key); err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("objectstore: 删除 %q 失败：%w", key, err)
	}
	return nil
}

// Stat 读取对象元信息。
func (s *Store) Stat(ctx context.Context, key string) (platform.ObjectInfo, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return platform.ObjectInfo{}, platform.ErrObjectNotFound
		}
		return platform.ObjectInfo{}, fmt.Errorf("objectstore: 读取 %q 元信息失败：%w", key, err)
	}
	return platform.ObjectInfo{
		Key:         info.Key,
		Size:        info.Size,
		ContentType: info.ContentType,
		ETag:        info.ETag,
	}, nil
}

// Health 探测 MinIO 可达性（供 /status 使用）。
func (s *Store) Health(ctx context.Context) error {
	_, err := s.client.BucketExists(ctx, s.bucket)
	return err
}
