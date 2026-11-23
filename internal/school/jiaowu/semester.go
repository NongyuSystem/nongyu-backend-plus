package jiaowu

import (
	"hash/fnv"
	"strconv"
	"strings"
	"time"
)

// 稳定类缓存的「学期末失效」策略（2026-11-23）。
//
// 背景：课表/成绩/培养方案等一学期内基本不变，按 1 小时新鲜度窗口反复
// cache_miss、让全校客户端每隔一小时重取一次是浪费。改为：
//
//   - 稳定类写入后在「整个学期内」都算命中，不按 TTL 判 miss；
//   - 到学期末切换日 + 每用户哈希偏移（0~7 天）后，才判 miss 让客户端重取；
//   - 偏移把全校用户摊到 8 天里逐步失效，避免同一时刻集体 cache_miss 打学校站
//     （这是防缓存雪崩，不是防穿透）。
//
// 失效点不写进缓存 entry，而是读时由 FetchedAt + userID 动态推算：
// 这样已存在的旧缓存无需迁移，自动套用新策略。

const semesterSpreadDays = 7 // 偏移范围 0..7 天

// stableDataTypes 是「一学期内不变」的数据类型集合（选项1 的 6 类）。
// 其它类型（exam/notice/...）仍走 dataTypeSpec 里的 TTL 新鲜度窗口。
var stableDataTypes = map[DataType]bool{
	DataTypeCourse:   true, // 课表
	DataTypeScore:    true, // 成绩
	DataTypeProgress: true, // 进度
	DataTypeRank:     true, // 排名
	DataTypePlan:     true, // 培养方案
	DataTypePersonal: true, // 个人档案
}

// IsStableDataType 判断是否为「学期末失效」的稳定类。
func IsStableDataType(dt DataType) bool { return stableDataTypes[dt] }

// semesterEnds 是解析后的学期末切换日（月-日），按一年中的顺序不重要——
// 计算时取「在基准时间之后、最近的那一个」。
type semesterEnd struct {
	month time.Month
	day   int
}

// parseSemesterEndDates 解析 "07-10,01-10" 形式。空串用默认值。
// 非法项直接忽略；全部非法时回落到默认值（宁可用默认，也不要让缓存永不失效）。
func parseSemesterEndDates(raw string) []semesterEnd {
	if strings.TrimSpace(raw) == "" {
		raw = "07-10,01-10"
	}
	var out []semesterEnd
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		segs := strings.Split(part, "-")
		if len(segs) != 2 {
			continue
		}
		m, err1 := strconv.Atoi(segs[0])
		d, err2 := strconv.Atoi(segs[1])
		if err1 != nil || err2 != nil || m < 1 || m > 12 || d < 1 || d > 31 {
			continue
		}
		out = append(out, semesterEnd{month: time.Month(m), day: d})
	}
	if len(out) == 0 {
		out = []semesterEnd{{month: 7, day: 10}, {month: 1, day: 10}}
	}
	return out
}

// userOffsetDays 用 userID 哈希算出 0..7 天的固定偏移。
// 同一用户总是同一偏移（无状态、可预测、不随重启变化），不同用户摊开分布。
func userOffsetDays(userID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(userID))
	return int(h.Sum32() % (semesterSpreadDays + 1))
}

// nextSemesterEnd 返回 strictlyAfter 之后（含当天的 00:00 之后）最近的一个学期末时刻。
// 年份按 t 的时区构造，比较用 time.Time。
func nextSemesterEnd(t time.Time, ends []semesterEnd) time.Time {
	loc := t.Location()
	year := t.Year()
	var best time.Time
	for _, e := range ends {
		candidate := time.Date(year, e.month, e.day, 0, 0, 0, 0, loc)
		// candidate <= t 时看明年（这样「当天 00:00 刚过」也算已过，取下一学期末）。
		if !candidate.After(t) {
			candidate = time.Date(year+1, e.month, e.day, 0, 0, 0, 0, loc)
		}
		if best.IsZero() || candidate.Before(best) {
			best = candidate
		}
	}
	return best
}

// stableCacheExpired 判断稳定类缓存是否到了「学期末 + 用户偏移」失效点。
//
// 逻辑：以 FetchedAt 为基准找到它写入后遇到的第一个学期末，再加 userID 偏移天数；
// 当前时间 now 越过这个点即判过期（触发 cache_miss）。
//
// 为什么以 FetchedAt 而不是 now 找学期末：一份缓存属于它写入的那个学期，
// 应当在「那个学期的期末」失效；用 now 会在跨学期后永远指向下一个期末、永不失效。
func stableCacheExpired(now, fetchedAt time.Time, userID string, ends []semesterEnd) bool {
	if len(ends) == 0 {
		return false
	}
	if fetchedAt.After(now) {
		fetchedAt = now // 时钟异常兜底
	}
	endTime := nextSemesterEndStrictlyAfter(fetchedAt, ends)
	expireAt := endTime.Add(time.Duration(userOffsetDays(userID)) * 24 * time.Hour)
	return !now.Before(expireAt)
}

// nextSemesterEndStrictlyAfter 与 nextSemesterEnd 相同语义，单独命名强调
// 「找 fetchedAt 之后的第一个学期末」。fetchedAt 正好等于某学期末 00:00 时取下一个。
func nextSemesterEndStrictlyAfter(t time.Time, ends []semesterEnd) time.Time {
	return nextSemesterEnd(t, ends)
}
