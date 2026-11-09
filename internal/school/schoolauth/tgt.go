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
	"fmt"
	"time"

	"nongyu-backend-plus/internal/platform"
)

// TGTStore 是 tgt 的持久化契约（由 platform.TicketStore 承载，加密后存）。
//
// 复用 TicketStore 而不是新造接口：它本来就是「按 userID 存一段加密字节 + 过期时刻」，
// 形状完全吻合，且已有 Redis 实现与过期索引。
type TGTStore = platform.TicketStore

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

// Save 加密保存一个 tgt。
func (m *TGTManager) Save(ctx context.Context, userID, tgt string) error {
	if userID == "" || tgt == "" {
		return fmt.Errorf("schoolauth: Save 需要 userID 与 tgt")
	}
	sealed, err := m.cipher.Seal([]byte(tgt))
	if err != nil {
		return err
	}
	return m.store.Put(ctx, userID, sealed, m.clock.Now().Add(m.ttl))
}

// Load 取出并解密 tgt；不存在返回 platform.ErrNotFound。
func (m *TGTManager) Load(ctx context.Context, userID string) (string, time.Time, error) {
	sealed, expiresAt, err := m.store.Get(ctx, userID)
	if err != nil {
		return "", time.Time{}, err
	}
	plain, err := m.cipher.Open(sealed)
	if err != nil {
		return "", time.Time{}, err
	}
	return string(plain), expiresAt, nil
}

// EnsureFresh 返回一个可用的 tgt：本地有且未临期就用本地的，否则用 fallback 换票并保存新的。
//
// fallback 是客户端本次交上来的 tgt（可为空）。两者都没有则报错。
func (m *TGTManager) EnsureFresh(ctx context.Context, userID, fallback string) (string, error) {
	if userID != "" {
		if tgt, exp, err := m.Load(ctx, userID); err == nil {
			// 未临期：直接用。
			if m.clock.Now().Add(m.refreshBefore).Before(exp) {
				return tgt, nil
			}
			// 临期：优先用本地 tgt 去换票续期（不需要客户端重交）。
			if refreshed, rerr := m.refreshWith(ctx, userID, tgt); rerr == nil {
				return refreshed, nil
			}
		} else if !platform.IsNotFound(err) {
			return "", err
		}
	}

	// 本地没有可用 tgt：用客户端交来的换一次并保管。
	if fallback == "" {
		return "", fmt.Errorf("schoolauth: 没有可用的 tgt（本地无记录且客户端未提供）")
	}
	return m.refreshWith(ctx, userID, fallback)
}

// refreshWith 用给定 tgt 换一次票，拿到新 tgt 后保存并返回。
func (m *TGTManager) refreshWith(ctx context.Context, userID, tgt string) (string, error) {
	res, err := m.source.exchangeTicketOnly(ctx, tgt)
	if err != nil {
		return "", err
	}
	// 换了新 tgt：保存它（服务端会滚动续期）。
	if userID != "" && res.TGT != "" {
		if err := m.Save(ctx, userID, res.TGT); err != nil {
			// 保存失败不阻断本次使用。
			_ = err
		}
	}
	// 若响应没带新 tgt，沿用旧的（它仍在有效期内）。
	if res.TGT == "" {
		return tgt, nil
	}
	return res.TGT, nil
}

// Clear 删除保管的 tgt（登出/用户解绑时调用）。
func (m *TGTManager) Clear(ctx context.Context, userID string) error {
	return m.store.Delete(ctx, userID)
}
