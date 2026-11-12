package config_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 本测试保证「代码读取的环境变量」与「.env.example 声明的变量」严格一致。
//
// 为什么值得一条测试：这两者一旦脱节，症状是**部署时静默漏配**——
// 服务能起来，但某个能力（身份域/缓存/对象存储）默默不生效，
// 而日志里的 WARN 很容易被忽略。人在新增配置项时最常忘的就是同步文档，
// 靠测试强制比靠记性可靠。

// envReadPatterns 也是「代码里怎么读环境变量」的白名单。
//
// 新增读取方式（例如引入 kelseyhightower/envconfig）时必须同步更新这里，
// 否则测试会漏掉那批变量——**这是刻意的**：漏掉比误报好，
// 因为误报会逼着人反复调测试，最终把测试删掉。
var envReadPatterns = []*regexp.Regexp{
	regexp.MustCompile(`env(?:Or|IntOr|FloatOr|BoolOr)\("([A-Z_0-9]+)"`),
	regexp.MustCompile(`os\.Getenv\("([A-Z_0-9]+)"`),
}

// declaredInExample 解析 .env.example 里声明的变量名。
//
// 只认「行首是变量名 + 等号」的形式（被注释掉的 `# FOO=bar` 不算声明，
// 那只是示例）。这正是我们想要的：示例被注释掉 = 没有真的声明。
var exampleLineRE = regexp.MustCompile(`(?m)^([A-Z_0-9]+)=`)

func TestEnvExample_CoversEveryVariable(t *testing.T) {
	// 1) 从 config.go 提取代码读取的变量。
	src, err := os.ReadFile(filepath.Join("..", "config", "config.go"))
	if err != nil {
		t.Fatalf("读 config.go 失败：%v", err)
	}
	codeVars := map[string]bool{}
	for _, re := range envReadPatterns {
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			codeVars[m[1]] = true
		}
	}
	if len(codeVars) == 0 {
		t.Fatal("没从 config.go 提取到任何环境变量——正则可能已失效，请更新 envReadPatterns")
	}

	// 2) 从 .env.example 提取声明的变量。
	example, err := os.ReadFile(filepath.Join("..", "..", ".env.example"))
	if err != nil {
		t.Fatalf("读 .env.example 失败：%v", err)
	}
	declared := map[string]bool{}
	for _, m := range exampleLineRE.FindAllStringSubmatch(string(example), -1) {
		declared[m[1]] = true
	}

	// 3) 两个方向都要查：漏写会导致部署漏配，多写会误导运维。
	var missing, extra []string
	for v := range codeVars {
		if !declared[v] {
			missing = append(missing, v)
		}
	}
	for v := range declared {
		if !codeVars[v] {
			extra = append(extra, v)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("以下环境变量代码在用但 .env.example 没声明（部署时会漏配）：\n  %s",
			strings.Join(missing, "\n  "))
	}
	if len(extra) > 0 {
		t.Errorf("以下变量 .env.example 声明了但代码没读取（会误导运维）：\n  %s",
			strings.Join(extra, "\n  "))
	}
}
