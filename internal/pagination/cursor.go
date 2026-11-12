// Package pagination 提供全仓共用的「游标」编解码。
//
// 为什么单独抽一个包（而不是三个域各写一份）：content/announcement、
// content/post、message 曾有各自的 encodeCursor / decodeCursor，
// 逻辑高度重复却要修 bug 时改三遍。更危险的是**精度这件事已经被踩过一次**：
// 早先公告域用 UnixMilli，PG timestamptz 是微秒精度，同毫秒多行在翻页时
// 被整批跳过（静默漏条）；修的时候只在公告域修，另两处是后来各自照抄的，
// 只要有人再写第四个域，同样的坑会第三次出现。
//
// 设计取舍：公告的排序键是**三元组**（pinned, publishedAt, id），
// 帖子与消息是二元组（时间, id）。所以这里保留一个可选的 Flag 维度，
// 而不是把三元组拆成两种编码——同一份编码格式才能保证「非法游标」的
// 判定口径也一致。
package pagination

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// Cursor 是游标。
//
// Time + ID 是所有域都有的；Flag 承载个别域额外的排序维度
// （当前只有公告的 pinned）。零值 Flag 表示该域不用这个维度。
type Cursor struct {
	Time time.Time
	ID   int64
	// Flag 是额外的布尔排序维度（公告用 pinned）。
	Flag bool
}

// Zero 判断游标是否为零值（表示「从最新开始」）。
func (c Cursor) Zero() bool { return c.ID == 0 && c.Time.IsZero() }

// Encode 把游标编成不透明串。
//
// 格式：base64url("<unixNano>|<id>|<0|1>")。
//
// **必须用 UnixNano**：PG timestamptz 是微秒精度，用毫秒编码会让同一毫秒内
// 的多行在翻页时被整批跳过。这是本项目已经踩过的真实 bug。
func Encode(c Cursor) string {
	flag := "0"
	if c.Flag {
		flag = "1"
	}
	raw := strconv.FormatInt(c.Time.UnixNano(), 10) + "|" +
		strconv.FormatInt(c.ID, 10) + "|" + flag
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// Decode 解析游标；空串返回零值（第一页）。
//
// 任何格式问题都返回 ok=false，由调用方映射成自己的 invalid_cursor 错误码。
// 这里不返回 error 类型是为了不让本包依赖业务域的错误包。
//
// 兼容性说明：第三段（flag）缺失时按 false 处理。这样早先只有
// "<ns>|<id>" 两段的旧游标仍然可用——客户端在灰度期间可能还持有旧游标，
// 直接判非法会让这些用户的分页突然报错。
func Decode(raw string) (Cursor, bool) {
	if raw == "" {
		return Cursor{}, true
	}
	dec, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, false
	}
	parts := strings.Split(string(dec), "|")
	if len(parts) != 2 && len(parts) != 3 {
		return Cursor{}, false
	}
	ns, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return Cursor{}, false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return Cursor{}, false
	}
	flag := false
	if len(parts) == 3 {
		switch parts[2] {
		case "0":
			flag = false
		case "1":
			flag = true
		default:
			return Cursor{}, false
		}
	}
	return Cursor{Time: time.Unix(0, ns).UTC(), ID: id, Flag: flag}, true
}
