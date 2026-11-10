package migrations_test

// 迁移嵌入资产的守护测试（issue #23）。
//
// 目的：//go:embed 让 SQL 随二进制走，但也带来「磁盘上的权威源与二进制里嵌入的
// 副本不一致」的风险。本测试在测试期把嵌入内容与仓库根 migrations/ 的磁盘文件
// 逐字节比对，任何一边改了另一边没跟上就立刻变红——避免「测试没跑」伪装成
// 「测试通过」，也避免漂移出第四份手写 DDL。

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"nongyu-backend-plus/migrations"
)

// TestEmbeddedSQL_MatchesRootMigrationFiles 断言嵌入的两份 SQL 与磁盘权威源一致，
// 且编号顺序正确（0001 在 0002 之前）。
func TestEmbeddedSQL_MatchesRootMigrationFiles(t *testing.T) {
	files, err := migrations.Files()
	if err != nil {
		t.Fatalf("枚举嵌入脚本失败：%v", err)
	}
	if len(files) == 0 {
		t.Fatal("没有任何迁移脚本")
	}
	// 不再硬编码「恰好两份」：迁移是增量的，新增编号属正常演进。
	// 这里改为校验「编号连续从 0001 开始」+「逐个与磁盘一致」。
	for i := range files {
		// 只校验「编号连续且从 0001 开始」，文件名主体不锁死。
		wantPrefix := fmt.Sprintf("%04d_", i+1)
		if !strings.HasPrefix(files[i].Name, wantPrefix) {
			t.Fatalf("第 %d 份脚本应以 %q 开头，实际 %q（顺序即执行顺序）", i, wantPrefix, files[i].Name)
		}
		// go test 的 cwd 就是本包目录（migrations/），权威源与 .go 同目录。
		disk, err := os.ReadFile(files[i].Name)
		if err != nil {
			t.Fatalf("读磁盘权威源 %s 失败：%v", files[i].Name, err)
		}
		if !bytes.Equal([]byte(files[i].SQL), disk) {
			t.Fatalf("嵌入的 %s 与磁盘权威源不一致（%d vs %d 字节）", files[i].Name, len(files[i].SQL), len(disk))
		}
	}
}
