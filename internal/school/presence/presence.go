package presence

// 本文件是「校内/校外状态」的模型与判定核心。
//
// 两条来源、一个模型：
//   - 后端配置：构造时传入 backendMode，进程内不变，不做周期性自探测；
//   - 客户端上报：Report 把 ClientPresence 外置到 platform.TicketStore（按用户键控）。
//
// Decide 合并二者：有「新鲜」上报 → 用上报模式；上报超过陈旧阈值未更新 → 视为
// 不可信、退回后端配置；无上报 → 用后端配置。陈旧判定完全由 platform.Clock 驱动，
// 因此可用 MockClock.Set 把时间推进到阈值之外做可测验证（issue #16 验收）。
//
// 进程无状态：Service 只持有外部能力接口与不可变的构造参数，没有任何 map 缓存、
// 没有 goroutine / ticker。两个 Service 实例共享同一 TicketStore 时读到同一份状态。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"nongyu-backend-plus/internal/platform"
)

// Mode 是「该用户这一次该走直连形态还是经网关形态」的判定结果。
//
// ModeDirect 对应「校内 / 直连形态」（不走 WebVPN 网关），
// ModeVPN 对应「校外 / 经网关形态」（地址经 WebVPN 编码）。
// 与父包 school.Target 的 Direct / Vpn 两张地址形态一一对应。
type Mode string

const (
	// ModeDirect 直连形态（校内）。
	ModeDirect Mode = "direct"
	// ModeVPN 经网关形态（校外，经 WebVPN）。
	ModeVPN Mode = "vpn"
)

// DefaultBackendMode 是后端配置缺省/非法时回落的安全模式（直连形态）。
const DefaultBackendMode = ModeDirect

// DefaultStaleAfter 是陈旧阈值的缺省值：5 分钟未更新即视为不可信。
// 装配层应显式传入配置值；这里只保证零值/非法值不会让服务进入不确定状态。
const DefaultStaleAfter = 5 * time.Minute

// 入参校验错误。用哨兵错误便于 handler 与调用方 errors.Is 判定。
var (
	// ErrInvalidMode 表示上报/构造的 Mode 不是 ModeDirect / ModeVPN。
	ErrInvalidMode = errors.New("presence: 非法 mode（只允许 direct / vpn）")
	// ErrEmptyUserID 表示 userID 为空，无法按用户键控外置存储。
	ErrEmptyUserID = errors.New("presence: userID 不能为空")
)

// IsValidMode 判断 m 是否为受支持的模式。
func IsValidMode(m Mode) bool {
	return m == ModeDirect || m == ModeVPN
}

// ClientPresence 是客户端每次进入 / 网络切换时上报的状态（客户端视角）。
// 可 JSON 序列化 —— 上报记录以不透明字节外置到 TicketStore。
type ClientPresence struct {
	UserID string    `json:"userID"`
	Mode   Mode      `json:"mode"`
	At     time.Time `json:"at"` // 上报时刻，由 platform.Clock 盖章，用于陈旧判定
}

// BackendPresence 是后端侧状态（后端视角）：启动时读配置，进程内不变。
// 刻意独立成类型，便于后续在配置里扩展字段而不动 ClientPresence 的形状。
type BackendPresence struct {
	Mode Mode
}

// Decision 是 Decide 的输出：模式 + 该模式来自哪条来源，便于 HTTP 层与调用方
// 观测「上报生效 / 陈旧回落」，也让测试能断言 source 而不只看 mode。
type Decision struct {
	Mode   Mode   `json:"mode"`
	Source string `json:"source"` // "client"（新鲜上报）/ "backend"（无上报或陈旧回落）
}

const (
	sourceClient  = "client"
	sourceBackend = "backend"
)

// Service 是校内/校外状态子域的服务。
//
// 字段刻意保持最小且不可变：store/clock 是 platform 外部能力接口；backendMode /
// staleAfter 是构造时读一次的不可变配置。没有任何可变字段、没有出站能力、没有
// 后台协程 —— 这是「进程无状态 + 后端侧不做周期性自探测」的结构性保证。
type Service struct {
	store       platform.TicketStore
	clock       platform.Clock
	backendMode Mode
	staleAfter  time.Duration
}

// New 构造 Service。
//
// 参数：
//   - store：外置存储（按用户键控），上报状态全部落在这里，进程无状态；
//   - clock：时间源，用于给上报盖章与陈旧判定（测试用 MockClock 推进）；
//   - backendMode：后端启动时从配置读到的模式，构造后不变、不发起任何自探测；
//   - staleAfter：陈旧阈值，上报超过该时长未更新即退回 backendMode。
//
// 装配层（app.New）把 platform 能力通过接口注入；本包不 import app/server/config，
// 也不自行构造具体实现。非法 backendMode / 非正 staleAfter 回落到文档化的默认值。
func New(store platform.TicketStore, clock platform.Clock, backendMode Mode, staleAfter time.Duration) *Service {
	if !IsValidMode(backendMode) {
		backendMode = DefaultBackendMode
	}
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}
	return &Service{
		store:       store,
		clock:       clock,
		backendMode: backendMode,
		staleAfter:  staleAfter,
	}
}

// BackendMode 返回构造时固定下来的后端配置模式（进程内不变）。
func (s *Service) BackendMode() Mode { return s.backendMode }

// StaleAfter 返回构造时固定下来的陈旧阈值。
func (s *Service) StaleAfter() time.Duration { return s.staleAfter }

// Report 接收客户端上报：校验入参 → 用 Clock 盖章上报时刻 → JSON 序列化后
// 外置到 TicketStore（按用户键控，会话级、expiresAt 零值）。
//
// 客户端每次进入上报一次、网络状态切换时再上报一次；重复上报覆盖同一用户键，
// 陈旧窗口从新上报时刻重新计时。本方法不发起任何出站请求。
func (s *Service) Report(ctx context.Context, p ClientPresence) error {
	if p.UserID == "" {
		return ErrEmptyUserID
	}
	if !IsValidMode(p.Mode) {
		return ErrInvalidMode
	}

	record := ClientPresence{
		UserID: p.UserID,
		Mode:   p.Mode,
		At:     s.clock.Now(),
	}
	sealed, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("presence: 序列化上报记录失败：%w", err)
	}
	// 会话级记录：expiresAt 传零值，不参与 TicketStore 的「即将过期」错峰扫描。
	if err := s.store.Put(ctx, p.UserID, sealed, time.Time{}); err != nil {
		return fmt.Errorf("presence: 写入外置存储失败：%w", err)
	}
	return nil
}

// Decide 合并「客户端上报 + 后端配置」两处来源，决定该用户这一次该走哪种形态。
//
//   - 无上报（ErrNotFound）→ 后端配置（source=backend）；
//   - 有上报但超过陈旧阈值未更新 → 视为不可信，退回后端配置（source=backend）；
//   - 有上报且新鲜 → 用上报模式（source=client）。
//
// 语义：「超过」阈值才判陈旧，恰好等于阈值仍算新鲜。存储读取/反序列化失败按
// 「无上报」保守处理（退回后端配置），不把内部错误泄漏成不可判定。
func (s *Service) Decide(ctx context.Context, userID string) (Mode, error) {
	d, _ := s.DecideDetailed(ctx, userID)
	return d.Mode, nil
}

// DecideDetailed 与 Decide 判定相同，但额外返回来源（client / backend），供
// HTTP 层与调用方观测「上报生效 / 陈旧回落」。生产调用 Decide 即可。
func (s *Service) DecideDetailed(ctx context.Context, userID string) (Decision, error) {
	backend := Decision{Mode: s.backendMode, Source: sourceBackend}
	if userID == "" {
		return backend, nil
	}

	sealed, _, err := s.store.Get(ctx, userID)
	if err != nil {
		// 不存在（或读取失败）一律保守回落到后端配置。
		return backend, nil
	}
	var p ClientPresence
	if err := json.Unmarshal(sealed, &p); err != nil || !IsValidMode(p.Mode) {
		return backend, nil
	}

	// 陈旧判定：now - At 严格大于阈值才算陈旧；等于阈值仍属新鲜。
	// At 是未来时刻（时钟回拨等异常）也按新鲜处理，避免误判。
	if elapsed := s.clock.Now().Sub(p.At); elapsed > s.staleAfter {
		return backend, nil
	}
	return Decision{Mode: p.Mode, Source: sourceClient}, nil
}
