package track

import (
	"context"
	"time"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/platform"
)

// Options 是构造 Service 的依赖。
type Options struct {
	// Repo 必填。
	Repo Repo
	// Clock 可为 nil，默认真实时钟。
	Clock platform.Clock
	// BufferOptions 覆盖默认缓冲参数。
	BufferOptions BufferOptions
}

// Service 是埋点业务逻辑。不感知 HTTP。
type Service struct {
	repo   Repo
	clock  platform.Clock
	buffer *Buffer
}

// New 构造 Service 并启动缓冲。
func New(opts Options) *Service {
	if opts.Repo == nil {
		panic("track: Options.Repo 必填")
	}
	if opts.Clock == nil {
		opts.Clock = platform.NewRealClock()
	}
	bo := opts.BufferOptions
	if bo.Capacity == 0 {
		bo = DefaultBufferOptions()
	}
	return &Service{
		repo:   opts.Repo,
		clock:  opts.Clock,
		buffer: NewBuffer(opts.Repo, bo),
	}
}

// ReportResult 是一次批量上报的结果。
//
// 三个计数合起来 = 提交条数：accepted + duplicated + rejected。
// 单条失败**不整批拒**——这样一条脏数据不会让整批 99 条好数据一起丢。
type ReportResult struct {
	Accepted   int `json:"accepted"`
	Duplicated int `json:"duplicated"`
	Rejected   int `json:"rejected"`
	// Dropped 是缓冲区满而丢弃的条数（Spec：不返回 503，只计数）。
	Dropped int `json:"dropped"`
}

// Report 处理一次批量上报。
//
// 参数 items 是客户端原始 JSON 对象数组（未脱敏）。
// userID 由 handler 从会话取得，**覆盖**客户端自报的任何 userId。
func (s *Service) Report(ctx context.Context, userID int64, requestID string, items []map[string]any) (ReportResult, error) {
	if userID <= 0 {
		return ReportResult{}, apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份")
	}
	if len(items) == 0 {
		return ReportResult{}, apperr.Errorf(apperr.CodeValidationFailed, "事件列表为空")
	}
	if len(items) > MaxBatch {
		return ReportResult{}, apperr.Errorf(apperr.CodeValidationFailed,
			"单次最多 %d 条，得到 %d 条", MaxBatch, len(items))
	}

	var res ReportResult
	now := s.clock.Now()
	for _, raw := range items {
		sanitized, err := Sanitize(raw)
		if err != nil {
			// 单条失败：计入 rejected，继续处理下一条。
			res.Rejected++
			continue
		}
		ev := sanitized.Event
		// 服务端注入，覆盖客户端任何自报值。
		ev.UserID = userID
		ev.RequestID = requestID
		ev.ReceivedAt = now

		s.buffer.Enqueue(ev)
		res.Accepted++
	}
	// 入队可能因满而被丢，这里把累计值反映出去。
	res.Dropped = int(s.buffer.Dropped())
	return res, nil
}

// Summary 返回时间窗内按事件类型的计数（管理端只读口）。
//
// Spec：**只给按事件类型 + 时间窗的计数**，无 props 明细、无导出。
func (s *Service) Summary(ctx context.Context, from, to time.Time) (map[EventType]int, error) {
	if from.IsZero() || to.IsZero() {
		// 默认最近 24 小时。
		now := s.clock.Now()
		to = now
		from = now.Add(-24 * time.Hour)
	}
	if !to.After(from) {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "时间窗非法")
	}
	return s.repo.CountByType(ctx, from, to)
}

// Flush 把缓冲里剩余事件写完（关停前调用）。
func (s *Service) Flush() { s.buffer.Flush() }

// Dropped 暴露丢弃计数供 /status 观测。
func (s *Service) Dropped() int64 { return s.buffer.Dropped() }

// Written 暴露已写入计数供观测。
func (s *Service) Written() int64 { return s.buffer.Written() }
