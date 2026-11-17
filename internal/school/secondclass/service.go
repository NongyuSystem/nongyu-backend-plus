// Package secondclass 的 Service：只持有缓存、schema 校验器、时钟、日志与审计。
//
// 与教务域的关键差别（规格 §2 / 架构落点草图 §四）：
// 二课后端零出站、零凭据，所以这里**没有** HTTPDoer / TicketStore / Cipher /
// SessionIssuer / WebVPN 票据链。装配层只需要注入缓存与审计等旁路能力。
package secondclass

import (
	"context"
	"io"
	"log/slog"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
)

// Service 是二课子域的服务。进程无状态：副本在缓存层，审计在 PG / slog，
// 配方在编译期常量。凭据 x-access-token 由客户端持有，后端从不接收。
type Service struct {
	cache      platform.CacheStore // 直接用平台层契约（#72 成果），不再自声明接口
	schema     *SchemaValidator
	clock      platform.Clock
	logger     *slog.Logger
	auditStore platform.AuditStore
}

// Options 是构造 Service 的可选依赖。装配层只填自己有的能力；
// 测试可注入内存实现。Cache 或 Schema 为 nil 时缓存端点回 503（见 cache.go）。
type Options struct {
	Cache  platform.CacheStore
	Schema *SchemaValidator
	Clock  platform.Clock
	Logger *slog.Logger
	Audit  platform.AuditStore
}

// New 构造 Service。logger / clock 为 nil 时补安全默认值（不写文件、真实时钟）。
func New(opts Options) *Service {
	clock := opts.Clock
	if clock == nil {
		clock = platform.NewRealClock()
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{
		cache:      opts.Cache,
		schema:     opts.Schema,
		clock:      clock,
		logger:     logger,
		auditStore: opts.Audit,
	}
}

// BindCache / BindSchemaValidator 供装配层在编译 schema 成功后补注入，
// 形状与教务一致（可选依赖降级 gate 由 app 层负责，本包只认是否就绪）。
func (s *Service) BindCache(store platform.CacheStore)      { s.cache = store }
func (s *Service) BindSchemaValidator(v *SchemaValidator)   { s.schema = v }
func (s *Service) BindAuditStore(store platform.AuditStore) { s.auditStore = store }

// audit 只写结构化字段，绝不把票据/密文/GPS/二维码/二课响应体放进任何字段
// （规格 §3.4 / §3.6、ADR-0001 第 4 条）。审计是旁路：写库失败被吞、不影响业务。
//
// 这里的「记审计」只覆盖后端自己经手的交互（配方下发、副本写入、失效），不覆盖
// 客户端直连二课的请求——见 doc.go 包注释里的规格 §3.4 盲区固定措辞：后端对二课
// 全部请求无观测能力，不代发、不转发、不缓存其响应，无法限流、无法记审计、无法判定
// 失败原因；不得声称已限流或已审计。
//
// action 沿用规格定的两个：secondclass.recipe_issue（配方下发）/ secondclass.record
// （副本写入）；失效按 §10.1 ② 扩展第三个 action secondclass.invalidate
// （见 cache.go 的 CacheInvalidate 三处调用）。
func (s *Service) audit(ctx context.Context, userID, action, result, target string) {
	s.logger.Info("school.audit",
		slog.String("userID", userID),
		slog.String("action", action),
		slog.String("result", result),
		slog.String("target", target),
	)
	if s.auditStore == nil {
		return
	}
	reqID := ""
	if ctx != nil {
		if v, ok := ctx.Value(school.CtxRequestID).(string); ok {
			reqID = v
		}
	}
	_ = s.auditStore.Append(ctx, platform.AuditEntry{
		RequestID:  reqID,
		UserID:     userID,
		Action:     action,
		Target:     target,
		Result:     result,
		OccurredAt: s.clock.Now(),
	})
}
