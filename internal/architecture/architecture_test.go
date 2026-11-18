// Package architecture 用编译期之外的一条测试，守住整改后的分层方向。
//
// 背景：整改前 internal/platform/pgstore 反向 import 了 content/message/skin/track/
// school 五个业务域——最底层依赖最上层，是典型的分层倒置。整改把仓储实现搬进了
// 各域的 store/ 子包。但「口头约定」会随时间腐化，故把三条方向性规则写成测试。
//
// **只检查生产代码（非 _test.go）**：测试文件为了造 fixture 会 import 具体仓储、
// 装配层甚至别的域，那是合理的，不该受分层约束。把测试也算进来只会产生大量误报。
package architecture

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot 返回仓库根（本测试文件在 internal/architecture/ 下，向上两级）。
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// prodImports 逐文件返回「文件 -> 它的 import 路径集合」，跳过 _test.go 与子目录。
func prodImports(t *testing.T, dir string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out // 目录不存在视为空
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("解析 %s 失败：%v", e.Name(), err)
		}
		var imps []string
		for _, imp := range f.Imports {
			imps = append(imps, strings.Trim(imp.Path.Value, `"`))
		}
		out[e.Name()] = imps
	}
	return out
}

// TestPlatformNeverImportsBusinessDomains 是最硬的一条：platform 是最底层，
// 不得 import 任何业务域。整改前 pgstore 违反的正是这一条。
func TestPlatformNeverImportsBusinessDomains(t *testing.T) {
	root := repoRoot(t)
	platformRoot := filepath.Join(root, "internal", "platform")

	forbidden := []string{
		"nongyu-backend-plus/internal/identity",
		"nongyu-backend-plus/internal/content",
		"nongyu-backend-plus/internal/message",
		"nongyu-backend-plus/internal/skin",
		"nongyu-backend-plus/internal/track",
		"nongyu-backend-plus/internal/school",
	}

	// platform 根 + 所有 platform/* 子包（单层即可，本仓库不会更深）
	dirs := []string{platformRoot}
	entries, _ := os.ReadDir(platformRoot)
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(platformRoot, e.Name()))
		}
	}

	for _, d := range dirs {
		for file, imps := range prodImports(t, d) {
			for _, imp := range imps {
				for _, f := range forbidden {
					if imp == f || strings.HasPrefix(imp, f+"/") {
						t.Errorf("分层倒置：%s/%s import 了业务域 %s\nplatform 是最底层，仓储实现应放在 <域>/store/ 子包里",
							filepath.Base(d), file, imp)
					}
				}
			}
		}
	}
}

// TestDomainRootsNeverImportTheirOwnStoreOrAPI 守住「域根不反向依赖自己的适配层」。
//
// 约定：域根放实体 + 仓储接口 + 业务规则；store/ 放实现；api/ 放 HTTP 层。
// 方向是 store → 域根、api → 域根，反过来就是环。
func TestDomainRootsNeverImportTheirOwnStoreOrAPI(t *testing.T) {
	root := repoRoot(t)
	domains := []string{
		"internal/content/announcement",
		"internal/content/post",
		"internal/message",
		"internal/skin",
		"internal/track",
		"internal/school/jiaowu",
		"internal/school/secondclass",
	}
	for _, d := range domains {
		domainImport := "nongyu-backend-plus/" + d
		for file, imps := range prodImports(t, filepath.Join(root, d)) {
			for _, imp := range imps {
				if imp == domainImport+"/store" || imp == domainImport+"/api" ||
					strings.HasPrefix(imp, domainImport+"/api/") {
					t.Errorf("分层环：域根 %s/%s 反向 import 了 %s\n域根不得依赖自己的 store/ 或 api/",
						d, file, imp)
				}
			}
		}
	}
}

// TestOnlyAppImportsApp 守住「业务域不得 import 装配层」。
//
// app 是 composition root，只允许被 cmd/ 与它自己（含 app/notify）引用。
// 业务域若 import app，说明出现了反向依赖或跨域协作没走编排层。
func TestOnlyAppImportsApp(t *testing.T) {
	root := repoRoot(t)
	internalRoot := filepath.Join(root, "internal")

	_ = filepath.Walk(internalRoot, func(dir string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, dir)
		relSlash := filepath.ToSlash(rel)
		// 允许 app 自身与 app/notify
		if relSlash == "internal/app" || strings.HasPrefix(relSlash, "internal/app/") {
			return nil
		}
		for file, imps := range prodImports(t, dir) {
			for _, imp := range imps {
				if imp == "nongyu-backend-plus/internal/app" ||
					strings.HasPrefix(imp, "nongyu-backend-plus/internal/app/") {
					t.Errorf("业务域反向依赖装配层：%s/%s -> %s\n跨域协作应由 app 编排，业务域不得 import app",
						relSlash, file, imp)
				}
			}
		}
		return nil
	})
}
