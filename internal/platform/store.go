package platform

// ⚠️ 本文件是**旧 store.go（1508 行）的迁移占位**，内容已全部拆到：
//
//	errors.go         哨兵错误（ErrNotFound / ErrAlreadyExists）
//	identity.go       身份域模型与仓储契约
//	pg_identity.go    PGIdentityStore 的 Postgres 实现
//	pg_audit.go       PGAuditStore 审计批量写
//	testschema.go     测试 schema 隔离
//	testschema_ddl.go 测试 DDL 内嵌副本
//	xsgz_profile.go   学工档案落库（待迁往 school/xsgz）
//
// 保留空声明是为了让「拆分这一步」自洽可回滚；**下一次 commit 必须删除本文件**。
