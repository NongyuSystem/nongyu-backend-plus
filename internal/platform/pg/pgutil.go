// Package pgutil 提供 Postgres 仓储实现之间**共用的小工具**。
//
// 为什么单独成包：各域的 store/ 子包（content/post/store、message/store …）
// 都需要「可空时间转换」和「参数占位符拼装」这两件事。以前它们被复制在每个
// 文件里，或者挤在已删除的 platform/pgstore 那个反向依赖业务域的错误位置。
//
// 本包只依赖 pgx 与标准库，**不 import 任何业务域**，因此任何层都能安全引用。
package pg

import (
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// TimePtrToPG 把可空时间转成 pgx 可编码的 timestamptz（nil → NULL）。
func TimePtrToPG(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// TimePtr 把扫描到的 timestamptz 转成可空时间（NULL → nil）。
//
// 每次返回新指针：调用方可能改它，不能让多个字段共享同一块内存。
func TimePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// Args 是「按顺序追加参数并返回 $n 占位符」的小工具。
//
// 为什么不手工数 $1/$2：列表查询的可选条件（游标、limit）会让占位符编号
// 随分支漂移，手写极易出现「加了 WHERE 忘了把 LIMIT 的编号 +1」，而那种
// 错误在只有一条数据时不会暴露。**任何值都不拼进 SQL 文本**，只拼占位符。
type Args struct {
	args []any
}

// Add 追加一个参数并返回它的占位符（从 $1 起）。
func (b *Args) Add(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

// All 返回累积的参数切片，供 Query/Exec 使用。
func (b *Args) All() []any { return b.args }

// RowScanner 是 pgx.Row 与 pgx.Rows 的公共 Scan 签名。
//
// 抽出来是为了让「单行 FindByID」与「多行 List」共用同一份扫描逻辑——
// 列清单与 Scan 参数一旦分两处写，加列时必然漏改一处。
type RowScanner interface {
	Scan(dest ...any) error
}
