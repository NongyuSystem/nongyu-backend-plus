package platform

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// 本文件是 4 个外部能力接口的骨架期内存/Mock 实现，沿用 mock.go 的风格：
// 零外部依赖、可被 app 装配层注入、可被测试替换。数据库 / 真实 HTTP / 真实时钟
// 就绪后，新增实现同一接口的类型并在装配层替换即可，业务域零改动。
//
// 注意：实现放在 platform 包内（与接口同包）是骨架期约定；业务域只 import 接口，
// 不得 import 或类型断言到这些具体类型。

// ---------------------------------------------------------------------------
// MockTicketStore
// ---------------------------------------------------------------------------

type ticketRecord struct {
	sealed    []byte
	expiresAt time.Time
}

// MockTicketStore 是 TicketStore 的内存实现：sync.RWMutex + map，进程内有效。
// 进程无状态是长期约束——本实现只用于骨架期联调/测试，生产必须换成外置存储。
type MockTicketStore struct {
	mu   sync.RWMutex
	data map[string]ticketRecord
}

// NewMockTicketStore 构造一个空的票据存储。
func NewMockTicketStore() *MockTicketStore {
	return &MockTicketStore{data: make(map[string]ticketRecord)}
}

// Put 实现 TicketStore。写入时复制 sealed，避免调用方后续改动污染存储。
func (m *MockTicketStore) Put(_ context.Context, userID string, sealed []byte, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[userID] = ticketRecord{
		sealed:    append([]byte(nil), sealed...),
		expiresAt: expiresAt,
	}
	return nil
}

// Get 实现 TicketStore；不存在时返回 ErrNotFound。返回 sealed 的副本。
func (m *MockTicketStore) Get(_ context.Context, userID string) ([]byte, time.Time, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.data[userID]
	if !ok {
		return nil, time.Time{}, ErrNotFound
	}
	return append([]byte(nil), rec.sealed...), rec.expiresAt, nil
}

// Delete 实现 TicketStore；不存在时按幂等处理。
func (m *MockTicketStore) Delete(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, userID)
	return nil
}

// ListExpiringSoon 实现 TicketStore：
// 只挑「有明确过期时刻且严格早于 before」的记录，按过期时刻升序（同刻按 UserID 稳定排序），
// 最后按 limit 截断。零值过期（会话级）不参与，limit <= 0 直接返回空列表。
func (m *MockTicketStore) ListExpiringSoon(_ context.Context, before time.Time, limit int) ([]TicketRef, error) {
	if limit <= 0 {
		return []TicketRef{}, nil
	}
	m.mu.RLock()
	refs := make([]TicketRef, 0)
	for userID, rec := range m.data {
		if rec.expiresAt.IsZero() || !rec.expiresAt.Before(before) {
			continue
		}
		refs = append(refs, TicketRef{UserID: userID, ExpiresAt: rec.expiresAt})
	}
	m.mu.RUnlock()

	sort.Slice(refs, func(i, j int) bool {
		if refs[i].ExpiresAt.Equal(refs[j].ExpiresAt) {
			return refs[i].UserID < refs[j].UserID
		}
		return refs[i].ExpiresAt.Before(refs[j].ExpiresAt)
	})
	if len(refs) > limit {
		refs = refs[:limit]
	}
	return refs, nil
}

// ---------------------------------------------------------------------------
// MockCipher（生产级 AES-256-GCM 实现）
// ---------------------------------------------------------------------------

const (
	// 格式：[magic(4) | nonce(12) | GCM 密文+tag(n)]
	aeadMagic   = "NGY2"
	aeadHeadLen = 4 + 12 // magic + GCM 标准 nonce 长度
	aeadKeyLen  = 32     // AES-256
	// 开发占位密钥（与 config.go 的 placeholderTicketKey 同值）。
	// 只用来「识别并拒绝」显式传入的弱密钥，**绝不再作为加密密钥回落到它**。
	// 历史实现：空密钥 → 回落到这个源码里公开的字符串，任何读源码的人都能
	// 用同一常量构造 Cipher 解密 tgt / 身份证密文。现改为弱密钥一律换成本进程
	// 随机临时密钥（见 ephemeralCipherKey），并 WARN 提示配置缺失。
	defaultCipherKey = "dev-only-placeholder-key-change-me"
)

var errInvalidSealed = errors.New("platform: 无效的 sealed 数据（格式错误、密钥不符或已被篡改）")

// MockCipher 是 Cipher 的实现：AES-256-GCM 认证加密。
// 名字沿用骨架期类型名以免改动装配层与既有调用方，但算法已是生产级 AEAD。
//
// AESCipher 是 MockCipher 的**真实实现名**：本类型实为生产级 AES-256-GCM，
// 不是内存桩。命名误导来自骨架期，改名会波及大量既有调用与测试，故用类型别名
// 提供一个一眼可辨的真名，装配层新代码应优先用 NewAESCipher。
// 这是 #21 / Spec M9 的处置：命名可区分，且零破坏。
type AESCipher = MockCipher

// NewAESCipher 构造真实 AES-256-GCM 加密器（与 NewMockCipher 同一实现）。
func NewAESCipher(key string) *AESCipher { return NewMockCipher(key) }

// 密钥来自配置注入，接口本身不暴露密钥；每次 Seal 用 crypto/rand 生成新 nonce，
// 因此同一明文两次封装产出不同密文。密文自带认证标签，篡改/错误密钥/截断一律拒绝。
type MockCipher struct {
	key []byte
}

// ephemeralCipherKey 是「缺配置时」的本进程随机临时密钥。
//
// 为什么是随机而不是源码常量：历史实现把空密钥回落到 defaultCipherKey（一个
// 公开在源码里的字符串），攻击者只要读源码就能构造同样的 Cipher 解密 tgt /
// 身份证号密文。改成 crypto/rand 生成的进程级随机密钥后：
//   - 同一进程内多个空密钥 Cipher 共享它 → 既有「空密钥能往返」的契约不破；
//   - 但外部任何人（含读源码者）无法预知/重建它 → 公开常量不再能解密；
//   - 进程重启即变 → 用弱密钥落库的密文在重启后解不开，这是**刻意**的
//     fail-closed：逼迫部署方显式配置真实 TICKET_KEY，而不是依赖隐蔽默认值。
//
// 装配层/配置层仍应把「prod 必须用强密钥」作为硬校验（config.Load 已做），
// 本层只负责「即使配置漏了，也不退化成全世界都知道的密钥」。
var ephemeralCipherKey = func() []byte {
	b := make([]byte, aeadKeyLen)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败属极端异常；不回落到任何可预测值，直接拒绝启动更安全。
		panic("platform: 无法生成临时 Cipher 密钥：" + err.Error())
	}
	return b
}()

// NewMockCipher 用配置里的密钥构造：SHA-256 派生出固定 32 字节 AES 密钥。
//
// 空密钥 / 仅由占位常量或空基串派生的弱密钥一律不回落到公开常量，改用
// sha256(本进程随机盐 || key) 派生并 WARN。这样：
//   - 外部攻击者即便读源码、知道 key 字符串（"" / 占位值 / ":idcard" 等），
//     也因不知随机盐而无法派生出同一把 AES 密钥，解不开 tgt / 身份证密文；
//   - 同一进程内「同一个弱 key 字符串」仍派生出同一把密钥，Seal/Open 往返不破
//     （不破坏既有单测与骨架装配：如 school/jiaowu.New 的 NewMockCipher("")）；
//   - 不同弱 key 之间仍保持域分隔（":tgt" 与 ":idcard" 密钥不同），
//     app.go 的派生用法语义不变；
//   - 进程重启盐变化 → 弱密钥落库密文重启后不可解，这是刻意的 fail-closed，
//     逼迫部署方显式配置真实 TICKET_KEY，而不是依赖隐蔽默认值；
//   - 生产强密钥路径完全不受影响（weakCipherKey 返回 false 走原 sha256(key)）。
func NewMockCipher(key string) *MockCipher {
	if weakCipherKey(key) {
		log.Printf("WARN platform: Cipher 收到空/占位密钥，已改用本进程随机盐派生临时密钥；请显式配置 TICKET_KEY（缺配置时密文在进程重启后不可解）")
		buf := make([]byte, 0, len(ephemeralCipherKey)+len(key))
		buf = append(buf, ephemeralCipherKey...)
		buf = append(buf, key...)
		sum := sha256.Sum256(buf)
		return &MockCipher{key: sum[:]}
	}
	sum := sha256.Sum256([]byte(key))
	return &MockCipher{key: sum[:]}
}

// weakCipherKey 判断是否为「绝不能拿来派生真实密钥」的弱输入：
//   - 空 / 纯空白；
//   - 等于公开的 defaultCipherKey 本身；
//   - 以 defaultCipherKey 为前缀再加域分隔派生（如 "<占位值>:tgt"、
//     "<占位值>:idcard"），这些是 app.go 在 TicketKey 为占位值时拼出的形态；
//   - 以 ":" 开头的域派生串（app.go 在 TicketKey 为空时拼出的 ":tgt"、
//     ":idcard"、":password"）——基串为空，同样可被任何人照源码复现。
//
// prod 由 config.Load 拦住，但 staging/dev 或漏配场景仍会走到这里，必须一并挡掉。
func weakCipherKey(key string) bool {
	if strings.TrimSpace(key) == "" {
		return true
	}
	if key == defaultCipherKey || strings.HasPrefix(key, defaultCipherKey+":") {
		return true
	}
	return strings.HasPrefix(key, ":")
}

// Seal 实现 Cipher：随机 nonce + AES-GCM 封装，返回 [magic|nonce|ciphertext+tag]。
func (c *MockCipher) Seal(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("platform: 生成 nonce 失败：%w", err)
	}
	out := make([]byte, 0, aeadHeadLen+len(plaintext)+gcm.Overhead())
	out = append(out, aeadMagic...)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, []byte(aeadMagic))
	return out, nil
}

// Open 实现 Cipher：校验格式/密钥/完整性后还原明文。
func (c *MockCipher) Open(sealed []byte) ([]byte, error) {
	if len(sealed) < aeadHeadLen {
		return nil, errInvalidSealed
	}
	if string(sealed[:4]) != aeadMagic {
		return nil, errInvalidSealed
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := sealed[4:aeadHeadLen]
	ct := sealed[aeadHeadLen:]
	plain, err := gcm.Open(nil, nonce, ct, []byte(aeadMagic))
	if err != nil {
		return nil, errInvalidSealed
	}
	return plain, nil
}

// ---------------------------------------------------------------------------
// MockHTTPDoer
// ---------------------------------------------------------------------------

// DoerPolicy 是出站闸口的策略，由 platform 实现层（唯一强制点）执行：
//   - Enforce：是否启用强制。零值 = 骨架期 mock 行为（只记录/返回可配置响应），
//     用于 #11 的契约测试；装配层显式置 true 后，白名单/超时/302/Set-Cookie 全部生效。
//   - AllowedHosts：目标白名单；业务域只能传逻辑目标名，由实现层解析校验。
//   - Timeout：单次请求超时（>0 时用 context.WithTimeout 施加）。
//
// 注意：不提供「允许跟随重定向」开关。ADR-0001 第 5 条的安全不变量在 Enforce=true
// 时由实现层**无条件拦截** 3xx 保证（任何 Location 都不跟随），该行为不可被配置弱化。
type DoerPolicy struct {
	Enforce      bool
	AllowedHosts []string
	Timeout      time.Duration
}

// RecordedRequest 是一次 Do 调用的可观察快照，供测试断言「收到了什么请求」，
// 而不是断言内部调用顺序。
type RecordedRequest struct {
	Method string
	Host   string
	Path   string
	URL    string
}

// Responder 决定 MockHTTPDoer 对一次请求返回什么；默认返回 200 空响应。
type Responder func(*http.Request) (*http.Response, error)

// MockHTTPDoer 是 HTTPDoer 的骨架期内存实现：记录收到的请求，返回可配置的响应。
// 它不发起真实网络请求，也不执行白名单/302 策略（留后续实现票）。
type MockHTTPDoer struct {
	mu        sync.Mutex
	policy    DoerPolicy
	responder Responder
	recorded  []RecordedRequest
}

// NewMockHTTPDoer 用给定策略构造；零值策略也有安全默认响应（200）。
func NewMockHTTPDoer(policy DoerPolicy) *MockHTTPDoer {
	return &MockHTTPDoer{policy: clonePolicy(policy)}
}

// SetResponse 配置「固定状态码 + 固定响应体」的响应。
func (m *MockHTTPDoer) SetResponse(status int, body string) {
	m.SetResponder(func(req *http.Request) (*http.Response, error) {
		return newMockResponse(req, status, body), nil
	})
}

// SetResponder 配置自定义响应函数；返回的错误会原样透传给调用方。
func (m *MockHTTPDoer) SetResponder(fn Responder) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responder = fn
}

// Requests 返回已记录请求的快照（副本），避免调用方与内部切片共享内存。
func (m *MockHTTPDoer) Requests() []RecordedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]RecordedRequest(nil), m.recorded...)
}

// Policy 返回策略快照（含 AllowedHosts 副本）。
func (m *MockHTTPDoer) Policy() DoerPolicy {
	m.mu.Lock()
	defer m.mu.Unlock()
	return clonePolicy(m.policy)
}

// Do 实现 HTTPDoer：这是出站请求的唯一闸口。
//
// Enforce=false（骨架期契约测试/零值策略）：只记录请求 + 返回可配置响应，
// 行为与 #11 一致，便于契约测试与上层单测隔离。
//
// Enforce=true（装配层显式启用）：依次强制
//  1. 白名单校验：req.URL.Hostname() 不在 AllowedHosts → ErrTargetDenied，零出站；
//  2. 超时：Timeout > 0 时 context.WithTimeout 施加到交给 responder 的请求；
//  3. 不跟随指向真站的 3xx：Location 非网关域 → ErrBlockedRedirect；Location 指向
//     网关登录页 → ErrSessionExpired；两种都先关闭 resp.Body 再返回（N1）；
//  4. 上游 Set-Cookie 一律剥离，不回显给调用方（ADR-0001 第 5 条）。
//
// 所有拒绝都恰好只触达 responder 一次（或零次），绝不静默重试。
func (m *MockHTTPDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if req == nil || req.URL == nil {
		return nil, errors.New("platform: nil *http.Request")
	}

	m.mu.Lock()
	pol := clonePolicy(m.policy)
	m.mu.Unlock()

	if pol.Enforce && !hostInAllowlist(req.URL.Hostname(), pol.AllowedHosts) {
		return nil, ErrTargetDenied
	}

	snapshot := RecordedRequest{
		Method: req.Method,
		Host:   req.Host,
		Path:   req.URL.Path,
		URL:    req.URL.String(),
	}
	m.mu.Lock()
	m.recorded = append(m.recorded, snapshot)
	fn := m.responder
	m.mu.Unlock()

	servedReq := req
	if ctx != nil {
		c := ctx
		if pol.Enforce && pol.Timeout > 0 {
			var cancel context.CancelFunc
			c, cancel = context.WithTimeout(ctx, pol.Timeout)
			defer cancel()
		}
		servedReq = req.WithContext(c)
	}

	var resp *http.Response
	var err error
	if fn != nil {
		resp, err = fn(servedReq)
	} else {
		resp = newMockResponse(servedReq, http.StatusOK, "")
	}
	if err != nil {
		return nil, err
	}
	if resp == nil {
		resp = newMockResponse(servedReq, http.StatusOK, "")
	}
	if resp.Body == nil {
		resp.Body = io.NopCloser(strings.NewReader(""))
	}

	if pol.Enforce {
		if isRedirectStatus(resp.StatusCode) {
			loc := resp.Header.Get("Location")
			target, perr := url.Parse(loc)
			// 先关 Body 再返回，避免真 http.Client 下连接泄漏（N1）。
			_ = resp.Body.Close()
			if perr == nil && target.Host != "" && isGatewayHost(target.Hostname()) {
				return nil, ErrSessionExpired
			}
			return nil, ErrBlockedRedirect
		}
		resp.Header.Del("Set-Cookie")
	}
	return resp, nil
}

// hostInAllowlist 判断 host 是否在白名单（大小写不敏感，忽略端口）。
func hostInAllowlist(host string, allowlist []string) bool {
	h := strings.ToLower(host)
	for _, a := range allowlist {
		if strings.EqualFold(strings.TrimSpace(a), h) {
			return true
		}
	}
	return false
}

// isGatewayHost 判断是否为 WebVPN 网关域（3xx 指向它视为会话过期）。
func isGatewayHost(host string) bool {
	return strings.EqualFold(strings.ToLower(host), "webvpn.sicau.edu.cn")
}

// isRedirectStatus 只把带 Location 语义的 3xx 当重定向；304 等排除。
func isRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

func clonePolicy(p DoerPolicy) DoerPolicy {
	return DoerPolicy{
		Enforce:      p.Enforce,
		AllowedHosts: append([]string(nil), p.AllowedHosts...),
		Timeout:      p.Timeout,
	}
}

func newMockResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

// ---------------------------------------------------------------------------
// MockClock / RealClock
// ---------------------------------------------------------------------------

// MockClock 是 Clock 的可设定实现，供测试注入固定时刻。
type MockClock struct {
	mu  sync.RWMutex
	now time.Time
}

// NewMockClock 构造时钟；传入零值时回落到系统当前时间（保证 Now 非零）。
func NewMockClock(t time.Time) *MockClock {
	if t.IsZero() {
		t = time.Now()
	}
	return &MockClock{now: t}
}

// Now 实现 Clock。
func (c *MockClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

// Set 重设当前时刻。
func (c *MockClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// RealClock 是 Clock 的真实实现：直接读系统时间，供装配层注入。
type RealClock struct{}

// NewRealClock 构造真实时钟。
func NewRealClock() *RealClock { return &RealClock{} }

// Now 实现 Clock。
func (*RealClock) Now() time.Time { return time.Now() }
