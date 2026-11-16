package skin_test

import (
	"encoding/json"
	"testing"

	"nongyu-backend-plus/internal/skin"
)

// ---------- IsSafeSegment（与客户端 SkinStoreLayout.isSafeSegment 对齐） ----------

func TestIsSafeSegment(t *testing.T) {
	ok := []string{"default-green", "1.0.0", "v2_beta", "a.b-c_d"}
	for _, s := range ok {
		if !skin.IsSafeSegment(s) {
			t.Fatalf("%q 应是合法段", s)
		}
	}
	bad := []string{"", ".", "..", "../etc", "a/../b", "a/b", "a\\b", "a b", "中文", "a:b"}
	for _, s := range bad {
		if skin.IsSafeSegment(s) {
			t.Fatalf("%q 应是非法段", s)
		}
	}
}

// ---------- CheckPathAllowed（与客户端 checkPathAllowed 对齐） ----------

func TestCheckPathAllowed(t *testing.T) {
	ok := []string{"manifest.json", "icons/home.png", "fonts/x.ttf", "cover.webp", "p/a-b.jpg"}
	for _, p := range ok {
		if err := skin.CheckPathAllowed(p); err != nil {
			t.Fatalf("%q 应合法，实际 %v", p, err)
		}
	}
	bad := []string{
		"",                  // 空
		"/etc/passwd",       // 绝对路径
		`\windows\evil.dll`, // 反斜杠绝对路径
		"../evil.png",       // 目录穿越
		"a/../../x.png",     // 含 ..
		"icons/",            // 目录
		"README",            // 无扩展名
		"icons/x.",          // 尾点 = 无扩展名
		"lib/x.dex",         // 禁用可执行
		"lib/x.so",          // 禁用可执行
		"run.sh",            // 禁用可执行
		"x.exe",             // 禁用可执行
		"x.bin",             // 未知扩展名
		"icons\\home.png",   // 反斜杠
	}
	for _, p := range bad {
		if err := skin.CheckPathAllowed(p); err == nil {
			t.Fatalf("%q 应被拒绝", p)
		}
	}
}

// ---------- ObjectKey ----------

func TestObjectKey(t *testing.T) {
	if got := skin.ObjectKey("default-green", "1.0.0"); got != "skins/default-green/1.0.0.zip" {
		t.Fatalf("ObjectKey 不符：%q", got)
	}
}

// ---------- ValidateManifest ----------

func validManifestForTest() *skin.Manifest {
	return &skin.Manifest{
		PackID: "default-green", SchemaVersion: 1, MinAppVersion: 1,
		DisplayName: "测试皮肤",
		Payload:     skin.Payload{FontFileName: "fonts/x.ttf"},
		Integrity: skin.Integrity{
			Signature: "c2ln",
			Entries: []skin.Entry{
				{Path: "fonts/x.ttf", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				{Path: "icons/home.png", SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			},
		},
	}
}

func TestValidateManifest_OK(t *testing.T) {
	if err := skin.ValidateManifest(validManifestForTest()); err != nil {
		t.Fatalf("合法清单应通过：%v", err)
	}
}

func TestValidateManifest_RequiredFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*skin.Manifest)
		code   string
	}{
		{"nil", func(m *skin.Manifest) {}, "validation_failed"}, // 单独处理
		{"空 packId", func(m *skin.Manifest) { m.PackID = "" }, "validation_failed"},
		{"非法 packId", func(m *skin.Manifest) { m.PackID = "../evil" }, "validation_failed"},
		{"schema 为 0", func(m *skin.Manifest) { m.SchemaVersion = 0 }, "validation_failed"},
		{"schema 过高", func(m *skin.Manifest) { m.SchemaVersion = 99 }, "schema_rejected"},
		{"minApp 为负", func(m *skin.Manifest) { m.MinAppVersion = -1 }, "validation_failed"},
		{"空 displayName", func(m *skin.Manifest) { m.DisplayName = "  " }, "validation_failed"},
		{"空签名", func(m *skin.Manifest) { m.Integrity.Signature = "" }, "validation_failed"},
		{"空 entries", func(m *skin.Manifest) { m.Integrity.Entries = nil }, "validation_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifestForTest()
			if tc.name == "nil" {
				assertCode(t, skin.ValidateManifest(nil), tc.code)
				return
			}
			tc.mutate(m)
			assertCode(t, skin.ValidateManifest(m), tc.code)
		})
	}
}

func TestValidateManifest_EntryShape(t *testing.T) {
	// 非法 sha256
	m := validManifestForTest()
	m.Integrity.Entries[0].SHA256 = "ABC" // 非小写 hex / 长度不足
	assertCode(t, skin.ValidateManifest(m), "validation_failed")

	// 非法路径
	m = validManifestForTest()
	m.Integrity.Entries[0].Path = "../evil.png"
	assertCode(t, skin.ValidateManifest(m), "validation_failed")

	// 重复路径
	m = validManifestForTest()
	m.Integrity.Entries[1].Path = m.Integrity.Entries[0].Path
	assertCode(t, skin.ValidateManifest(m), "validation_failed")

	// fontFileName 未在 entries 里
	m = validManifestForTest()
	m.Payload.FontFileName = "fonts/missing.ttf"
	assertCode(t, skin.ValidateManifest(m), "validation_failed")

	// fontFileName 非法路径
	m = validManifestForTest()
	m.Payload.FontFileName = "../x.ttf"
	assertCode(t, skin.ValidateManifest(m), "validation_failed")
}

// ---------- VerifyPack（zip 全链路） ----------

func TestVerifyPack_OK(t *testing.T) {
	raw := validPack(t)
	vr, err := skin.VerifyPack(skin.VerifyInput{PackBytes: raw})
	if err != nil {
		t.Fatalf("合法包应通过：%v", err)
	}
	if vr.Manifest.PackID != "default-green" {
		t.Fatalf("packId 不符：%q", vr.Manifest.PackID)
	}
	if vr.PackSize != int64(len(raw)) {
		t.Fatalf("PackSize 不符：%d", vr.PackSize)
	}
	if vr.PackSHA256 != skin.Sha256Hex(raw) {
		t.Fatal("PackSHA256 不符")
	}
	if vr.EntryCount != 2 {
		t.Fatalf("EntryCount 应为 2，实际 %d", vr.EntryCount)
	}
	// manifest 原文可再解析（客户端拿到的就是这个串）
	var m skin.Manifest
	if err := json.Unmarshal([]byte(vr.ManifestJSON), &m); err != nil {
		t.Fatalf("ManifestJSON 不可解析：%v", err)
	}
}

func TestVerifyPack_RejectsNonZip(t *testing.T) {
	_, err := skin.VerifyPack(skin.VerifyInput{PackBytes: []byte("not a zip")})
	assertCode(t, err, "validation_failed")
}

func TestVerifyPack_RejectsEmpty(t *testing.T) {
	_, err := skin.VerifyPack(skin.VerifyInput{})
	assertCode(t, err, "validation_failed")
}

func TestVerifyPack_RejectsMissingManifest(t *testing.T) {
	raw := buildZip(t, []zipFile{{name: "icons/home.png", body: []byte("x")}})
	_, err := skin.VerifyPack(skin.VerifyInput{PackBytes: raw})
	assertCode(t, err, "validation_failed")
}

func TestVerifyPack_RejectsBadJSON(t *testing.T) {
	raw := packWith(t, []byte("{not json"))
	_, err := skin.VerifyPack(skin.VerifyInput{PackBytes: raw})
	assertCode(t, err, "validation_failed")
}

func TestVerifyPack_RejectsUndeclaredFile(t *testing.T) {
	entries := []testEntry{{path: "icons/home.png", body: []byte("x")}}
	manifest := buildManifest("p", entries, nil)
	raw := packWith(t, manifest,
		zipFile{name: "icons/home.png", body: []byte("x")},
		zipFile{name: "icons/extra.png", body: []byte("surprise")}, // 清单没声明
	)
	_, err := skin.VerifyPack(skin.VerifyInput{PackBytes: raw})
	assertCode(t, err, "validation_failed")
}

func TestVerifyPack_RejectsMissingDeclaredFile(t *testing.T) {
	entries := []testEntry{
		{path: "icons/home.png", body: []byte("x")},
		{path: "fonts/x.ttf", body: []byte("y")},
	}
	manifest := buildManifest("p", entries, nil)
	raw := packWith(t, manifest, zipFile{name: "icons/home.png", body: []byte("x")})
	_, err := skin.VerifyPack(skin.VerifyInput{PackBytes: raw})
	assertCode(t, err, "validation_failed")
}

func TestVerifyPack_RejectsShaMismatch(t *testing.T) {
	entries := []testEntry{{path: "icons/home.png", body: []byte("x")}}
	manifest := buildManifest("p", entries, nil)
	// 文件字节被改，与清单里 sha256(x) 不符
	raw := packWith(t, manifest, zipFile{name: "icons/home.png", body: []byte("tampered")})
	_, err := skin.VerifyPack(skin.VerifyInput{PackBytes: raw})
	assertCode(t, err, "validation_failed")
}

func TestVerifyPack_RejectsExecutableAndTraversal(t *testing.T) {
	// 可执行文件（白名单外 + 禁用扩展名）
	raw := packWith(t, buildManifest("p", nil, nil),
		zipFile{name: "lib/evil.dex", body: []byte("x")})
	_, err := skin.VerifyPack(skin.VerifyInput{PackBytes: raw})
	assertCode(t, err, "validation_failed")

	// 目录穿越
	raw = packWith(t, buildManifest("p", nil, nil),
		zipFile{name: "../evil.png", body: []byte("x")})
	_, err = skin.VerifyPack(skin.VerifyInput{PackBytes: raw})
	assertCode(t, err, "validation_failed")
}

func TestVerifyPack_RejectsInvalidVersionField(t *testing.T) {
	entries := []testEntry{{path: "icons/home.png", body: []byte("x")}}
	manifest := buildManifest("p", entries, func(m map[string]any) {
		m["schemaVersion"] = 99 // 高于 SupportedSchemaMajor
	})
	raw := packWith(t, manifest, zipFile{name: "icons/home.png", body: []byte("x")})
	_, err := skin.VerifyPack(skin.VerifyInput{PackBytes: raw})
	assertCode(t, err, "schema_rejected")
}

func TestVerifyPack_RejectsDuplicateEntryName(t *testing.T) {
	entries := []testEntry{{path: "icons/home.png", body: []byte("x")}}
	manifest := buildManifest("p", entries, nil)
	// 同名文件在 zip 里出现两次
	raw := buildZip(t, []zipFile{
		{name: skin.ManifestFileName, body: manifest},
		{name: "icons/home.png", body: []byte("x")},
		{name: "icons/home.png", body: []byte("x")},
	})
	_, err := skin.VerifyPack(skin.VerifyInput{PackBytes: raw})
	assertCode(t, err, "validation_failed")
}

func TestVerifyPack_SignatureIsPassedThroughUnverified(t *testing.T) {
	// 服务端不验整包签名的真伪：哪怕换成一个任意串，只要非空就随包下发。
	entries := []testEntry{{path: "icons/home.png", body: []byte("x")}}
	manifest := buildManifest("p", entries, func(m map[string]any) {
		m["integrity"].(map[string]any)["signature"] = "totally-not-a-real-signature"
	})
	raw := packWith(t, manifest, zipFile{name: "icons/home.png", body: []byte("x")})
	vr, err := skin.VerifyPack(skin.VerifyInput{PackBytes: raw})
	if err != nil {
		t.Fatalf("服务端不应验签名真伪：%v", err)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(vr.ManifestJSON), &m)
	if m["integrity"].(map[string]any)["signature"] != "totally-not-a-real-signature" {
		t.Fatal("签名应原样透传")
	}
}
