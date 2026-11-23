package post

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"nongyu-backend-plus/internal/apperr"
)

// DetailRule 是「某类型下某个字段该长什么样」。
//
// 它是从数据库 post_type_rule 表读出来的**数据**，不是编译期常量——
// 这是本设计的关键：加一个新类型只需要往表里插行，不改这里。
type DetailRule struct {
	// Field 是 detail 的键名。
	Field string `json:"field"`
	// ValueType 取值 string / int / number / bool。
	ValueType string `json:"valueType"`
	// Required 为真时该字段必须存在且非空。
	Required bool `json:"required"`
	// MinLen / MaxLen 用于 string（MaxLen=0 表示不限制）。
	MinLen int `json:"minLen"`
	MaxLen int `json:"maxLen"`
	// MinValue / MaxValue 用于 int / number（nil 表示不限制）。
	MinValue *float64 `json:"minValue,omitempty"`
	MaxValue *float64 `json:"maxValue,omitempty"`
	// Label 是校验失败时给人看的字段名。
	Label string `json:"label"`
}

// DetailRuleRepo 读取「类型 → 字段规则」的映射。
//
// 接口在域内、实现放 store（与其它 repo 一致）。
// 单独一个接口而不是塞进 Repo：规则是**配置类数据**（低频写、高频读），
// 与帖子的读写路径完全不同的生命周期，混在一起会让 mock 与 PG 实现都变胖。
type DetailRuleRepo interface {
	// RulesFor 返回某类型的全部字段规则；类型没有专属字段时返回空切片（不是错误）。
	RulesFor(ctx context.Context, board Board) ([]DetailRule, error)

	// KnownType 判断该类型是否**已登记且启用**。
	//
	// 必须与 RulesFor 分开：两者对 feedback（已登记、无专属字段）
	// 与 unknown_type（未登记）会给出同样的「零规则」，但语义完全不同——
	// 前者合法，后者应被拒成 400 而不是拖到数据库层炸成 500。
	KnownType(ctx context.Context, board Board) (bool, error)
}

// ValidateDetail 按规则校验 detail，并**只保留规则里声明过的字段**。
//
// 三件事一起做（顺序有意义）：
//  1. 未声明的字段一律**剔除**——防止客户端往 detail 里塞任意垃圾，
//     也防止把 detail 当成第二个 body 用；
//  2. 必填字段必须存在且非空；
//  3. 类型与范围校验（string 长度、int/number 区间）。
//
// 返回规范化后的 detail（可直接落库）。校验失败返回 validation_failed，
// 错误信息带上字段的中文 label，便于客户端直接展示。
func ValidateDetail(board Board, detail map[string]any, rules []DetailRule) (map[string]any, error) {
	// 规范化结果：即使 detail 为 nil 也返回空对象（与列默认值 '{}' 一致）。
	out := make(map[string]any, len(rules))

	// 类型没有规则 → 该类型的 detail 必须为空。
	// 这不是「跳过校验」，而是「明确不许带字段」：否则 feedback 帖可以
	// 挂一个 teacherName，数据含义就乱了。
	if len(rules) == 0 {
		if len(detail) > 0 {
			return nil, apperr.Errorf(apperr.CodeValidationFailed,
				"%s 类型的帖子不接受结构化字段（收到 %d 个）", board, len(detail))
		}
		return out, nil
	}

	for _, r := range rules {
		raw, present := detail[r.Field]
		label := r.Label
		if label == "" {
			label = r.Field
		}

		// 空字符串 / nil 统一视作「未填」，与「字段缺失」等价处理。
		// 这样客户端传 "" 和完全不传的行为一致，少一类歧义。
		empty := !present || raw == nil || raw == ""

		if empty {
			if r.Required {
				return nil, apperr.Errorf(apperr.CodeValidationFailed, "%s 不能为空", label)
			}
			continue // 选填且未填：不写进结果
		}

		v, err := coerceDetailValue(r, raw, label)
		if err != nil {
			return nil, err
		}
		out[r.Field] = v
	}

	// 兜底：剔除规则之外的字段（上面循环只处理了声明过的字段）。
	// 这里不报错而是静默丢弃——与「未知顶层字段丢弃」的口径一致，
	// 客户端多传一个字段不该让整条发帖失败。
	return out, nil
}

// coerceDetailValue 把一个原始值转成规则要求的类型并做范围校验。
//
// JSON 数字解码后统一是 float64，所以 int 类型要额外确认「没有小数部分」，
// 否则 4.5 会被悄悄截断成 4——那种静默容错会让评分数据失去可信度。
func coerceDetailValue(r DetailRule, raw any, label string) (any, error) {
	switch r.ValueType {
	case "string":
		s, ok := raw.(string)
		if !ok {
			return nil, apperr.Errorf(apperr.CodeValidationFailed, "%s 必须是字符串", label)
		}
		if r.MinLen > 0 && len(s) < r.MinLen {
			return nil, apperr.Errorf(apperr.CodeValidationFailed,
				"%s 至少 %d 个字符", label, r.MinLen)
		}
		if r.MaxLen > 0 && len(s) > r.MaxLen {
			return nil, apperr.Errorf(apperr.CodeValidationFailed,
				"%s 最多 %d 个字符", label, r.MaxLen)
		}
		return s, nil

	case "int":
		f, ok := toFloat(raw)
		if !ok {
			return nil, apperr.Errorf(apperr.CodeValidationFailed, "%s 必须是整数", label)
		}
		if f != math.Trunc(f) {
			return nil, apperr.Errorf(apperr.CodeValidationFailed, "%s 必须是整数（收到 %v）", label, f)
		}
		if err := checkRange(r, f, label); err != nil {
			return nil, err
		}
		return int64(f), nil

	case "number":
		f, ok := toFloat(raw)
		if !ok {
			return nil, apperr.Errorf(apperr.CodeValidationFailed, "%s 必须是数字", label)
		}
		if err := checkRange(r, f, label); err != nil {
			return nil, err
		}
		return f, nil

	case "bool":
		b, ok := raw.(bool)
		if !ok {
			return nil, apperr.Errorf(apperr.CodeValidationFailed, "%s 必须是布尔值", label)
		}
		return b, nil

	default:
		// 规则表里出现未知 value_type 属于**配置错误**，不是用户输入问题。
		// 用 internal 语义的错误让上层映射成 5xx，而不是把锅甩给客户端。
		return nil, fmt.Errorf("post: 规则表里 %s 的字段 %s 使用了未知类型 %q",
			r.Field, r.Field, r.ValueType)
	}
}

func checkRange(r DetailRule, f float64, label string) error {
	if r.MinValue != nil && f < *r.MinValue {
		return apperr.Errorf(apperr.CodeValidationFailed,
			"%s 不能小于 %v", label, *r.MinValue)
	}
	if r.MaxValue != nil && f > *r.MaxValue {
		return apperr.Errorf(apperr.CodeValidationFailed,
			"%s 不能大于 %v", label, *r.MaxValue)
	}
	return nil
}

// toFloat 把 JSON 解出来的数值转成 float64。
//
// encoding/json 默认把数字解成 float64；这里兼容 int/int64 是为了
// 让「程序内部构造的 map」（测试、种子数据）也能直接传。
func toFloat(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// NormalizeDetailJSON 把 detail 序列化成落库用的 JSON 文本。
//
// 排序键是刻意的：同样的数据每次序列化得到**同样的字节**，
// 便于比较、也便于将来做缓存/去重。map 的随机遍历顺序会让
// 每次输出都不同，那会让「内容没变但 updated_at 变了」这类判断失真。
func NormalizeDetailJSON(detail map[string]any) ([]byte, error) {
	if len(detail) == 0 {
		return []byte("{}"), nil
	}
	keys := make([]string, 0, len(detail))
	for k := range detail {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// 手工拼有序对象：encoding/json 对 map 的键排序是稳定行为，
	// 但显式做一次不依赖实现细节。
	ordered := make(map[string]any, len(detail))
	for _, k := range keys {
		ordered[k] = detail[k]
	}
	return json.Marshal(ordered)
}
