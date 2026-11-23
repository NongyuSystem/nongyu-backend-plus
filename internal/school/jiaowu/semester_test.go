package jiaowu

import (
	"testing"
	"time"
)

func TestParseSemesterEndDates(t *testing.T) {
	cases := []struct {
		raw  string
		want []semesterEnd
	}{
		{"", []semesterEnd{{7, 10}, {1, 10}}},
		{"07-10,01-10", []semesterEnd{{7, 10}, {1, 10}}},
		{" 09-01 , 02-15 ", []semesterEnd{{9, 1}, {2, 15}}},
		{"bad,13-01,07-10", []semesterEnd{{7, 10}}},  // 非法项忽略
		{"garbage", []semesterEnd{{7, 10}, {1, 10}}}, // 全非法回落默认
	}
	for _, c := range cases {
		got := parseSemesterEndDates(c.raw)
		if len(got) != len(c.want) {
			t.Errorf("parse(%q) 长度=%d 期望=%d", c.raw, len(got), len(c.want))
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parse(%q)[%d]=%v 期望 %v", c.raw, i, got[i], c.want[i])
			}
		}
	}
}

func TestUserOffsetDaysSpread(t *testing.T) {
	// 一批用户应覆盖多个偏移值（证明能摊开，防雪崩）
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		seen[userOffsetDays(string(rune('a'+i%26))+string(rune('0'+i/26)))] = true
	}
	if len(seen) < 5 {
		t.Errorf("偏移分布过于集中：%v", seen)
	}
	// 同一用户稳定
	u := "202502204"
	a, b := userOffsetDays(u), userOffsetDays(u)
	if a != b || a < 0 || a > semesterSpreadDays {
		t.Errorf("同一用户偏移不稳定或越界：%d %d", a, b)
	}
}

func TestStableCacheExpired(t *testing.T) {
	ends := parseSemesterEndDates("07-10,01-10")
	loc := time.UTC

	// 9 月 1 日写入的课表，到次年 1 月 10 日 + 偏移前都应命中
	fetched := time.Date(2025, 9, 1, 0, 0, 0, 0, loc)
	beforeEnd := time.Date(2026, 1, 9, 0, 0, 0, 0, loc)
	if stableCacheExpired(beforeEnd, fetched, "202502204", ends) {
		t.Error("学期末前不应过期")
	}

	// 学期末 + 8 天（超过最大偏移 7）后必过期
	afterEnd := time.Date(2026, 1, 19, 0, 0, 0, 0, loc)
	if !stableCacheExpired(afterEnd, fetched, "202502204", ends) {
		t.Error("学期末+8天应过期")
	}

	// 偏移边界：同一时刻，不同用户有的过期有的没过期（证明分批）
	boundary := time.Date(2026, 1, 10, 12, 0, 0, 0, loc)
	expiredCount, aliveCount := 0, 0
	for i := 0; i < 100; i++ {
		u := "user-" + string(rune('a'+i))
		if stableCacheExpired(boundary, fetched, u, ends) {
			expiredCount++
		} else {
			aliveCount++
		}
	}
	if expiredCount == 0 || aliveCount == 0 {
		t.Errorf("学期末当天应分批失效，实际 过期=%d 未过期=%d", expiredCount, aliveCount)
	}
}

func TestStableDataTypes(t *testing.T) {
	for _, dt := range []DataType{DataTypeCourse, DataTypeScore, DataTypeProgress, DataTypeRank, DataTypePlan, DataTypePersonal} {
		if !IsStableDataType(dt) {
			t.Errorf("%s 应为稳定类", dt)
		}
	}
	for _, dt := range []DataType{DataTypeExam, DataTypeNotice, DataTypeTeachingNotice, DataTypeCompetition} {
		if IsStableDataType(dt) {
			t.Errorf("%s 不应为稳定类", dt)
		}
	}
}
