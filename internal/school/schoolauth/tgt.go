package schoolauth

// tgt 保管与续期。
//
// 为什么要保管（维护者决定，2026-11-09）：
//
//	换票响应里**直接带回新的 tgt**（实测：{"tgt":"201690:TGT:..."}）。
//	既然换一次票就拿到新的，就没必要每次让客户端重交凭据——
//	服务端把 tgt 加密存起来，10 天内复用，临期再换一次即自动续期。
//
// 安全要点（与 ADR-0002 一致）：
//   - tgt 是等效长期凭据，**加密存储**，绝不明文落库/进日志/进审计；
//   - 只按 userID 存，不与任何请求体里的身份混用；
//   - 到期前主动换票续期，避免用过期 tgt 反复失败。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"nongyu-backend-plus/internal/platform"
)

// TGTStore 是 tgt 的持久化契约（由 platform.TicketStore 承载，加密后存）。
//
// 复用 TicketStore 而不是新造接口：它本来就是「按 userID 存一段加密字节 + 过期时刻」，
// 形状完全吻合，且已有 Redis 实现与过期索引。
type TGTStore = platform.TicketStore

// 续期失败的两类原因，必须由调用方可区分（#25 / #20）：
//
//   - ErrCredentialRejected：学校侧拒绝了这组凭据（业务码非 200 / 未登录）。
//     这是**用户需要重新登录**的场景，提示里要讲清「需客户端重交凭据」。
//   - ErrMissingCookie：本地保管记录里没有配套 cookie。这是**代码或数据 bug**
//     （骨架期只存了裸 tgt 的旧记录），不该让用户去重登，而应修记录/修代码。
//
// 两者对应的运维动作完全不同，混成一句「续期失败」会误导排查。
var (
	ErrCredentialRejected = errors.New("schoolauth: 学校侧拒绝了保管的凭据（需客户端重新交凭据）")
	ErrMissingCookie      = errors.New("schoolauth: 保管记录缺少整套会话 cookie（代码/数据问题，非用户需重登）")
)

// TGTCipher 是加解密能力（与密码镜像用不同密钥）。
type TGTCipher interface {
	Seal(plaintext []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

// TGTManager 保管并续期用户的 go-admin-tgt。
type TGTManager struct {
	store  TGTStore
	cipher TGTCipher
	source *JiaowuSource
	clock  platform.Clock
	// ttl 是本地保管时长；比服务端 10 天短，留余量。
	ttl time.Duration
	// refreshBefore 是「提前多久续期」。
	refreshBefore time.Duration
}

// TGTManagerOptions 是构造参数。
type TGTManagerOptions struct {
	Store  TGTStore
	Cipher TGTCipher
	Source *JiaowuSource
	Clock  platform.Clock
	// TTL 默认 9 天（服务端 10 天，留 1 天余量）。
	TTL time.Duration
	// RefreshBefore 默认 1 天（到期前 1 天主动续）。
	RefreshBefore time.Duration
}

// NewTGTManager 构造 tgt 管理器。
func NewTGTManager(o TGTManagerOptions) (*TGTManager, error) {
	if o.Store == nil {
		return nil, fmt.Errorf("schoolauth: TGTManager 需要 Store")
	}
	if o.Cipher == nil {
		return nil, fmt.Errorf("schoolauth: TGTManager 需要 Cipher")
	}
	if o.Source == nil {
		return nil, fmt.Errorf("schoolauth: TGTManager 需要 Source")
	}
	if o.Clock == nil {
		o.Clock = platform.NewRealClock()
	}
	if o.TTL <= 0 {
		o.TTL = 9 * 24 * time.Hour
	}
	if o.RefreshBefore <= 0 {
		o.RefreshBefore = 24 * time.Hour
	}
	return &TGTManager{
		store:         o.Store,
		cipher:        o.Cipher,
		source:        o.Source,
		clock:         o.Clock,
		ttl:           o.TTL,
		refreshBefore: o.RefreshBefore,
	}, nil
}

// TGTBundle 是保管的整套登录态。
//
// 为什么不是单个 tgt（#20 M5）：实测换票必须带**整套同域 cookie**，
// 单枚 go-admin-tgt 请求返回 code=500 未登录。因此续期时既要有新的 tgt，
// 也要有配套的 laravel_session / XSRF-TOKEN 等 cookie。
type TGTBundle struct {
	// TGT 是 go-admin-tgt 的值（换票响应里带回的新值）。
	TGT string
	// CookieHeader 是整套同域 cookie 头（含 go-admin-tgt 与 laravel_session 等）。
	CookieHeader string
}

// Save 加密保存整套登录态。
// 存储内容仍是单段不透明字节（TicketStore 形状不变），只是明文从「单个 tgt」
// 变成「tgt + 整套 cookie」的 JSON——兼容既有 TicketStore 实现。
func (m *TGTManager) Save(ctx context.Context, userID string, b TGTBundle) error {
	if userID == "" || strings.TrimSpace(b.TGT) == "" {
		return fmt.Errorf("schoolauth: Save 需要 userID 与 tgt")
	}
	plain, err := json.Marshal(b)
	if err != nil {
		return err
	}
	sealed, err := m.cipher.Seal(plain)
	if err != nil {
		return err
	}
	return m.store.Put(ctx, userID, sealed, m.clock.Now().Add(m.ttl))
}

// Load 取出并解密整套登录态；不存在返回 platform.ErrNotFound。
//
// 兼容旧格式：若解出来不是 JSON（骨架期只存了裸 tgt 字符串），
// 包装成 CookieHeader 为空的 bundle——EnsureFresh 会据此给出明确失败原因。
func (m *TGTManager) Load(ctx context.Context, userID string) (TGTBundle, time.Time, error) {
	sealed, expiresAt, err := m.store.Get(ctx, userID)
	if err != nil {
		return TGTBundle{}, time.Time{}, err
	}
	plain, err := m.cipher.Open(sealed)
	if err != nil {
		return TGTBundle{}, time.Time{}, err
	}
	var b TGTBundle
	if json.Unmarshal(plain, &b) == nil && b.TGT != "" {
		return b, expiresAt, nil
	}
	return TGTBundle{TGT: string(plain)}, expiresAt, nil
}

// EnsureFresh 返回一个可用的 tgt：本地有且未临期就用本地的，否则用 fallback 换票并保存新的。
//
// fallback 是客户端本次交上来的整套凭据（可为空）。两者都没有则报错。
func (m *TGTManager) EnsureFresh(ctx context.Context, userID string, fallback Credential) (string, error) {
	if userID != "" {
		if bundle, exp, err := m.Load(ctx, userID); err == nil {
			// 未临期：直接用本地 tgt。
			if m.clock.Now().Add(m.refreshBefore).Before(exp) {
				return bundle.TGT, nil
			}
			// 临期：用本地整套 cookie 去换票续期（不需要客户端重交）。
			// 续期失败**必须返回真实原因**（评审 M3）：本地明明有记录却落到
			// fallback 会误报「没有可用的 tgt」，把学校站不可达 / cookie 失效
			// / 业务码 500 等真实原因全部吞掉。
			refreshed, rerr := m.refreshWith(ctx, userID, bundle)
			if rerr != nil {
				return "", fmt.Errorf("schoolauth: 续期失败：%w", rerr)
			}
			return refreshed, nil
		} else if !platform.IsNotFound(err) {
			return "", err
		}
	}

	// 本地没有可用 tgt：用客户端交来的整套凭据换一次并保管。
	tgt := cookieValue(fallback, goAdminTgtCookie)
	if tgt == "" {
		return "", fmt.Errorf("schoolauth: 没有可用的 tgt（本地无记录且客户端未提供）")
	}
	bundle := TGTBundle{TGT: tgt, CookieHeader: fallback.FullCookieHeader(tgt)}
	return m.refreshWith(ctx, userID, bundle)
}

// refreshWith 用整套凭据换一次票，拿到新 tgt 后保存并返回。
//
// 关键（#20 M5）：换票必须带**整套** cookie。若 bundle 里没有 cookieHeader，
// 说明是骨架期只存了裸 tgt 的旧记录，真实环境必然失败——这里直接给出
// 可识别的错误，而不是静默只带 tgt 去撞 code=500。
func (m *TGTManager) refreshWith(ctx context.Context, userID string, b TGTBundle) (string, error) {
	if strings.TrimSpace(b.CookieHeader) == "" {
		return "", fmt.Errorf("%w：无法续期，需修保管记录或代码", ErrMissingCookie)
	}
	res, err := m.source.exchangeTicketOnly(ctx, b.TGT, b.CookieHeader)
	if err != nil {
		// 换票失败按「学校侧拒绝凭据」归类：这是用户需要重登的场景。
		// 用 %w 同时保留底层原因，便于排查具体是业务码、网络还是解析问题。
		return "", fmt.Errorf("%w：%v", ErrCredentialRejected, err)
	}
	// 换了新 tgt：滚动保存（cookieHeader 保留，只替换 tgt 值）。
	if userID != "" && res.TGT != "" {
		next := TGTBundle{
			TGT:          res.TGT,
			CookieHeader: replaceCookieValue(b.CookieHeader, goAdminTgtCookie, res.TGT),
		}
		if err := m.Save(ctx, userID, next); err != nil {
			// 保存失败不静默吞掉：返回可识别的错误，让调用方/日志看到（评审 M3）。
			return "", fmt.Errorf("schoolauth: 续期后回写保管失败：%w", err)
		}
	}
	if res.TGT == "" {
		return b.TGT, nil
	}
	return res.TGT, nil
}

// Clear 删除保管的 tgt（登出/用户解绑时调用）。
func (m *TGTManager) Clear(ctx context.Context, userID string) error {
	return m.store.Delete(ctx, userID)
}
