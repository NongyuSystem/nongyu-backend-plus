package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// isUniqueViolation 判断是否是唯一约束冲突（SQLSTATE 23505）。
//
// 在本包里判断而不是复用别包的私有函数：那需要把它们导出，
// 为一个两行的判断扩大公共 API 不划算。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// isForeignKeyViolation 判断是否是外键冲突（SQLSTATE 23503）。
//
// 典型场景：往一个不存在的 building_id 插路线。
// 这必须翻译成 404/400，而不是让它冒成 500——那是把调用方的
// 参数错误报成服务端故障。
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23503"
	}
	return false
}
