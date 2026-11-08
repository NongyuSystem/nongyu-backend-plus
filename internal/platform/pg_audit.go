package platform

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGOptions 是 Postgres 审计存储的配置。
type PGOptions struct {
	// DSN 形如 postgres://user:pass@host:port/db?sslmode=disable
	DSN string
	// BufferSize 是异步缓冲容量；满后丢弃新记录（审计宁可丢也不能挡住业务）。
	BufferSize int
	// BatchSize 是一次批量插入的最大条数。
	BatchSize int
	// FlushPeriod 是后台批写周期。
	FlushPeriod time.Duration
}

// PGAuditStore 是 AuditStore 的 Postgres 实现。
//
// 设计要点：
//   - Append 只做「入缓冲」，永不阻塞业务线程；
//   - 缓冲满时丢弃并计数（Dropped），而不是让登录因为审计写不进去而失败；
//   - 后台按 FlushPeriod 批量 INSERT，一次提交多条，减少往返。
type PGAuditStore struct {
	pool  *pgxpool.Pool
	opts  PGOptions
	ch    chan AuditEntry
	drops atomic.Int64

	closeOnce sync.Once
	done      chan struct{}
	stopCh    chan struct{}
	flushMu   sync.Mutex
}

// NewPGAuditStore 构造审计存储并启动后台批写协程。
func NewPGAuditStore(opts PGOptions) (*PGAuditStore, error) {
	if opts.DSN == "" {
		return nil, fmt.Errorf("platform: PGAuditStore 需要 DSN")
	}
	if opts.BufferSize <= 0 {
		opts.BufferSize = 1024
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 64
	}
	if opts.FlushPeriod <= 0 {
		opts.FlushPeriod = time.Second
	}

	cfg, err := pgxpool.ParseConfig(opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("platform: 解析 DSN 失败：%w", err)
	}
	// 快速失败：数据库不可用不应把请求拖到秒级。
	cfg.MaxConns = 4
	cfg.ConnConfig.ConnectTimeout = 2 * time.Second

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, err
	}

	s := &PGAuditStore{
		pool:   pool,
		opts:   opts,
		ch:     make(chan AuditEntry, opts.BufferSize),
		done:   make(chan struct{}),
		stopCh: make(chan struct{}),
	}
	go s.loop()
	return s, nil
}

// Ping 探测连通性（装配层启动期调用）。
func (s *PGAuditStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Append 投递一条审计。永不阻塞：缓冲满则丢弃并计数。
func (s *PGAuditStore) Append(_ context.Context, entry AuditEntry) error {
	select {
	case s.ch <- entry:
		return nil
	default:
		s.drops.Add(1)
		return nil
	}
}

// Dropped 返回因缓冲满被丢弃的条数。
func (s *PGAuditStore) Dropped() int64 { return s.drops.Load() }

func (s *PGAuditStore) loop() {
	defer close(s.done)
	ticker := time.NewTicker(s.opts.FlushPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			s.drainAndWrite()
			return
		case <-ticker.C:
			s.drainAndWrite()
		}
	}
}

// drainAndWrite 在写锁内完成「从 channel 取到写库」的整段，
// 保证 Flush（同样持锁）不会与后台协程交错，从而不丢记录。
func (s *PGAuditStore) drainAndWrite() {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	batch := make([]AuditEntry, 0, s.opts.BatchSize)
	for {
		select {
		case e := <-s.ch:
			batch = append(batch, e)
			if len(batch) >= s.opts.BatchSize {
				s.writeBatchLocked(batch)
				batch = batch[:0]
			}
		default:
			if len(batch) > 0 {
				s.writeBatchLocked(batch)
			}
			return
		}
	}
}

// writeBatchLocked 一次批量插入，调用方须已持有 flushMu。
// 失败只把该批计入丢弃数，绝不 panic、绝不阻塞业务。
func (s *PGAuditStore) writeBatchLocked(batch []AuditEntry) {
	if len(batch) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 用多行 VALUES 一次提交，减少往返。
	sql := `INSERT INTO audit_log (request_id, user_id, action, target, result, occurred_at) VALUES `
	args := make([]any, 0, len(batch)*6)
	for i, e := range batch {
		if i > 0 {
			sql += ", "
		}
		base := i * 6
		sql += fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6)
		args = append(args, e.RequestID, e.UserID, e.Action, e.Target, e.Result, e.OccurredAt)
	}
	if _, err := s.pool.Exec(ctx, sql, args...); err != nil {
		s.drops.Add(int64(len(batch)))
	}
}

// Flush 把当前缓冲同步落库（测试与关停前使用）。
// 与后台协程共用同一把写锁，因此不会出现「后台已取走但未写」的丢失窗口。
func (s *PGAuditStore) Flush(ctx context.Context) error {
	s.drainAndWrite()
	return s.pool.Ping(ctx)
}

// Close 停止后台协程并释放连接池。
func (s *PGAuditStore) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopCh)
		<-s.done
		s.pool.Close()
	})
	return nil
}

// QueryAuditByRequestID 按流水号查询审计记录（测试与运维排障用）。
func QueryAuditByRequestID(ctx context.Context, dsn, requestID string) ([]AuditEntry, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer pool.Close()

	rows, err := pool.Query(ctx,
		`SELECT request_id, user_id, action, target, result, occurred_at
		   FROM audit_log WHERE request_id = $1 ORDER BY id`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.RequestID, &e.UserID, &e.Action, &e.Target, &e.Result, &e.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
