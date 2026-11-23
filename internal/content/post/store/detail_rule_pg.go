package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"nongyu-backend-plus/internal/content/post"
)

// DetailRulePGRepo 从 post_type_rule 表读规则。
//
// 这是「数据驱动校验」的落地：管理员往表里插一行，新的字段要求立刻生效，
// 不需要发版。所以本仓储**每次现查**，不做进程内缓存——
// 规则表是低频写、量极小（每类型几行），一条带索引的小查询成本可以忽略；
// 而缓存会让「管理员刚加的规则要等重启/过期才生效」，那是运维上很难解释的现象。
//
// 若将来规则查询成为热点（例如每请求多次），再在**装配层**加一层
// 带 TTL 的缓存 + 管理端写后失效，而不是在这里偷偷加缓存。
type DetailRulePGRepo struct {
	pool *pgxpool.Pool
}

// NewDetailRulePGRepo 构造 PG 规则仓库。
func NewDetailRulePGRepo(pool *pgxpool.Pool) *DetailRulePGRepo {
	return &DetailRulePGRepo{pool: pool}
}

// RulesFor 实现 post.DetailRuleRepo。
//
// 只返回**已启用类型**的规则行：类型被管理员停用后，历史帖子仍可读，
// 但不应再接受新发帖（校验时拿不到规则 → detail 必须为空 → 评价类发不出去）。
// 类型停用的语义因此是「不再能发」，而不是「数据消失」。
func (r *DetailRulePGRepo) RulesFor(ctx context.Context, board post.Board) ([]post.DetailRule, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT r.field, r.value_type, r.required, r.min_len, r.max_len,
		       r.min_value, r.max_value, r.label
		  FROM post_type_rule r
		  JOIN post_types t ON t.code = r.type_code
		 WHERE r.type_code = $1 AND t.enabled
		 ORDER BY r.id`, string(board))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]post.DetailRule, 0, 4)
	for rows.Next() {
		var (
			d  post.DetailRule
			vt string
		)
		if err := rows.Scan(&d.Field, &vt, &d.Required, &d.MinLen, &d.MaxLen,
			&d.MinValue, &d.MaxValue, &d.Label); err != nil {
			return nil, err
		}
		d.ValueType = vt
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// KnownType 实现 DetailRuleRepo：查 post_types 是否存在且启用。
//
// 与 RulesFor 分开的理由见接口注释：feedback 这类「合法但无规则」的类型
// 与「根本不存在的类型」都会返回零规则行，必须靠这个方法区分，
// 否则未登记类型会一路走到 PG 外键才炸成 500。
func (r *DetailRulePGRepo) KnownType(ctx context.Context, board post.Board) (bool, error) {
	var ok bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM post_types WHERE code = $1 AND enabled)`,
		string(board)).Scan(&ok)
	if err != nil {
		return false, err
	}
	return ok, nil
}

// 编译期确认。
var _ post.DetailRuleRepo = (*DetailRulePGRepo)(nil)
