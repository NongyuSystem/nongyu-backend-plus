// Package pgstore_test 的埋点域测试 + 两个测试文件共用的辅助函数。
//
// 共用辅助函数放这里而不是各写一份：schema 隔离、时钟、造帖这三件事
// 在帖子与埋点两个文件里都要用，重复写就会漂移。
//
// **命名约定**：所有本文件导出到包内的辅助函数都带 pgTest/PgTest 前缀。
// 外部测试包是「同包不同文件共享符号」的，父代理也在往这个包加测试文件；
// 叫 testBaseTime、contains 这类通用名，迟早会和别人撞成 redeclared。
package pgstore_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"nongyu-backend-plus/internal/content/post"
	"nongyu-backend-plus/internal/platform/pgstore"
	"nongyu-backend-plus/internal/track"
)

// pgTestBaseTime 是所有测试的固定基准时刻。
//
// 用固定值而不是 time.Now()：时间戳参与游标与排序断言，
// 固定基准让失败信息可复现（"期望 2026-01-02T03:04:05Z" 比 "期望某刻" 有用得多）。
var pgTestBaseTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// pgTestClock 返回一个始终给出 t 的时钟，满足 now func() time.Time 形状。
func pgTestClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// mustPgPost 造一条帖子并断言成功。publishedAt 用基准时刻。
func mustPgPost(t *testing.T, repo *pgstore.PostPGRepo, board post.Board, title string, authorID int64) *post.Post {
	t.Helper()
	return mustPgPostAt(t, repo, board, title, authorID, pgTestBaseTime)
}

// mustPgPostAt 造一条指定 publishedAt 的帖子。
func mustPgPostAt(t *testing.T, repo *pgstore.PostPGRepo, board post.Board, title string, authorID int64, publishedAt time.Time) *post.Post {
	t.Helper()
	p := &post.Post{
		Board: board, Title: title, Body: "正文：" + title,
		AuthorID: authorID, PublishedAt: publishedAt,
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("Create(%q): %v", title, err)
	}
	if p.ID == 0 {
		t.Fatalf("Create(%q) 未回填 ID", title)
	}
	return p
}

// pgPostIDs 提取 ID 列表，只为让失败信息可读。
func pgPostIDs(ps []*post.Post) []int64 {
	out := make([]int64, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

// ---- 埋点域测试 ----

// TestTrackPGRepo_BatchInsertIdempotent 验收批量插入区分 inserted / duplicated。
//
// 三段式：
//  1. 首次插入 3 条全新 → inserted=3, duplicated=0；
//  2. 原样再插一次 → inserted=0, duplicated=3（幂等，不报错）；
//  3. 混合（2 条已存在 + 2 条新的）→ inserted=2, duplicated=2。
//
// 第 3 段是关键：它同时验证「同批内的新旧混合」也能各自算对，
// 而这是「先查再插」实现最容易漏的场景。
func TestTrackPGRepo_BatchInsertIdempotent(t *testing.T) {
	pool := newPostTrackPool(t)
	repo := pgstore.NewTrackPGRepo(pool)
	ctx := context.Background()

	newEvent := func(id string) track.Event {
		return track.Event{
			EventID:    id,
			EventType:  track.TypeScreenView,
			EventName:  "HomeScreen",
			ClientTsMs: 1767000000000,
			SessionID:  "sess-1",
			AppVersion: "1.0.0",
			Platform:   track.PlatformAndroid,
			Props:      map[string]any{"tab": "home", "durMs": 120.0},
			RequestID:  "req-1",
			UserID:     42,
			ReceivedAt: pgTestBaseTime,
		}
	}

	// ① 首次全插入。
	inserted, duplicated, err := repo.BatchInsert(ctx, []track.Event{
		newEvent("e1"), newEvent("e2"), newEvent("e3"),
	})
	if err != nil {
		t.Fatalf("首次 BatchInsert: %v", err)
	}
	if inserted != 3 || duplicated != 0 {
		t.Fatalf("首次应 inserted=3 duplicated=0，实际 %d/%d", inserted, duplicated)
	}

	// ② 全重复：不报错，全部计入 duplicated。
	inserted, duplicated, err = repo.BatchInsert(ctx, []track.Event{
		newEvent("e1"), newEvent("e2"), newEvent("e3"),
	})
	if err != nil {
		t.Fatalf("重复 BatchInsert 不应报错：%v", err)
	}
	if inserted != 0 || duplicated != 3 {
		t.Fatalf("重复应 inserted=0 duplicated=3，实际 %d/%d", inserted, duplicated)
	}

	// ③ 混合。
	inserted, duplicated, err = repo.BatchInsert(ctx, []track.Event{
		newEvent("e2"), newEvent("e4"), newEvent("e3"), newEvent("e5"),
	})
	if err != nil {
		t.Fatalf("混合 BatchInsert: %v", err)
	}
	if inserted != 2 || duplicated != 2 {
		t.Fatalf("混合应 inserted=2 duplicated=2，实际 %d/%d", inserted, duplicated)
	}

	// 表里应恰好 5 行（e1..e5）。
	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM track_event`).Scan(&total); err != nil {
		t.Fatalf("数 track_event：%v", err)
	}
	if total != 5 {
		t.Fatalf("track_event 应有 5 行，实际 %d", total)
	}

	// 空批：不报错、计数全 0、不发 SQL。
	if inserted, duplicated, err := repo.BatchInsert(ctx, nil); err != nil || inserted != 0 || duplicated != 0 {
		t.Fatalf("空批应 0/0 且无错，实际 %d/%d err=%v", inserted, duplicated, err)
	}
}

// TestTrackPGRepo_PropsRoundTrip 验收 props JSONB 往返、nil props 落 NULL。
func TestTrackPGRepo_PropsRoundTrip(t *testing.T) {
	pool := newPostTrackPool(t)
	repo := pgstore.NewTrackPGRepo(pool)
	ctx := context.Background()

	_, _, err := repo.BatchInsert(ctx, []track.Event{
		{
			EventID: "p1", EventType: track.TypeButtonClick, EventName: "submit",
			Platform: track.PlatformIOS, UserID: 1, ReceivedAt: pgTestBaseTime,
			Props: map[string]any{"buttonId": "btn-ok", "count": 3.0},
		},
		{
			EventID: "p2", EventType: track.TypeAppOpen, EventName: "cold",
			Platform: track.PlatformWeb, UserID: 1, ReceivedAt: pgTestBaseTime,
			Props: nil, // 显式无 props
		},
	})
	if err != nil {
		t.Fatalf("BatchInsert: %v", err)
	}

	var raw string
	if err := pool.QueryRow(ctx,
		`SELECT props::text FROM track_event WHERE event_id = 'p1'`).Scan(&raw); err != nil {
		t.Fatalf("读 p1 props：%v", err)
	}
	if raw == "" || raw == "null" {
		t.Fatalf("p1 props 不应为空，实际 %q", raw)
	}
	// jsonb 会重排键序，按子串断言而不是整串相等。
	if !strings.Contains(raw, `"buttonId": "btn-ok"`) && !strings.Contains(raw, `"buttonId":"btn-ok"`) {
		t.Fatalf("p1 props 丢了 buttonId：%s", raw)
	}

	var isNull bool
	if err := pool.QueryRow(ctx,
		`SELECT props IS NULL FROM track_event WHERE event_id = 'p2'`).Scan(&isNull); err != nil {
		t.Fatalf("读 p2 props：%v", err)
	}
	if !isNull {
		t.Fatalf("nil props 应落成 SQL NULL")
	}
}

// TestTrackPGRepo_CountByTypeWindow 验收 CountByType 按 received_at 时间窗分组。
//
// 造数刻意让「窗口内/窗口外」交错，并让同一类型跨在窗口边界上：
// 边界是闭区间 [from, to]，落在 from 与 to 上的必须被计入。
func TestTrackPGRepo_CountByTypeWindow(t *testing.T) {
	pool := newPostTrackPool(t)
	repo := pgstore.NewTrackPGRepo(pool)
	ctx := context.Background()

	from := pgTestBaseTime
	to := pgTestBaseTime.Add(10 * time.Minute)

	// 窗口内：screen_view ×2（其中一条正好在 from）、button_click ×1（正好在 to）。
	// 窗口外：screen_view ×1（from 前一秒）、crash ×1（to 后一秒）。
	events := []track.Event{
		pgTestEvent("w1", track.TypeScreenView, from),
		pgTestEvent("w2", track.TypeScreenView, from.Add(5*time.Minute)),
		pgTestEvent("w3", track.TypeButtonClick, to),
		pgTestEvent("out1", track.TypeScreenView, from.Add(-time.Second)),
		pgTestEvent("out2", track.TypeCrash, to.Add(time.Second)),
	}
	if inserted, _, err := repo.BatchInsert(ctx, events); err != nil || inserted != 5 {
		t.Fatalf("BatchInsert: inserted=%d err=%v", inserted, err)
	}

	got, err := repo.CountByType(ctx, from, to)
	if err != nil {
		t.Fatalf("CountByType: %v", err)
	}
	if got[track.TypeScreenView] != 2 {
		t.Fatalf("窗口内 screen_view 应为 2（含 from 边界的那条），实际 %d", got[track.TypeScreenView])
	}
	if got[track.TypeButtonClick] != 1 {
		t.Fatalf("窗口内 button_click 应为 1（含 to 边界的那条），实际 %d", got[track.TypeButtonClick])
	}
	if n, ok := got[track.TypeCrash]; ok && n != 0 {
		t.Fatalf("窗口外 crash 不应被计入，实际 %d", n)
	}
	if len(got) != 2 {
		t.Fatalf("结果应只含 2 个类型，实际 %v", got)
	}

	// 空窗口：结果为空 map（不是 nil，调用方会直接遍历）。
	empty, err := repo.CountByType(ctx, pgTestBaseTime.Add(100*time.Hour), pgTestBaseTime.Add(101*time.Hour))
	if err != nil {
		t.Fatalf("空窗口 CountByType: %v", err)
	}
	if empty == nil {
		t.Fatalf("空窗口应返回空 map 而非 nil")
	}
	if len(empty) != 0 {
		t.Fatalf("空窗口应无计数，实际 %v", empty)
	}
}

// pgTestEvent 造一条最小可用事件。
func pgTestEvent(id string, typ track.EventType, receivedAt time.Time) track.Event {
	return track.Event{
		EventID: id, EventType: typ, EventName: "n",
		Platform: track.PlatformAndroid, UserID: 1, ReceivedAt: receivedAt,
	}
}
