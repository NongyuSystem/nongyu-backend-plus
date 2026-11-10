package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// ---- from http_doer.go ----
// 本文件是 HTTPDoer 的**真实**实现：真的用 net/http 发外部请求，带出站闸口策略。
//
// 与 caps_mock.go 里的 MockHTTPDoer 是两条完全不同的路径：
//   - MockHTTPDoer：骨架期内存实现，不发网络请求，默认返回 200 空响应；Enforce=true 时
//     才在「内存里」模拟白名单/302/Set-Cookie 策略，供 #11 契约测试与上层单测隔离用。
//   - RealHTTPDoer（本文件）：生产实现，发真实网络请求；下面所有约束由**本实现**
//     无条件强制，不依赖任何 Enforce 开关。
//
// 命名刻意不带 Mock 前缀（M9）：看装配层注入的是哪个类型，就知道当前是否真的出站。

const (
	defaultDialTimeout    = 5 * time.Second  // Transport 层：连接 + TLS 握手
	defaultOverallTimeout = 10 * time.Second // client.Timeout：整次请求（含读响应体）
	defaultMaxBodyBytes   = 2 << 20          // 2 MiB 响应体上限
)

// RealHTTPDoerOptions 是 NewRealHTTPDoer 的可选配置。零值即可用（自带安全默认值）。
// Client / Transport 用于测试注入，注入后仍会强制补上 CheckRedirect 与白名单校验。
type RealHTTPDoerOptions struct {
	// AllowedHosts 出站目标白名单（主机名）。校验用 req.URL.Hostname()，
	// 复用 caps_mock.go 的 hostInAllowlist：大小写不敏感、忽略端口。
	AllowedHosts []string
	// Timeout 整次请求超时（client.Timeout）。<=0 回落 defaultOverallTimeout。
	Timeout time.Duration
	// DialTimeout 连接 + TLS 握手超时。<=0 回落 defaultDialTimeout。
	DialTimeout time.Duration
	// MaxBodyBytes 响应体读取上限。<=0 回落 defaultMaxBodyBytes(2 MiB)。
	MaxBodyBytes int64

	// Client 注入自定义 *http.Client；为 nil 时用本实现自建的 client。
	// 注入时本实现会**浅拷贝**一份（不改动调用方持有的对象），并强制覆盖
	// CheckRedirect 与 Timeout，白名单校验仍在 Do 入口处无条件执行。
	Client *http.Client
	// Transport 注入自定义 http.RoundTripper（Client 为 nil 时生效）。
	// 为 nil 时用本实现自建的、不复用 http.DefaultTransport 的 *http.Transport。
	Transport http.RoundTripper
}

// RealHTTPDoer 是发真实网络请求的 HTTPDoer 实现。
type RealHTTPDoer struct {
	client       *http.Client
	allowedHosts []string
	maxBodyBytes int64
}

// NewRealHTTPDoer 按 opts 构造真实出站实现。
func NewRealHTTPDoer(opts RealHTTPDoerOptions) *RealHTTPDoer {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultOverallTimeout
	}
	dialTimeout := opts.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = defaultDialTimeout
	}
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultMaxBodyBytes
	}

	var client *http.Client
	if opts.Client != nil {
		// 浅拷贝：既要拿到注入的 Transport/Jar，又不能原地改写调用方的 client。
		cp := *opts.Client
		client = &cp
	} else {
		client = &http.Client{Transport: opts.Transport}
	}

	// 自建 Transport（不复用 http.DefaultTransport）：显式施加连接/TLS 握手超时。
	// 只有在调用方完全没提供 Transport 时才套这层默认值；注入的 Transport 原样尊重。
	if client.Transport == nil {
		client.Transport = newDefaultTransport(dialTimeout)
	}

	// 无条件强制：不自动跟随重定向。ErrUseLastResponse 让 client 把 3xx 原样交回，
	// 调用方才能看见中间跳的 Set-Cookie（教务三跳建会话，ASPSESSIONID 在中间跳下发，
	// 见 internal/school/schoolauth/doer_contract.go 与 jiaowu.go:286 的 resp.Cookies()）。
	// 同时覆盖注入 client 自带的 CheckRedirect，杜绝被配置弱化。
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client.Timeout = timeout

	return &RealHTTPDoer{
		client:       client,
		allowedHosts: append([]string(nil), opts.AllowedHosts...),
		maxBodyBytes: maxBody,
	}
}

// newDefaultTransport 构造不复用 http.DefaultTransport 的 Transport，带连接/TLS 握手超时。
func newDefaultTransport(dialTimeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: dialTimeout,
		ForceAttemptHTTP2:     true,
	}
}

// Do 实现 HTTPDoer：出站请求的唯一闸口。
//
// 约束（全部由本实现无条件强制）：
//  1. 白名单：req.URL.Hostname() 不在 AllowedHosts → ErrTargetDenied，零出站（不建 tcp）；
//  2. 不跟随重定向：见 NewRealHTTPDoer 的 CheckRedirect；
//  3. 超时：Transport 连接/TLS 超时 + client.Timeout 整体超时 + context.WithTimeout 施加；
//  4. 响应体上限：io.LimitReader 包装 resp.Body；
//  5. ctx 取消：透传给底层 client，调用方可 cancel 在途请求；
//  6. 不剥离 Set-Cookie：见下方长注释。
//
// 关于 ctx 与超时 cancel 的坑（重要）：client.Timeout 与 context.WithTimeout 都会把
// resp.Body 的读取绑定到同一个 ctx。**不能用 `defer cancel()`**：Do 一返回，defer 立刻
// 触发 cancel，调用方随后读分块响应正文会得到 `context canceled`。因此 cancel 被挂到
// 包装 resp.Body 的 ReadCloser.Close() 里（幂等），只在「请求出错」分支立即 cancel。
// 这样 Body 的生命周期成为 ctx 的生命周期，读完/关闭才真正释放超时资源。
func (d *RealHTTPDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req == nil || req.URL == nil {
		return nil, errors.New("platform: nil *http.Request")
	}

	// 出站前白名单校验：复用 caps_mock.go 的判定（大小写不敏感、忽略端口）。
	// 放在建连之前，保证非白名单目标「零出站」——连 tcp 连接都不会建立。
	if !hostInAllowlist(req.URL.Hostname(), d.allowedHosts) {
		return nil, ErrTargetDenied
	}

	// context.WithTimeout 让调用方的 ctx 与整体超时共同约束这次请求；
	// cancel 不在这里 defer，而是随 resp.Body.Close() 释放（见上方长注释）。
	reqCtx, cancel := context.WithTimeout(ctx, d.client.Timeout)
	servedReq := req.WithContext(reqCtx)

	resp, err := d.client.Do(servedReq)
	if err != nil {
		cancel() // 错误分支立即释放：没有 Body 会接管 cancel。
		return nil, err
	}

	// 成功分支：把 cancel 与「响应体上限」一起包进 resp.Body 的 ReadCloser。
	// 调用方 Close() 时关闭真实 resp.Body（归还连接到连接池）并幂等地 cancel 掉超时 ctx。
	// 读用 io.LimitReader 截断，但 Close 必须落到真实 body 上——不能用 io.NopCloser，
	// 否则底层连接不会被归还，长跑下会泄漏连接。
	resp.Body = &limitBody{
		reader: io.LimitReader(resp.Body, d.maxBodyBytes+1),
		max:    d.maxBodyBytes,
		closer: resp.Body,
		cancel: cancel,
	}
	return resp, nil
}

// limitBody 把三件事绑到响应体的生命周期上：
//   - Read：经 io.LimitReader 截断到上界字节；
//   - Close：关闭**真实** resp.Body（归还连接），并幂等地 cancel 掉整体超时 ctx。
//
// 把 cancel 放在这里而不是 `defer cancel()`，是为了避开分块响应在 Do 返回后才被读取、
// 却被提前 cancel 掉导致 `context canceled` 的坑（见 Do 上方长注释）。
type limitBody struct {
	reader io.Reader
	max    int64
	read   int64
	closer io.Closer
	cancel context.CancelFunc
	once   sync.Once
}

// Read 累计已读字节；一旦超过上限立即返回可识别错误，
// 绝不把截断内容当完整响应静默交给调用方（评审 M4，口径同 schoolauth/http.go）。
func (b *limitBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += int64(n)
	if b.max > 0 && b.read > b.max {
		return n, fmt.Errorf("platform: 响应体超过上限 %d 字节", b.max)
	}
	return n, err
}

func (b *limitBody) Close() error {
	var err error
	b.once.Do(func() {
		err = b.closer.Close()
		b.cancel()
	})
	return err
}

// ---------------------------------------------------------------------------
// 关于「为什么不剥离 Set-Cookie」——与 MockHTTPDoer(Enforce=true) 的**有意差异**
// ---------------------------------------------------------------------------
//
// MockHTTPDoer 在 Enforce=true 时会执行 resp.Header.Del("Set-Cookie")，其语义来自
// ADR-0001 第 5 条「不回显非必要 Set-Cookie」。但那条约束约束的是**外部 HTTP API
// 边界**：不要把上游 cookie 透传给「农屿后端自己的外部调用方」（客户端/第三方）。
// 这条边界由业务域的 handler 落地（见 internal/school/jiaowu/handler.go:88 在回写
// 给客户端时再次兜底剥掉 Set-Cookie），而不是由平台层的出站组件落地。
//
// 本 RealHTTPDoer 是**内部组件**，它的调用方是 schoolauth / jiaowu 等业务子域，这些
// 子域恰恰需要拿到上游的 Set-Cookie：教务建会话是三跳重定向，ASPSESSIONID 在中间跳
// 的 302 上下发，业务代码通过 resp.Cookies() 收集后用于后续请求（硬证据见
// internal/school/schoolauth/jiaowu.go:286 的 `for _, c := range resp.Cookies()`，
// 以及 doer_contract.go 记录的「自动跟随会丢中间跳 cookie」实测坑）。如果这里剥离，
// 教务取数会从第一步就误判「未下发会话 cookie」而失败。
//
// 因此：
//   - 本实现**不剥离** resp.Header 的 Set-Cookie，也不做任何额外透传；Set-Cookie 原样
//     留在返回给内部业务域的 *http.Response 上，由业务域决定如何使用；
//   - 外部 API 边界的「不回显 Set-Cookie」由 handler 层负责（它本就有兜底），与本组件正交；
//   - ADR-0001 第 5 条的「3xx 指向真站立即失败」也是调用方职责：本 Doer 必须把 3xx
//     原样交回（三跳建会话要读中间跳的 Location/Set-Cookie），由 jiaowu.Service.Fetch
//     把它转成 CodeBlockedRedirect / CodeSessionExpired。骨架期由 Mock 在 Doer 层代做，
//     换真实 Doer 后该责任上移，勿再依赖本组件拦截 3xx。
//   - 这与 MockHTTPDoer(Enforce=true) 的 resp.Header.Del("Set-Cookie") 是**有意不同**，
//     不是遗漏。Mock 是内存模拟，没有下游业务域需要收 cookie；真实实现必须保留。
// ---------------------------------------------------------------------------

// ---- from capabilities.go ----
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
// 实现层强制：目标白名单校验、超时、不跟随指向真站的 302、剥离上游 Set-Cookie
// （ADR-0001 / 架构落点草图 §3、§7、§8 的「全系统唯一强制点」）。
// 守卫逻辑落在 platform 的 HTTPDoer 实现里，由装配层注入同一个实例给所有业务子域；
// 业务域只依赖本接口，不得绕过它直接用 http.DefaultClient 发请求。
//
// 实现层以哨兵错误表达拒绝原因，业务域用 errors.Is 映射为各自的统一错误码。
type HTTPDoer interface {
	// Do 执行一个已构造好的请求。请求需携带 ctx，以便实现层施加超时/取消。
	Do(ctx context.Context, req *http.Request) (*http.Response, error)
}

// 出站闸口的哨兵错误：由 platform 实现层产出，业务域据此映射统一错误码。
var (
	// ErrTargetDenied 请求目标不在白名单（含上游 302 指向真站被拦下）。
	ErrTargetDenied = errors.New("platform: outbound target denied")
	// ErrBlockedRedirect 上游 3xx 的 Location 指向真站，禁止跟随。
	ErrBlockedRedirect = errors.New("platform: redirect to origin blocked")
	// ErrSessionExpired 上游 3xx 指向网关登录页，业务会话已过期。
	ErrSessionExpired = errors.New("platform: upstream session expired")
)

// Clock 表达「取当前时间」这一外部依赖，便于测试注入固定时刻。
type Clock interface {
	Now() time.Time
}
