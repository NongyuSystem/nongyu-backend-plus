package bridge

// 网关票的存储实现（装配层）。
//
// 复用一个专门的 RedisTicketStore 实例（前缀 `webvpn:`），把票当成「按用户存的
// 一段不透明字节」——与 tgt / presence 一样，但命名空间独立，互不覆盖。
//
// 为什么不放进 platform：这里是「票值怎么进出」的适配，属于装配层职责；
// jiaowu 子域只认 WebVPNTicketStore 接口，不绑 Redis。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"nongyu-backend-plus/internal/platform"
)

// WebVPNTicketStore 把 platform.TicketStore 适配成 jiaowu.WebVPNTicketStore。
type WebVPNTicketStore struct {
	store platform.TicketStore
	// cipher 是可选的加密封装。网关票是活凭证，落库前必须加密。
	cipher platform.Cipher
}

func (s WebVPNTicketStore) Put(ctx context.Context, userID, ticket string, expiresAt time.Time) error {
	ticket = strings.TrimSpace(ticket)
	if ticket == "" {
		return fmt.Errorf("webvpn: 票为空")
	}
	payload := []byte(ticket)
	if s.cipher != nil {
		sealed, err := s.cipher.Seal(payload)
		if err != nil {
			return err
		}
		payload = sealed
	}
	return s.store.Put(ctx, userID, payload, expiresAt)
}

func (s WebVPNTicketStore) Get(ctx context.Context, userID string) (string, time.Time, error) {
	raw, exp, err := s.store.Get(ctx, userID)
	if err != nil {
		return "", time.Time{}, err
	}
	plain := raw
	if s.cipher != nil {
		opened, err := s.cipher.Open(raw)
		if err != nil {
			return "", time.Time{}, err
		}
		plain = opened
	}
	return string(plain), exp, nil
}

func (s WebVPNTicketStore) Delete(ctx context.Context, userID string) error {
	return s.store.Delete(ctx, userID)
}

// NewWebVPNTicketStore 构造网关票存储桥。
func NewWebVPNTicketStore(store platform.TicketStore, cipher platform.Cipher) WebVPNTicketStore {
	return WebVPNTicketStore{store: store, cipher: cipher}
}
