// Package jiaowu 的 schema 校验器：启动期把 15 份 JSON Schema 编译好，运行期只做校验。
//
// 对应决议 #41 / #42：
//   - 载体是 JSON Schema，Draft 2020-12（#42 Q2）；
//   - schema 文件放本包 schema/ 下，用 go:embed 打进二进制（#42 Q4）；
//   - 启动时一次性 Compile，写错 schema 立刻 fail-fast，不等到第一个请求；
//   - 形状分三层：#42 Q3 的第三层（业务语义）留在这里的 Go 代码，不进 schema。
package jiaowu

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema/*.json
var schemaFS embed.FS

// schemaBaseURL 给每个 schema 一个稳定的绝对 URL。
//
// Compile 需要一个可解析的 loc；用裸文件名在部分实现里会被当成相对引用。
// 这些 schema 互相之间没有 $ref，所以这个 URL 不需要真的可访问。
const schemaBaseURL = "https://nongyu.local/schema/"

// SchemaValidator 持有全部 dataType 的已编译 schema。
//
// 编译后是只读的，可被多 goroutine 并发复用（jsonschema.Schema.Validate 是纯函数）。
type SchemaValidator struct {
	schemas map[DataType]*jsonschema.Schema
}

// NewSchemaValidator 读入并编译全部 schema。
//
// 任何一步失败都返回错误而不是降级：schema 是这道闸的全部依据，
// 缺一份或坏一份就意味着某类脏数据能进缓存，必须在启动期拦住。
func NewSchemaValidator() (*SchemaValidator, error) {
	files, err := fs.Glob(schemaFS, "schema/*.json")
	if err != nil {
		return nil, fmt.Errorf("jiaowu: 枚举 schema 文件失败: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("jiaowu: 没有找到任何 schema 文件")
	}
	sort.Strings(files)

	compiler := jsonschema.NewCompiler()
	v := &SchemaValidator{schemas: make(map[DataType]*jsonschema.Schema, len(files))}

	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		dt, ok := ParseDataType(name)
		if !ok {
			return nil, fmt.Errorf("jiaowu: schema 文件 %q 不对应任何已知 dataType", f)
		}

		raw, err := schemaFS.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("jiaowu: 读取 schema %q 失败: %w", f, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("jiaowu: schema %q 不是合法 JSON: %w", f, err)
		}

		url := schemaBaseURL + name + ".json"
		if err := compiler.AddResource(url, doc); err != nil {
			return nil, fmt.Errorf("jiaowu: 注册 schema %q 失败: %w", f, err)
		}
		sch, err := compiler.Compile(url)
		if err != nil {
			return nil, fmt.Errorf("jiaowu: 编译 schema %q 失败: %w", f, err)
		}
		v.schemas[dt] = sch
	}

	// 每个受控类型都必须有 schema：漏一份就意味着这类回传永远不会被校验。
	missing := make([]string, 0)
	for _, dt := range KnownDataTypes() {
		if _, ok := v.schemas[dt]; !ok {
			missing = append(missing, string(dt))
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("jiaowu: 以下 dataType 缺少 schema 文件: %s", strings.Join(missing, ", "))
	}
	return v, nil
}

// Validate 校验一份回传体是否符合该 dataType 的 schema。
//
// 返回的错误会带上具体是哪个位置不符合（jsonschema 的定位信息），
// **只进日志与审计**，绝不回传给终端用户——那属于内部实现细节（#39 Q4 的口径）。
func (v *SchemaValidator) Validate(dt DataType, body []byte) error {
	if v == nil {
		return fmt.Errorf("jiaowu: schema 校验器为 nil")
	}
	sch, ok := v.schemas[dt]
	if !ok {
		return fmt.Errorf("jiaowu: dataType %q 没有对应的 schema", string(dt))
	}

	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("回传体不是合法 JSON: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("形状不符合 %s 的 schema: %w", string(dt), err)
	}
	return nil
}

// DataTypes 返回校验器已装载的类型（供自检与测试核对）。
func (v *SchemaValidator) DataTypes() []DataType {
	if v == nil {
		return nil
	}
	out := make([]DataType, 0, len(v.schemas))
	for dt := range v.schemas {
		out = append(out, dt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
