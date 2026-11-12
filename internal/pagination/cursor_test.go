package pagination_test

import (
	"encoding/base64"
	"testing"
	"time"

	"nongyu-backend-plus/internal/pagination"
)

func TestEncodeDecode_RoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 12, 12, 0, 0, 123456789, time.UTC)
	in := pagination.Cursor{Time: at, ID: 42}

	got, ok := pagination.Decode(pagination.Encode(in))
	if !ok {
		t.Fatal("自身编码必须能解回")
	}
	if !got.Time.Equal(in.Time) {
		t.Fatalf("时间不一致：%v vs %v", got.Time, in.Time)
	}
	if got.ID != in.ID {
		t.Fatalf("ID 不一致：%d vs %d", got.ID, in.ID)
	}
}

// TestEncodeDecode_KeepsSubMillisecondPrecision 是本包存在的最重要理由。
//
// 早先公告域用 UnixMilli，PG timestamptz 是微秒精度，同一毫秒内多行在
// 翻页时被整批跳过（静默漏条）。这条用例锁住纳秒精度不再退化。
func TestEncodeDecode_KeepsSubMillisecondPrecision(t *testing.T) {
	base := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	// 三条相隔 400 微秒，全部落在同一毫秒内。
	for i := 0; i < 3; i++ {
		at := base.Add(time.Duration(i) * 400 * time.Microsecond)
		in := pagination.Cursor{Time: at, ID: int64(i + 1)}
		got, ok := pagination.Decode(pagination.Encode(in))
		if !ok {
			t.Fatal("应能解码")
		}
		if !got.Time.Equal(at) {
			t.Fatalf("亚毫秒精度丢失：写 %v 读回 %v（用 UnixMilli 就会这样）", at, got.Time)
		}
	}
}

func TestDecode_EmptyMeansFirstPage(t *testing.T) {
	c, ok := pagination.Decode("")
	if !ok {
		t.Fatal("空串应视为第一页而不是错误")
	}
	if !c.Zero() {
		t.Fatal("空串应解出零值游标")
	}
}

func TestDecode_RejectsMalformed(t *testing.T) {
	bad := []string{
		"!!!not-base64!!!",
		"bm90LWEtY3Vyc29y", // base64 合法但不含分隔符
		"YWJjfGRlZg",       // "abc|def"：数字部分非法
	}
	for _, raw := range bad {
		if _, ok := pagination.Decode(raw); ok {
			t.Fatalf("应拒绝非法游标：%s", raw)
		}
	}
}

func TestZero(t *testing.T) {
	if !(pagination.Cursor{}).Zero() {
		t.Fatal("空结构应判为零值")
	}
	if (pagination.Cursor{ID: 1}).Zero() {
		t.Fatal("有 ID 不该判为零值")
	}
	if (pagination.Cursor{Time: time.Now()}).Zero() {
		t.Fatal("有时间不该判为零值")
	}
}

// TestEncodeDecode_KeepsFlag 锁住三元组游标（公告的 pinned 维度）。
//
// 抽取共享游标时曾把公告的三元组压成二元组，结果丢掉 Pinned，
// 表现为「翻到下一页时置顶帖又重新出现」。这条用例防复发。
func TestEncodeDecode_KeepsFlag(t *testing.T) {
	for _, flag := range []bool{true, false} {
		in := pagination.Cursor{
			Time: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC),
			ID:   7, Flag: flag,
		}
		got, ok := pagination.Decode(pagination.Encode(in))
		if !ok {
			t.Fatalf("应能解码 (flag=%v)", flag)
		}
		if got.Flag != flag {
			t.Fatalf("Flag 丢失：写 %v 读回 %v", flag, got.Flag)
		}
	}
}

// TestDecode_AcceptsLegacyTwoSegmentCursor 保证灰度期旧游标仍可用。
//
// 客户端可能还持有只有 "<ns>|<id>" 两段的旧游标；直接判非法会让这些用户的
// 分页突然报错。缺第三段时按 flag=false 处理。
func TestDecode_AcceptsLegacyTwoSegmentCursor(t *testing.T) {
	raw := base64.RawURLEncoding.EncodeToString([]byte("1700000000000000000|42"))
	c, ok := pagination.Decode(raw)
	if !ok {
		t.Fatal("两段旧游标应仍可解析")
	}
	if c.ID != 42 {
		t.Fatalf("ID 应解析为 42，得到 %d", c.ID)
	}
	if c.Flag {
		t.Fatal("缺第三段时 Flag 应为 false")
	}
}
