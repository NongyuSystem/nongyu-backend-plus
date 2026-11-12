// Package track 是「用户行为埋点」域。
//
// 它与另外两层刻意分开（GLOSSARY.md 的三层定义）：
//   - 用户行为埋点（本包）：产品分析用，进 PG 的 track_event 表；
//   - 运行日志：stdout + slog，由各层直接打；
//   - 审计流水：PG audit_log，旁路写入，只记标识性字段。
//
// 三条硬约束（决策-用户行为埋点.md）：
//  1. **信封统一** {code,message} + camelCase，**不照抄旧 Track 的 {ok,data}**；
//  2. **脱敏顺序不可颠倒**：顶层白名单 → props 敏感键剔除 → 再 4KB 截断；
//  3. **缓冲满不返回 503**：正常返回并计入 dropped，避免客户端重试风暴。
//
// 本包只依赖 apperr 与 platform（Clock）；不 import server / app / identity / config。
package track

import (
	"encoding/json"
	"strings"
	"time"

	"nongyu-backend-plus/internal/apperr"
)

// EventType 是事件类型枚举（7 个，最后一个仅服务端）。
type EventType string

const (
	TypeScreenView   EventType = "screen_view"
	TypeButtonClick  EventType = "button_click"
	TypePerf         EventType = "perf"
	TypeAppOpen      EventType = "app_open"
	TypeHeartbeat    EventType = "heartbeat"
	TypeCrash        EventType = "crash"
	TypeLLMProxyFail EventType = "llm_proxy_fail"
)

// ParseEventType 解析事件类型。
func ParseEventType(s string) (EventType, bool) {
	switch EventType(s) {
	case TypeScreenView, TypeButtonClick, TypePerf, TypeAppOpen,
		TypeHeartbeat, TypeCrash, TypeLLMProxyFail:
		return EventType(s), true
	default:
		return "", false
	}
}

// ClientReportable 判断某类型是否允许客户端上报。
//
// llm_proxy_fail 是纯服务端事件（后端代理失败时自己记），
// 客户端报它等于伪造服务端事实，必须拒。
func (t EventType) ClientReportable() bool {
	if t == "" {
		return false
	}
	return t != TypeLLMProxyFail
}

// Platform 是客户端平台。
type Platform string

const (
	PlatformIOS     Platform = "ios"
	PlatformAndroid Platform = "android"
	PlatformWeb     Platform = "web"
)

// ParsePlatform 解析平台取值。
func ParsePlatform(s string) (Platform, bool) {
	switch Platform(s) {
	case PlatformIOS, PlatformAndroid, PlatformWeb:
		return Platform(s), true
	default:
		return "", false
	}
}

// 批量与体积上限。
const (
	// MaxBatch 是单次上报的事件条数上限。
	MaxBatch = 100
	// MaxBodyBytes 是单次请求体上限（1MB）。
	MaxBodyBytes = 1 << 20
	// MaxPropsBytes 是 props 序列化后的上限，超出即截断。
	MaxPropsBytes = 4 << 10
	// MaxNameLen 是 eventName 的长度上限。
	MaxNameLen = 200
)

// Event 是一条埋点事件。
//
// UserID 由**服务端注入**，忽略客户端自报——否则任何人可伪造他人的行为。
type Event struct {
	EventID    string
	EventType  EventType
	EventName  string
	ClientTsMs int64
	SessionID  string
	AppVersion string
	Platform   Platform
	Props      map[string]any
	RequestID  string
	UserID     int64
	ReceivedAt time.Time
}

// 顶层白名单：只认这些字段，其余一律丢弃（不报错）。
//
// 用白名单而不是黑名单：新客户端可能带上未定义字段，静默丢弃比报错
// 更不容易让埋点链路断掉；同时也防止有人把整份用户档案塞进顶层。
var allowedTopLevel = map[string]bool{
	"eventId":    true,
	"eventType":  true,
	"eventName":  true,
	"clientTsMs": true,
	"sessionId":  true,
	"appVersion": true,
	"platform":   true,
	"props":      true,
}

// sensitivePropKeys 是 props 里必须剔除的键名片段（大小写不敏感、按子串匹配）。
//
// 用子串而不是精确匹配：真实客户端会用 userPassword、accessToken、
// idCardNo 这类变体，精确匹配会漏。
var sensitivePropKeys = []string{
	"password", "passwd", "pwd",
	"idcard", "id_card",
	"token", "ticket", "cookie", "authorization",
	"secret", "credential",
	"phone", "mobile", "email", "address",
	"code", // 验证码/邀请码一类
}

// isSensitiveKey 判断某个 props 键是否敏感。
func isSensitiveKey(k string) bool {
	lower := strings.ToLower(k)
	for _, s := range sensitivePropKeys {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// SanitizeResult 是脱敏的结果，带上计数供观测。
type SanitizeResult struct {
	Event Event
	// DroppedTopLevel 是被丢弃的未知顶层字段数。
	DroppedTopLevel int
	// RemovedProps 是被剔除的敏感 props 键数。
	RemovedProps int
	// PropsTruncated 表示 props 是否被截断。
	PropsTruncated bool
}

// Sanitize 按**固定顺序**脱敏一条原始事件。
//
// 顺序是硬约束，不可颠倒：
//  1. 顶层白名单：未知顶层字段丢弃（不报错）；
//  2. props 敏感键剔除；
//  3. 再做 4KB 截断（**在剔除之后**——否则先截断可能把敏感值截成
//     不完整片段而躲过第 2 步的键名匹配）。
//
// userID / receivedAt / requestId 由调用方（service）注入，本函数不碰
// 客户端上报的 userId（它在白名单外，天然被丢弃）。
func Sanitize(raw map[string]any) (SanitizeResult, error) {
	res := SanitizeResult{}

	// 第 1 步：顶层白名单。
	clean := make(map[string]any, len(raw))
	for k, v := range raw {
		if allowedTopLevel[k] {
			clean[k] = v
		} else {
			res.DroppedTopLevel++
		}
	}

	ev, err := decode(clean)
	if err != nil {
		return res, err
	}

	// 第 2 步：props 敏感键剔除。
	if ev.Props != nil {
		for k := range ev.Props {
			if isSensitiveKey(k) {
				delete(ev.Props, k)
				res.RemovedProps++
			}
		}
	}

	// 第 3 步：4KB 截断（在剔除之后）。
	if ev.Props != nil {
		b, err := json.Marshal(ev.Props)
		if err == nil && len(b) > MaxPropsBytes {
			// 截断策略：整体丢弃 props，而不是切半个 JSON。
			// 理由：半个 JSON 无法解析，下游还得处理「不完整」这种状态；
			// 直接丢更干净，且 truncated 计数能让我们知道发生了多少。
			ev.Props = nil
			res.PropsTruncated = true
		}
	}

	res.Event = ev
	return res, nil
}

// decode 把白名单后的 map 解成 Event 并做基础校验。
func decode(raw map[string]any) (Event, error) {
	var ev Event

	ev.EventID, _ = raw["eventId"].(string)
	if ev.EventID == "" {
		return ev, apperr.Errorf(apperr.CodeValidationFailed, "eventId 不能为空")
	}

	typeStr, _ := raw["eventType"].(string)
	et, ok := ParseEventType(typeStr)
	if !ok {
		return ev, apperr.Errorf(apperr.CodeValidationFailed, "eventType 非法")
	}
	if !et.ClientReportable() {
		return ev, apperr.Errorf(apperr.CodeForbidden, "该事件类型不允许客户端上报")
	}
	ev.EventType = et

	ev.EventName, _ = raw["eventName"].(string)
	if len(ev.EventName) > MaxNameLen {
		return ev, apperr.Errorf(apperr.CodeValidationFailed, "eventName 超长")
	}

	if v, ok := raw["clientTsMs"].(float64); ok {
		ev.ClientTsMs = int64(v)
	}
	ev.SessionID, _ = raw["sessionId"].(string)
	ev.AppVersion, _ = raw["appVersion"].(string)

	platStr, _ := raw["platform"].(string)
	p, ok := ParsePlatform(platStr)
	if !ok {
		return ev, apperr.Errorf(apperr.CodeValidationFailed, "platform 非法")
	}
	ev.Platform = p

	if props, ok := raw["props"].(map[string]any); ok {
		ev.Props = props
	}
	return ev, nil
}
