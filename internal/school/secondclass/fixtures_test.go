package secondclass

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 真实采集夹具（2026-11-16）在仓库根 testdata/secondclass/ 下。
// 本包测试用它替代真实二课平台，绝不访问网络（规格 §8.3）。
// go test 的工作目录是本包源码目录 <root>/internal/school/secondclass，
// 往上三级就是仓库根。
const fixtureDir = "../../../testdata/secondclass/real-2026-11-16"

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("读夹具 %s 失败: %v", name, err)
	}
	return raw
}

// submitBodyFromFixture 把一份真实二课响应信封转成后端回传体：
// 取实测的 content（不是离线样本里的 data）作为 payload，补 schemaVersion=1。
// 同时返回取出的 content，供「payload 原样透出」断言比对。
func submitBodyFromFixture(t *testing.T, name string) (body []byte, content json.RawMessage) {
	t.Helper()
	var env struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(readFixture(t, name), &env); err != nil {
		t.Fatalf("解析夹具 %s 失败: %v", name, err)
	}
	if len(env.Content) == 0 {
		t.Fatalf("夹具 %s 没有 content 字段", name)
	}
	body, err := json.Marshal(struct {
		SchemaVersion int             `json:"schemaVersion"`
		Payload       json.RawMessage `json:"payload"`
	}{SchemaVersion: 1, Payload: env.Content})
	if err != nil {
		t.Fatalf("构造回传体失败: %v", err)
	}
	return body, env.Content
}

// jsonEqual 比对两段 JSON 的语义相等（不受字段顺序/空白影响）。
func jsonEqual(t *testing.T, got, want []byte) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("got 不是合法 JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal(want, &b); err != nil {
		t.Fatalf("want 不是合法 JSON: %v (%s)", err, want)
	}
	g, _ := json.Marshal(a)
	w, _ := json.Marshal(b)
	return string(g) == string(w)
}
