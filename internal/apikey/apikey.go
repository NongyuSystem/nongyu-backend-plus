// Package apikey 是「第三方 API 密钥池」域。
//
// 需求（2026-11-23 维护者）：客户端**不存死高德 key**，通过服务端密钥池
// 取用。这样做的三个实际理由：
//
//  1. APK 里的任何字符串都能被提取（反编译成本极低），死 key 等于公开 key；
//  2. 高德个人 key 有 QPS/日配额，单 key 高峰期会被限流——池可以轮换；
//  3. 某把 key 被滥用/失效时，管理员禁用它即可，不必发新版本客户端。
//
// **必须说清的一个边界**：密钥一旦下发到客户端，它就不再是秘密了。
// 这个池能降低「key 被永久硬编码」的风险，并让轮换可行，
// 但它**不能**防止拿到 key 的人滥用。真正的配额防线在高德侧
// （在控制台给 key 绑定包名/签名/域名 + 设置配额）。
// 任何声称「服务端下发 key 就安全了」的设计都是自欺。
//
// 本包只依赖 apperr 与 platform（Cipher/Clock），不 import server / app。
package apikey

import (
	"context"
	"strings"
	"sync"
	"time"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/platform"
)

// Platform 是 key 绑定的客户端平台。
//
// 高德 key 是按平台绑定的，混用会导致验签失败，所以分发时按端区分。
type Platform string

const (
	PlatformAndroid Platform = "android"
	PlatformIOS     Platform = "ios"
	PlatformWeb     Platform = "web"
	// PlatformServer 是服务端自用（如逆地理编码），不下发给客户端。
	PlatformServer Platform = "server"
)

// ParsePlatform 解析平台取值。
func ParsePlatform(s string) (Platform, bool) {
	switch Platform(s) {
	case PlatformAndroid:
		return PlatformAndroid, true
	case PlatformIOS:
		return PlatformIOS, true
	case PlatformWeb:
		return PlatformWeb, true
	case PlatformServer:
		return PlatformServer, true
	default:
		return "", false
	}
}

// Key 是密钥池里的一条记录。
//
// **注意：结构体里没有明文 key**。明文只在 Seal/Open 的瞬间存在于内存，
// 不落库、不进日志、不进 JSON（见 handler 的响应体）。
type Key struct {
	ID       int64
	Platform Platform
	// Cipher 是密文（AES-256-GCM）。
	Cipher []byte
	// Hint 是后四位，用于管理员辨认，不含明文。
	Hint       string
	QPSLimit   int
	Enabled    bool
	LastUsedAt *time.Time
	UsedCount  int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Repo 是密钥池的持久化接口。
type Repo interface {
	Create(ctx context.Context, k *Key) error
	// Update 更新可变字段（平台不可改：改平台等于换一把 key）。
	Update(ctx context.Context, k *Key) error
	FindByID(ctx context.Context, id int64) (*Key, error)
	// List 返回密钥列表（含停用，管理端用）。
	List(ctx context.Context, platform Platform) ([]*Key, error)
	// ListEnabled 返回某平台下启用的 key（用于轮换取用）。
	ListEnabled(ctx context.Context, platform Platform) ([]*Key, error)
	Delete(ctx context.Context, id int64) error
	// MarkUsed 记录一次取用（累加计数 + 刷新 last_used_at）。
	//
	// 单独一个方法而不是 Update 的一部分：取用是高频路径，
	// 只更新这两列能让 SQL 更窄，也避免与管理员编辑互相覆盖。
	MarkUsed(ctx context.Context, id int64, at time.Time) error
}

// Options 是构造 Service 的依赖。
type Options struct {
	Repo Repo
	// Cipher 必填：key 必须加密落库（ADR-0002）。
	Cipher platform.Cipher
	Clock  platform.Clock
	// Pick 是轮换策略；nil 时用轮询（RoundRobin）。
	Pick Picker
}

// Service 是密钥池业务逻辑。
type Service struct {
	repo   Repo
	cipher platform.Cipher
	clock  platform.Clock
	pick   Picker

	// rr 是轮询游标（按平台分别计数）。
	rrMu sync.Mutex
	rr   map[Platform]uint64
}

// New 构造 Service。
func New(opts Options) *Service {
	if opts.Repo == nil {
		panic("apikey: Options.Repo 必填")
	}
	if opts.Cipher == nil {
		panic("apikey: Options.Cipher 必填（key 必须加密落库）")
	}
	if opts.Clock == nil {
		opts.Clock = platform.NewRealClock()
	}
	if opts.Pick == nil {
		opts.Pick = RoundRobin
	}
	return &Service{repo: opts.Repo, cipher: opts.Cipher, clock: opts.Clock, pick: opts.Pick,
		rr: map[Platform]uint64{}}
}

// Picker 选择本次返回哪一把 key。
//
// 抽成函数是为了将来能换成「按剩余配额选」「按权重选」而不动 Service。
type Picker func(keys []*Key, seq uint64) *Key

// RoundRobin 是默认策略：按调用序轮换，让各 key 的配额均匀消耗。
//
// 为什么不用「随机」：轮询的分布可预测，排查「为什么这条请求被限流了」
// 时能算出来；随机还要考虑种子与偏斜。
func RoundRobin(keys []*Key, seq uint64) *Key {
	if len(keys) == 0 {
		return nil
	}
	return keys[seq%uint64(len(keys))]
}

// AcquireResult 是取用结果。
//
// 只返回 Hint（后四位）与明文 key 本身，**不含 ID 与密文**——
// 客户端不需要也不该知道池的内部结构。
type AcquireResult struct {
	Platform Platform
	Key      string
	// Hint 是后四位，便于客户端在日志里标识「用的是哪把」（不泄露完整 key）。
	Hint string
}

// Acquire 为指定平台取一把可用的 key。
//
// 失败语义：
//   - 平台非法 → validation_failed；
//   - 该平台没有启用的 key → not_found（管理端还没配，不是客户端错误）。
func (s *Service) Acquire(ctx context.Context, platformStr string) (AcquireResult, error) {
	p, ok := ParsePlatform(platformStr)
	if !ok {
		return AcquireResult{}, apperr.Errorf(apperr.CodeValidationFailed, "平台取值非法")
	}

	keys, err := s.repo.ListEnabled(ctx, p)
	if err != nil {
		return AcquireResult{}, err
	}
	if len(keys) == 0 {
		return AcquireResult{}, apperr.Errorf(apperr.CodeNotFound, "该平台暂未配置可用密钥")
	}

	// 推进轮询序号（按平台独立，避免 android 的取用把 web 的序也推走）。
	s.rrMu.Lock()
	seq := s.rr[p]
	s.rr[p] = seq + 1
	s.rrMu.Unlock()

	k := s.pick(keys, seq)
	if k == nil {
		return AcquireResult{}, apperr.Errorf(apperr.CodeNotFound, "该平台暂未配置可用密钥")
	}

	plain, err := s.cipher.Open(k.Cipher)
	if err != nil {
		// 解密失败通常是换了 TICKET_KEY（派生密钥变了）。
		// 这是**运维事故**，不是客户端问题 → 用 500 语义的错误。
		return AcquireResult{}, apperr.Errorf(apperr.CodeValidationFailed,
			"密钥解密失败（可能更换过加密主密钥），需要管理员重新录入")
	}

	// 记录取用。失败不影响本次返回——取用计数是观测数据，
	// 不该因为一次统计写失败就让客户端拿不到 key。
	_ = s.repo.MarkUsed(ctx, k.ID, s.clock.Now())

	return AcquireResult{Platform: p, Key: string(plain), Hint: k.Hint}, nil
}

// KeyInput 是管理端录入/更新密钥的入参。
type KeyInput struct {
	Platform Platform
	// Plain 是明文 key。创建时必填；更新时留空表示不改 key 本身
	//（只改配额/启停）。
	Plain    string
	QPSLimit int
	Enabled  *bool
}

// Create 录入一把新 key（明文加密后落库，明文不保存）。
func (s *Service) Create(ctx context.Context, in KeyInput) (*Key, error) {
	plain := strings.TrimSpace(in.Plain)
	if plain == "" {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "key 不能为空")
	}
	if len(plain) > 256 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "key 超长")
	}
	if in.QPSLimit < 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "qpsLimit 不能为负")
	}

	sealed, err := s.cipher.Seal([]byte(plain))
	if err != nil {
		return nil, err
	}
	qps := in.QPSLimit
	if qps == 0 {
		qps = 10
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	now := s.clock.Now()
	k := &Key{
		Platform:  in.Platform,
		Cipher:    sealed,
		Hint:      hintOf(plain),
		QPSLimit:  qps,
		Enabled:   enabled,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.repo.Create(ctx, k); err != nil {
		return nil, err
	}
	// 返回前清掉密文：调用方（handler）不该把密文带进响应。
	k.Cipher = nil
	return k, nil
}

// Update 更新配额/启停；Plain 非空时同时换 key。
func (s *Service) Update(ctx context.Context, id int64, in KeyInput) (*Key, error) {
	if id <= 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "id 非法")
	}
	k, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if plain := strings.TrimSpace(in.Plain); plain != "" {
		if len(plain) > 256 {
			return nil, apperr.Errorf(apperr.CodeValidationFailed, "key 超长")
		}
		sealed, serr := s.cipher.Seal([]byte(plain))
		if serr != nil {
			return nil, serr
		}
		k.Cipher = sealed
		k.Hint = hintOf(plain)
	}
	if in.QPSLimit < 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "qpsLimit 不能为负")
	}
	if in.QPSLimit > 0 {
		k.QPSLimit = in.QPSLimit
	}
	if in.Enabled != nil {
		k.Enabled = *in.Enabled
	}
	k.UpdatedAt = s.clock.Now()

	if err := s.repo.Update(ctx, k); err != nil {
		return nil, err
	}
	k.Cipher = nil
	return k, nil
}

// AdminList 管理端列表（含停用，永不含明文与密文）。
func (s *Service) AdminList(ctx context.Context, platformStr string) ([]*Key, error) {
	var p Platform
	if platformStr != "" {
		var ok bool
		p, ok = ParsePlatform(platformStr)
		if !ok {
			return nil, apperr.Errorf(apperr.CodeValidationFailed, "平台取值非法")
		}
	}
	keys, err := s.repo.List(ctx, p)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		k.Cipher = nil // 密文也不外泄
	}
	return keys, nil
}

// AdminGet 管理端单条（不含明文与密文）。
func (s *Service) AdminGet(ctx context.Context, id int64) (*Key, error) {
	if id <= 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "id 非法")
	}
	k, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	k.Cipher = nil
	return k, nil
}

// Delete 删除一把 key。
func (s *Service) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return apperr.Errorf(apperr.CodeValidationFailed, "id 非法")
	}
	return s.repo.Delete(ctx, id)
}

// hintOf 取 key 的后四位作为提示。
//
// 后四位足够管理员辨认（配合平台与录入时间），又不至于泄露可用凭据。
func hintOf(plain string) string {
	if len(plain) <= 4 {
		return "****"
	}
	return "****" + plain[len(plain)-4:]
}
