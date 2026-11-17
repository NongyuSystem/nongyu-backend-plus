// Package secondclass 的 schema 校验器：启动期把 12 份 JSON Schema 编译好，运行期只做校验。
//
// 形状照 jiaowu/schemavalidate.go（go:embed + 启动期 Compile + KnownDataTypes 反查缺失），
// 但自己实现、不 import 教务域；词表换成二课 A 组 12 个 dataType。
// 载体 jsonschema/v6（Draft 2020-12）已在 go.mod，不引入新依赖。
package secondclass

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

// schemaBaseURL 给每个 schema 一个稳定的绝对 URL。schema 间无 $ref，不需真可访问。
const schemaBaseURL = "https://nongyu.local/secondclass/schema/"

// SchemaValidator 持有全部 dataType 的已编译 schema（编译后只读、可并发复用）。
type SchemaValidator struct {
	schemas map[DataType]*jsonschema.Schema
}

// NewSchemaValidator 读入并编译全部 schema。任何一步失败都返回错误而不是降级。
func NewSchemaValidator() (*SchemaValidator, error) {
	files, err := fs.Glob(schemaFS, "schema/*.json")
	if err != nil {
		return nil, fmt.Errorf("secondclass: 枚举 schema 文件失败: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("secondclass: 没有找到任何 schema 文件")
	}
	sort.Strings(files)

	compiler := jsonschema.NewCompiler()
	v := &SchemaValidator{schemas: make(map[DataType]*jsonschema.Schema, len(files))}

	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		dt, ok := ParseDataType(name)
		if !ok {
			return nil, fmt.Errorf("secondclass: schema 文件 %q 不对应任何已知 dataType", f)
		}
		raw, err := schemaFS.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("secondclass: 读取 schema %q 失败: %w", f, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("secondclass: schema %q 不是合法 JSON: %w", f, err)
		}
		url := schemaBaseURL + name + ".json"
		if err := compiler.AddResource(url, doc); err != nil {
			return nil, fmt.Errorf("secondclass: 注册 schema %q 失败: %w", f, err)
		}
		sch, err := compiler.Compile(url)
		if err != nil {
			return nil, fmt.Errorf("secondclass: 编译 schema %q 失败: %w", f, err)
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
		return nil, fmt.Errorf("secondclass: 以下 dataType 缺少 schema 文件: %s", strings.Join(missing, ", "))
	}
	return v, nil
}

// Validate 校验一份回传体是否符合该 dataType 的 schema。
// 错误带具体位置，只进日志与审计，绝不回传终端用户（规格 §3.6 / §8.1）。
func (v *SchemaValidator) Validate(dt DataType, body []byte) error {
	if v == nil {
		return fmt.Errorf("secondclass: schema 校验器为 nil")
	}
	sch, ok := v.schemas[dt]
	if !ok {
		return fmt.Errorf("secondclass: dataType %q 没有对应的 schema", string(dt))
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
