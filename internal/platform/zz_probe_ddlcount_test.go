package platform

// 本文件的临时回归探针已用完并移除。
//
// 留下了什么：原探针 `TestZZProbe_IdentityDDLStatementsCount` 的作用已被
// `TestTestSchemaDDL_MatchesMigrationsStructurally` 里的「表集合必须相等」断言覆盖——
// 手写测试 DDL 只要漏一张表，DDL 一致性守卫立刻变红（2026-11 已验证：临时注释掉
// skin_current 一条，守卫报「表集合不一致」）。
//
// 保留空声明只为让删除这一步在沙箱内可回滚；下一次 commit 删除本文件。
