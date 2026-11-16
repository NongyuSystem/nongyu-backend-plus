package skin_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"testing"

	"nongyu-backend-plus/internal/skin"
)

// 本文件是皮肤域测试的公共构造工具：造一个「结构合法」的皮肤包 zip。
// 与客户端 SkinPack 契约逐字段对齐；单测通过改写 manifest / 增删文件来
// 覆盖各条校验分支，不必每个用例手搓一遍 zip。

func sha(b []byte) string { return skin.Sha256Hex(b) }

// testEntry 是造包时的一个资源文件。
type testEntry struct {
	path string
	body []byte
}

// buildManifest 由资源文件清单生成一份合法的 manifest.json 原文。
// signature 故意填一个非空占位串：服务端不验签（信任根在客户端公钥），
// 但客户端会因缺签名整包拒绝，故服务端把它列为必填。
func buildManifest(packID string, entries []testEntry, mutate func(map[string]any)) []byte {
	entriesJSON := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		entriesJSON = append(entriesJSON, map[string]any{
			"path": e.path, "sha256": sha(e.body),
		})
	}
	m := map[string]any{
		"packId":        packID,
		"schemaVersion": 1,
		"minAppVersion": 1,
		"displayName":   "测试皮肤",
		"payload": map[string]any{
			"iconStrokes": map[string]float64{"home": 0.8},
		},
		"integrity": map[string]any{
			"entries":   entriesJSON,
			"signature": "c2lnbmF0dXJlLXBsYWNlaG9sZGVy", // base64 占位
		},
	}
	if mutate != nil {
		mutate(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

type zipFile struct {
	name string
	body []byte
}

// buildZip 把所有文件打进一个 zip。
func buildZip(t *testing.T, files []zipFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatalf("zip.Create(%s): %v", f.name, err)
		}
		if _, err := w.Write(f.body); err != nil {
			t.Fatalf("zip write %s: %v", f.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip.Close: %v", err)
	}
	return buf.Bytes()
}

// validPack 造一个默认合法的皮肤包（manifest.json + 两个资源文件）。
func validPack(t *testing.T) []byte {
	t.Helper()
	entries := []testEntry{
		{path: "icons/home.png", body: []byte("fake-png-bytes")},
		{path: "fonts/nongyu.ttf", body: []byte("fake-ttf-bytes")},
	}
	manifest := buildManifest("default-green", entries, func(m map[string]any) {
		m["payload"].(map[string]any)["fontFileName"] = "fonts/nongyu.ttf"
	})
	files := []zipFile{{name: skin.ManifestFileName, body: manifest}}
	for _, e := range entries {
		files = append(files, zipFile{name: e.path, body: e.body})
	}
	return buildZip(t, files)
}

// packWith 允许替换 manifest / 文件列表，用于覆盖各校验失败分支。
func packWith(t *testing.T, manifest []byte, files ...zipFile) []byte {
	t.Helper()
	all := []zipFile{{name: skin.ManifestFileName, body: manifest}}
	all = append(all, files...)
	return buildZip(t, all)
}

// assertCode 断言错误是领域错误且带期望码。
func assertCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误码 %q，实际 nil", want)
	}
	// apperr.Error 的 Error() 形如 "code: message"。
	got := err.Error()
	if len(got) < len(want) || got[:len(want)] != want {
		t.Fatalf("期望错误码 %q，实际 %q", want, got)
	}
}

// entryMap 把 entries 转成 map 便于改写 integrity 块。
func entryMap(entries []testEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{"path": e.path, "sha256": sha(e.body)})
	}
	return out
}
