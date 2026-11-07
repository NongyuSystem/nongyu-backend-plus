package platform

import (
	"context"
	"net/http"
	"time"
)

// 本文件是「外部能力适配层」的 4 个接缝，与 store.go 里的 Store 同属一层。
// 铁律：这里只描述「进程能对外做什么」，不含任何业务语义（不出现 ticket 的业务含义、
// 不出现 WebVPN/CAS/教务等词、不暴露密钥）。实现放本包的 mock/子包，由 app 装配层注入。
//
// 业务域只依赖这些接口；换实现（内存 → 数据库 / 真实 HTTP / 真实时钟）时业务域零改动。

// TicketStore 表达「按用户维度存放一份不透明字节，并带一个可选过期时刻」的外部存储能力。
// 这里不解释 sealed 的内容，也不解释过期时刻的业务含义——那是业务域的事。
// Get 在 userID 不存在时返回 ErrNotFound（复用 store.go 的哨兵错误）。
type TicketStore interface {
	// Put 写入/覆盖一条记录。expiresAt 为零值表示「会话级、无明确过期时刻」。
	Put(ctx context.Context, userID string, sealed []byte, expiresAt time.Time) error
	// Get 读取一条记录；不存在时返回 ErrNotFound。
	Get(ctx context.Context, userID string) (sealed []byte, expiresAt time.Time, err error)
	// Delete 删除一条记录；不存在时按幂等处理（返回 nil）。
	Delete(ctx context.Context, userID string) error
	// ListExpiringSoon 返回「过期时刻严格早于 before」的候选，按过期时刻升序，最多 limit 条。
	// 零值过期时刻的记录不参与候选（无明确过期即无法判定「即将过期」）。
	// limit <= 0 时返回空列表。
	ListExpiringSoon(ctx context.Context, before time.Time, limit int) ([]TicketRef, error)
}

// TicketRef 是 ListExpiringSoon 的返回元素：只带定位与排序所需的最小信息，
// 不把 sealed 内容暴露给调度/扫描侧。
type TicketRef struct {
	UserID    string
	ExpiresAt time.Time
}

// Cipher 表达「把明文字节封成不透明字节、再还原」的对称加解密能力。
// 密钥来自配置并在构造实现时注入，接口本身不暴露密钥、也不暴露算法。
type Cipher interface {
	// Seal 把 plaintext 封装成可存储/下发的字节。
	Seal(plaintext []byte) ([]byte, error)
	// Open 还原由 Seal 产出的字节；密钥不符、格式非法或被篡改时返回错误。
	Open(sealed []byte) ([]byte, error)
}

// HTTPDoer 是「后端有没有权发外部请求」的唯一闸口。
//
// 实现层负责：目标白名单校验、超时、以及不跟随指向真站的 302
// （ADR-0001 / CodeBlockedRedirect 的唯一强制点）。本票只铺接口与基础实现，
// 完整 302 策略留后续实现票；策略形状由 DoerPolicy 预留（见 caps_mock.go）。
// 业务域不得绕过本接口直接用 http.DefaultClient 发请求。
type HTTPDoer interface {
	// Do 执行一个已构造好的请求。请求需携带 ctx，以便实现层施加超时/取消。
	Do(ctx context.Context, req *http.Request) (*http.Response, error)
}

// Clock 表达「取当前时间」这一外部依赖，便于测试注入固定时刻。
type Clock interface {
	Now() time.Time
}
