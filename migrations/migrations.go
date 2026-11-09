// Package migrations 把仓库根的迁移 SQL 作为编译期资产嵌入二进制。
//
// 为什么 .go 文件放在 migrations/ 目录里：//go:embed 的模式不能含 `..`，
// 也不能越过包目录去引用上级/同级目录的文件。若把 embed 写在 internal/platform
// 下并写 `//go:embed ../../migrations/*.sql`，编译器直接报错。把 embed 锚点放在
// 与 .sql 同目录，仓库根 migrations/*.sql 就是**唯一权威源**（运维手工执行、
// docs/tech/持久层结构.md 也引用它），不存在「第四份手写 DDL」漂移问题。
//
// 本包只负责「把文件交出来」，不负责连库、不解释 SQL；执行逻辑在
// internal/platform.Migrate，测试/装配层按需调用。
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

// FS 是嵌入的迁移文件集合（含 0001_audit_log.sql、0002_identity.sql）。
//
//go:embed *.sql
var FS embed.FS

// SQLFile 是一份迁移脚本：文件名 + 原始内容。
type SQLFile struct {
	Name string
	SQL  string
}

// Files 返回按文件名升序排列的迁移脚本。
//
// 顺序即执行顺序：迁移按编号命名（0001 < 0002），升序保证 0002 里引用
// users 的外键建立在 0001/0002 自身已建的 users 之后。新增迁移只需在
// migrations/ 放一个编号更大的 .sql，无需改 Go 代码。
func Files() ([]SQLFile, error) {
	matches, err := fs.Glob(FS, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("migrations: 枚举 *.sql 失败：%w", err)
	}
	sort.Strings(matches)
	out := make([]SQLFile, 0, len(matches))
	for _, name := range matches {
		b, err := FS.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("migrations: 读取 %s 失败：%w", name, err)
		}
		out = append(out, SQLFile{Name: name, SQL: string(b)})
	}
	return out, nil
}
