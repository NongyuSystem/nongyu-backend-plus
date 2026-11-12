package track

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Repo 是埋点的持久化接口。
//
// 接口在域内、实现放 internal/platform/pgstore（README 架构约束第 4 条）。
type Repo interface {
	// BatchInsert 批量写入，按 eventID 幂等。
	// 返回实际新写入条数与重复条数（重复不算失败）。
	BatchInsert(ctx context.Context, events []Event) (inserted, duplicated int, err error)
	// CountByType 返回时间窗内按事件类型的计数。
	CountByType(ctx context.Context, from, to time.Time) (map[EventType]int, error)
}

// BufferOptions 是有界缓冲的参数。
type BufferOptions struct {
	// Capacity 是队列容量（满了就丢，不阻塞）。
	Capacity int
	// BatchSize 是每批写入条数。
	BatchSize int
	// FlushPeriod 是后台定期 flush 间隔。
	FlushPeriod time.Duration
}

// DefaultBufferOptions 是施工期默认值（Spec 把这些列为「pending 数值」）。
func DefaultBufferOptions() BufferOptions {
	return BufferOptions{
		Capacity:    4096,
		BatchSize:   128,
		FlushPeriod: 2 * time.Second,
	}
}

// Buffer 是埋点的有界异步缓冲。
//
// 核心语义（Spec「缓冲满」）：**满了就丢并计数，绝不阻塞、绝不返回 503**。
// 理由：埋点是分析数据，丢几十条远好于让客户端重试风暴打垮服务。
type Buffer struct {
	repo Repo
	opts BufferOptions

	ch      chan Event
	dropped atomic.Int64
	written atomic.Int64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// NewBuffer 构造并启动后台 flush 协程。
func NewBuffer(repo Repo, opts BufferOptions) *Buffer {
	if opts.Capacity <= 0 {
		opts.Capacity = 4096
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 128
	}
	if opts.FlushPeriod <= 0 {
		opts.FlushPeriod = 2 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &Buffer{
		repo: repo, opts: opts,
		ch:  make(chan Event, opts.Capacity),
		ctx: ctx, cancel: cancel,
	}
	b.wg.Add(1)
	go b.run()
	return b
}

// Enqueue 尝试入队，**永不阻塞**。
//
// 队列满时直接丢弃并计数——这是 Spec 明确要求的行为，
// 不是「实现不完善」。
func (b *Buffer) Enqueue(ev Event) {
	select {
	case b.ch <- ev:
	default:
		b.dropped.Add(1)
	}
}

// Dropped 返回累计丢弃条数（观测用）。
func (b *Buffer) Dropped() int64 { return b.dropped.Load() }

// Written 返回累计成功写入条数。
func (b *Buffer) Written() int64 { return b.written.Load() }

func (b *Buffer) run() {
	defer b.wg.Done()
	ticker := time.NewTicker(b.opts.FlushPeriod)
	defer ticker.Stop()

	batch := make([]Event, 0, b.opts.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		// 用独立 context：关停时传入的 ctx 可能已 cancel，
		// 但我们已经拿到的数据应当尽力写完。
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		n, _, err := b.repo.BatchInsert(ctx, batch)
		cancel()
		if err == nil {
			b.written.Add(int64(n))
		} else {
			// 写失败整批丢弃并计数：埋点不值得重试到把内存吃满。
			b.dropped.Add(int64(len(batch)))
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-b.ctx.Done():
			// 关停：把 channel 里剩的排空后最后 flush 一次。
			for {
				select {
				case ev := <-b.ch:
					batch = append(batch, ev)
					if len(batch) >= b.opts.BatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case ev := <-b.ch:
			batch = append(batch, ev)
			if len(batch) >= b.opts.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// Flush 停止后台协程并把剩余数据写完（关停前调用，幂等）。
func (b *Buffer) Flush() {
	b.once.Do(func() {
		b.cancel()
		b.wg.Wait()
	})
}
