package platform

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// ErrObjectNotFound 表示对象不存在（或已被删除）。
//
// 单独给出哨兵错误，是为了让 ObjectStore 的实现（MinIO、内存假件）与调用方
// 对「不存在」有同一判据，而不是各自字符串比较错误信息。
var ErrObjectNotFound = errors.New("platform: 对象不存在")

// ObjectInfo 是一个对象的最小元信息。
//
// 只放调用方真正需要的四个字段。刻意不暴露 S3 的完整头部集合：
// 业务域不该知道自己跑在什么存储上（README 架构约束第 2 条）。
type ObjectInfo struct {
	Key         string
	Size        int64
	ContentType string
	ETag        string
}

// ObjectStore 是对象存储的外部能力接缝。
//
// 为什么接口方法只有四个（来自 map #43 子票 #53 的决议，见
// docs/tech/决策-MinIO接入落点.md）：
//   - 本 effort 只做「帖子图片附件」，读取一律走短 TTL 预签名，
//     没有分片上传、没有服务端复制、没有列目录的需求；
//   - 不提前塞 List/Copy/分片，避免接口变成没人用的负债。
//
// 注意这里**没有 PresignPut**：上传走后端中转（客户端 → 本服务 → 对象存储），
// 因为只有流量过服务端，「单张 5MB + 四种格式」才是后端可执行的硬约束，
// 而不是靠客户端自律或桶策略。将来若真要做直传，再加方法。
//
// 实现必须放在 platform 的子包里（如 platform/objectstore），并且那个子包是
// **全仓唯一允许 import S3 SDK 的地方**。
type ObjectStore interface {
	// Put 写入一个对象。r 从当前位置读到 EOF 为止；size 为声明的字节数，
	// 实现可用它做长度校验（与 r 实际长度不一致时应报错）。
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (ObjectInfo, error)

	// PresignGet 生成一个限时只读 URL。ttl 由调用方决定（短 TTL 是刻意的：
	// 已签发的 URL 在过期前无法回收，TTL 越短，内容软删后的残留窗口越小）。
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)

	// Delete 删除一个对象。对象本就不存在时返回 ErrObjectNotFound。
	Delete(ctx context.Context, key string) error

	// Stat 读取对象元信息。不存在时返回 ErrObjectNotFound。
	Stat(ctx context.Context, key string) (ObjectInfo, error)
}

// MemoryObjectStore 是 ObjectStore 的内存实现，供测试与骨架期使用。
//
// 它不是 MinIO 的等价物：不做真实签名、不做网络 IO，只保证四个方法的
// 「契约形状」正确，让上层逻辑（体积/格式校验、预签名拼接、软删清理）
// 能在不启动对象存储的情况下被测试。真实的 MinIO 语义属 Spec 的
// 「需实测」层，见 docs/specs/spec-六类常见功能.md 的 Further Notes。
type MemoryObjectStore struct {
	mu      sync.RWMutex
	objects map[string]memoryObject

	// puts/deletes 是调用计数，供测试断言「是否真的写/删过」，
	// 避免测试去读私有字段。
	puts    int
	deletes int
}

type memoryObject struct {
	body        []byte
	contentType string
	etag        string
}

// NewMemoryObjectStore 构造一个空的内存对象存储。
//
// 刻意返回指针：`mu sync.RWMutex` 使值拷贝不安全（go vet 会报 copylocks）。
func NewMemoryObjectStore() *MemoryObjectStore {
	return &MemoryObjectStore{objects: map[string]memoryObject{}}
}

// Put 实现 ObjectStore。
func (m *MemoryObjectStore) Put(_ context.Context, key string, r io.Reader, size int64, contentType string) (ObjectInfo, error) {
	if key == "" {
		return ObjectInfo{}, errors.New("platform: 对象 key 不能为空")
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return ObjectInfo{}, err
	}
	// 声明的 size 与实际读到的不一致时拒绝：上传路径依赖这一点来保证
	// 「声称 5MB 就真的是 5MB」，否则限额可被绕过。
	if size >= 0 && int64(len(body)) != size {
		return ObjectInfo{}, errors.New("platform: 对象长度与声明不符")
	}
	etag := memoryETag(body)

	m.mu.Lock()
	m.puts++
	m.objects[key] = memoryObject{body: body, contentType: contentType, etag: etag}
	m.mu.Unlock()

	return ObjectInfo{Key: key, Size: int64(len(body)), ContentType: contentType, ETag: etag}, nil
}

// PresignGet 实现 ObjectStore。内存实现给不出真签名，返回可预测的伪 URL。
func (m *MemoryObjectStore) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	m.mu.RLock()
	_, ok := m.objects[key]
	m.mu.RUnlock()
	if !ok {
		return "", ErrObjectNotFound
	}
	return "memory://" + key + "?ttl=" + ttl.String(), nil
}

// Delete 实现 ObjectStore。
func (m *MemoryObjectStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objects[key]; !ok {
		return ErrObjectNotFound
	}
	m.deletes++
	delete(m.objects, key)
	return nil
}

// Stat 实现 ObjectStore。
func (m *MemoryObjectStore) Stat(_ context.Context, key string) (ObjectInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	o, ok := m.objects[key]
	if !ok {
		return ObjectInfo{}, ErrObjectNotFound
	}
	return ObjectInfo{Key: key, Size: int64(len(o.body)), ContentType: o.contentType, ETag: o.etag}, nil
}

// PutCount 返回累计 Put 次数（测试用）。
func (m *MemoryObjectStore) PutCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.puts
}

// DeleteCount 返回累计成功 Delete 次数（测试用）。
func (m *MemoryObjectStore) DeleteCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.deletes
}

// memoryETag 用 body 长度与首尾字节拼一个稳定的伪 etag。
//
// 不用真哈希是为了避免为假件引入 crypto 依赖与额外开销；测试只需要
// 「同一内容 etag 相同、不同内容 etag 不同」这一级别。
func memoryETag(body []byte) string {
	n := len(body)
	var a, b byte
	if n > 0 {
		a, b = body[0], body[n-1]
	}
	return "mem-" + string(rune('0'+n%10)) + "-" + string(rune('a'+a%26)) + string(rune('a'+b%26))
}

// 编译期确认：内存实现满足接口。
var _ ObjectStore = (*MemoryObjectStore)(nil)
