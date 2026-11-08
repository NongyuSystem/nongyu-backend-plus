package login

import (
	"strings"
	"unicode"
)

// 姓名拼音首字母（登录方式④的校验因子）。
//
// 设计取舍（重要）：
// 早期尝试内联手写「汉字→声母」映射表，结果表内大量重复键、覆盖不全，
// 且生僻字无法处理——**用一张手写表做登录校验因子是不可靠的**。
//
// 现改为可注入的 PinyinInitializer：
//   - 装配层可接入完整拼音库（go-pinyin 等），实现全量覆盖；
//   - 默认实现 DefaultInitializer 只处理拉丁字母，遇到汉字返回 ErrNoHanSupport，
//     由业务层 fail-closed（该登录方式对含汉字的姓名暂不可用），
//     而不是算出一个「差不多」的值去比对——那会让校验形同虚设。
//
// 真正的生产路径应当是：登录方式④ 的比对值来自**服务端在学校认证时算好并存进画像的值**，
// 而不是每次现场计算。计算器只是产出该值的手段；产出不了就必须拒绝，不能猜。

// ErrNoHanSupport 表示当前转换器处理不了汉字，无法计算拼音首字母。
var ErrNoHanSupport = errNoHan{}

type errNoHan struct{}

func (errNoHan) Error() string { return "login: 当前拼音转换器不支持汉字" }

// PinyinInitializer 把姓名转成拼音首字母（大写）。
// 处理不了时返回 ErrNoHanSupport，绝不返回猜测值。
type PinyinInitializer interface {
	Initials(name string) (string, error)
}

// DefaultInitializer 只处理拉丁字母姓名，**按词取首字母**。
//
// 边界（必须清楚）："Yao Yifei" 是两个词 → "YY"，不是 "YYF"。
// 要得到"每个汉字一个首字母"的 YYF，需要拼音库把姓名拆成音节
// （Yao / Yi / Fei），这超出本实现的职责。
// 因此生产路径应由装配层注入一个真正的拼音实现（实现 PinyinInitializer），
// 本默认实现只用于「姓名本身就是拉丁词序列」的场景。
type DefaultInitializer struct{}

// Initials 实现 PinyinInitializer。
func (DefaultInitializer) Initials(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	if hasHan(name) {
		return "", ErrNoHanSupport
	}
	// 按空白与常见分隔符分词，每词取首字母。
	fields := strings.FieldsFunc(name, func(r rune) bool {
		return r == ' ' || r == '.' || r == '-' || r == '·' || r == ','
	})
	var b strings.Builder
	for _, f := range fields {
		for _, r := range f {
			if unicode.IsLetter(r) {
				b.WriteString(strings.ToUpper(string(r)))
				break
			}
		}
	}
	return b.String(), nil
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// MapInitializer 是「已知字符→首字母」查表的实现，供装配层注入一个小规模的
// 显式映射（例如学校认证时实际遇到的姓氏集合），不是全量拼音库。
//
// 查表时遇到表外汉字同样返回 ErrNoHanSupport；表外字符不得被跳过，
// 否则算出的字符串会变短，比对结果不可信。
type MapInitializer struct {
	Table map[rune]string
}

// Initials 实现 PinyinInitializer。
func (m MapInitializer) Initials(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	var b strings.Builder
	prevLetter := false
	for _, r := range name {
		switch {
		case unicode.Is(unicode.Han, r):
			init, ok := m.Table[r]
			if !ok {
				return "", ErrNoHanSupport
			}
			b.WriteString(init)
			prevLetter = false
		case unicode.IsLetter(r):
			if !prevLetter {
				b.WriteString(strings.ToUpper(string(r)))
			}
			prevLetter = true
		default:
			prevLetter = false
		}
	}
	return b.String(), nil
}

// NormalizeInitials 归一化用户输入的拼音首字母（大写、去空格与点）。
func NormalizeInitials(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.NewReplacer(" ", "", ".", "", "-", "", "·", "").Replace(s)
}
