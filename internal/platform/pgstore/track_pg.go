package pgstore

// 埋点域的 PostgreSQL 仓储。
//
// 与 TrackMemoryRepo 同一接口（track.Repo）。两处语义必须对齐：
//   - **eventId 幂等**：重复 id 不失败，只计入 duplicated；
//   - **埋点写失败不重试**：Buffer 侧已经在失败时整批丢弃并计数，本层不再加重试。
//
// 为什么 duplicated 用「提交条数 - 实际插入条数」算，而不是查一遍再插：
// 埋点是高频旁路数据，多一次往返就多一次把数据库当查重服务用的开销；
// 而且「先查再插」在并发下仍会漏（另一个进程刚插进来）。
// INSERT ... ON CONFLICT DO NOTHING 让数据库一次给出答案。

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/track"
)

// TrackPGRepo 是 track.Repo 的 PostgreSQL 实现。
//
// 没有 now 注入：ReceivedAt 由服务层在 Sanitize 之后填（track.Event.ReceivedAt），
// 本层只负责原样落库。若这里再取一次时钟，同一批事件会出现两个时间戳，
// 「同批事件 received_at 相同」这条分析前提就被破坏了。
type TrackPGRepo struct {
	pool *pgxpool.Pool
}

// NewTrackPGRepo 构造 PG 仓储。
func NewTrackPGRepo(pool *pgxpool.Pool) *TrackPGRepo {
	return &TrackPGRepo{pool: pool}
}

// BatchInsert 实现 track.Repo：一条 SQL 批量插入，按 eventID 幂等。
//
// 实现方式是 **unnest 多数组** 而不是 pgx.Batch：
//
//   - pgx.Batch 发的是 N 条独立 INSERT，要么 N 次往返，要么在隐式事务里逐条执行，
//     并且每条的 RowsAffected 要自己累加，多一层易错代码；
//   - `INSERT ... SELECT ... FROM unnest($1::text[], $2::text[], ...)` 只发**一条**
//     SQL、一个往返，RowsAffected 天然就是「真正插进去的行数」。
//
// 幂等靠 ON CONFLICT (event_id) DO NOTHING，于是：
//
//	inserted   = CommandTag.RowsAffected()   // 冲突行不计数
//	duplicated = len(events) - inserted      // 提交条数减去真正落库的
//
// 注意 props 是 jsonb：先 json.Marshal 成 []byte 再以 text[] 传，SQL 里 ::jsonb[]。
// 直接把 map 放进数组参数会被 pgx 当成 unknown，能否编码取决于版本，
// 显式序列化更可靠（且 nil props 会落成 SQL NULL，与列可空一致）。
func (r *TrackPGRepo) BatchInsert(ctx context.Context, events []track.Event) (int, int, error) {
	if len(events) == 0 {
		return 0, 0, nil
	}

	n := len(events)
	eventIDs := make([]string, n)
	eventTypes := make([]string, n)
	eventNames := make([]string, n)
	clientTsMs := make([]int64, n)
	sessionIDs := make([]string, n)
	appVersions := make([]string, n)
	platforms := make([]string, n)
	props := make([]*string, n)
	requestIDs := make([]string, n)
	userIDs := make([]int64, n)
	receivedAts := make([]any, n)

	for i, ev := range events {
		eventIDs[i] = ev.EventID
		eventTypes[i] = string(ev.EventType)
		eventNames[i] = ev.EventName
		clientTsMs[i] = ev.ClientTsMs
		sessionIDs[i] = ev.SessionID
		appVersions[i] = ev.AppVersion
		platforms[i] = string(ev.Platform)
		requestIDs[i] = ev.RequestID
		userIDs[i] = ev.UserID
		receivedAts[i] = ev.ReceivedAt

		if ev.Props != nil {
			b, err := json.Marshal(ev.Props)
			if err != nil {
				// props 序列化失败只丢这一个字段，不让整批失败：
				// 埋点宁可少一个 props 也不能丢整批 128 条。
				props[i] = nil
				continue
			}
			s := string(b)
			props[i] = &s
		}
	}

	tag, err := r.pool.Exec(ctx, `
		INSERT INTO track_event (
			event_id, event_type, event_name, client_ts_ms, session_id,
			app_version, platform, props, request_id, user_id, received_at)
		SELECT t.event_id, t.event_type, t.event_name, t.client_ts_ms, t.session_id,
		       t.app_version, t.platform, t.props_text::jsonb, t.request_id, t.user_id,
		       t.received_at
		  FROM unnest(
			$1::text[], $2::text[], $3::text[], $4::bigint[], $5::text[],
			$6::text[], $7::text[], $8::text[], $9::text[], $10::bigint[],
			$11::timestamptz[])
			AS t(event_id, event_type, event_name, client_ts_ms, session_id,
			     app_version, platform, props_text, request_id, user_id, received_at)
		ON CONFLICT (event_id) DO NOTHING`,
		eventIDs, eventTypes, eventNames, clientTsMs, sessionIDs,
		appVersions, platforms, props, requestIDs, userIDs, receivedAts)
	if err != nil {
		return 0, 0, err
	}

	inserted := int(tag.RowsAffected())
	duplicated := n - inserted
	return inserted, duplicated, nil
}

// CountByType 实现 track.Repo：时间窗内按 event_type 分组计数。
//
// 时间窗按 received_at（服务端接收时刻），不按 client_ts_ms：
// 客户端时钟可被篡改或有偏差，用它做窗口会让统计结果随用户设备时间漂移。
// 边界是闭区间 [from, to]，与内存实现的 Before/After 判定同一口径。
func (r *TrackPGRepo) CountByType(ctx context.Context, from, to time.Time) (map[track.EventType]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT event_type, COUNT(*)
		  FROM track_event
		 WHERE received_at >= $1 AND received_at <= $2
		 GROUP BY event_type`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[track.EventType]int{}
	for rows.Next() {
		var typ string
		var n int
		if err := rows.Scan(&typ, &n); err != nil {
			return nil, err
		}
		out[track.EventType(typ)] = n
	}
	return out, rows.Err()
}

// 编译期确认。
var _ track.Repo = (*TrackPGRepo)(nil)
