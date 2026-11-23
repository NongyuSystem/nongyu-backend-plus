// Package classroom 是「教室楼栋地址」域。
//
// 需求要点（2026-11-23 维护者）：
//   - 粒度是**楼栋**而非具体教室：`10-B201` 与 `10-B213` 同为「十教B」，
//     `1-404` 与 `1-301` 同为一教；
//   - 楼栋由管理员添加（管理端 CRUD）；
//   - 楼栋可挂多条「进入路线」，路线是**点状经纬度 + 每点说明**；
//   - 客户端不存死高德 key，由密钥池下发（见 apikey 域）。
//
// 本包只依赖 apperr 与 platform，不 import server / app / identity。
package classroom

import (
	"strings"
)

// ParseKey 从教室名（或楼栋号）提取**楼栋键**。
//
// 例子（需求里给的原始例子）：
//
//	"10-B201" → "10-B"   （十教 B 栋）
//	"10-B213" → "10-B"
//	"1-404"   → "1"      （一教）
//	"1-301"   → "1"
//	"10-b201" → "10-B"   （字母统一大写）
//	"10"      → "10"     （只给楼栋号也能查）
//
// 规则（刻意保持简单可预测，宁可有少数漏判也不要误判）：
//  1. 去掉首尾空白；
//  2. 取第一个 `-` 之前的部分作为「楼栋号」；
//  3. 若 `-` 之后紧跟一个字母，该字母（大写）就是「分区」；
//  4. 结果是 `<楼栋号>` 或 `<楼栋号>-<分区>`。
//
// 为什么不用正则一次性解析：规则要能被人读懂并在注释里逐条核对。
// 手写分支虽然啰嗦，但每种输入走哪条路一目了然。
//
// 返回 ok=false 表示这个字符串无法识别出楼栋（如空串、纯字母）。
// 调用方应把它当成「查不到」，而不是猜一个楼栋——猜错的地址
// 比查不到更糟（学生会走到错的楼）。
func ParseKey(raw string) (key string, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	// 全角连字符与中文破折号也当分隔符：用户从文档里复制过来很常见。
	s = strings.NewReplacer("－", "-", "—", "-", "–", "-").Replace(s)

	no, rest, hasDash := strings.Cut(s, "-")

	// 楼栋号必须存在且至少含一个数字。
	// 只允许 ASCII 数字：教务数据里没有中文数字（「十教」是显示名，不是输入）。
	no = strings.TrimSpace(no)
	if !hasDigit(no) {
		return "", false
	}

	// 楼栋号里若混了非字母数字的字符（空格、斜杠），视为无法识别。
	for _, c := range no {
		if !isAlnum(c) {
			return "", false
		}
	}
	no = strings.ToUpper(no)

	if !hasDash {
		return no, true
	}

	// `-` 之后：第一个字符若是字母，就是分区；否则（纯数字，如 `1-404`
	// 的 `404`）没有分区。
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return no, true
	}
	c := rune(rest[0])
	if !isAlpha(c) {
		// `1-404` / `10-201` 这类：无分区。
		return no, true
	}
	return no + "-" + string(toUpper(c)), true
}

// ParseRoomNo 从教室名里取出「房间号」部分（用于展示，不参与匹配）。
//
//	"10-B201" → "B201"
//	"1-404"   → "404"
func ParseRoomNo(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.NewReplacer("－", "-", "—", "-", "–", "-").Replace(s)
	_, rest, has := strings.Cut(s, "-")
	if !has {
		return ""
	}
	return strings.TrimSpace(rest)
}

// NormalizeKey 把管理员录入的楼栋号 + 分区 组装成同一个键格式。
//
// 管理员在管理端填 buildingNo=`10`、zone=`B`，这里产出 `10-B`；
// 填 buildingNo=`1`、zone=“ 产出 `1`。
// 与 ParseKey 的结果格式**必须一致**——否则「录入的键」与「查询算出的键」
// 对不上，功能整体失效。两处共用本函数来保证这一点。
func NormalizeKey(buildingNo, zone string) (string, bool) {
	no := strings.TrimSpace(buildingNo)
	no = strings.ToUpper(no)
	if no == "" || !hasDigit(no) {
		return "", false
	}
	for _, c := range no {
		if !isAlnum(c) {
			return "", false
		}
	}
	z := strings.TrimSpace(zone)
	if z == "" {
		return no, true
	}
	// 分区只接受单个字母。
	rs := []rune(strings.ToUpper(z))
	if len(rs) != 1 || !isAlpha(rs[0]) {
		return "", false
	}
	return no + "-" + string(rs[0]), true
}

func hasDigit(s string) bool {
	for _, c := range s {
		if c >= '0' && c <= '9' {
			return true
		}
	}
	return false
}

func isAlpha(c rune) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isAlnum(c rune) bool {
	return isAlpha(c) || (c >= '0' && c <= '9')
}

func toUpper(c rune) rune {
	if c >= 'a' && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}
