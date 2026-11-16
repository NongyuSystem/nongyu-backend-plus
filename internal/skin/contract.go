// Package skin 是「皮肤包上传 / 下载」业务域（issue #53 / ADR-0003、0004、0006）。
//
// 与 nongyu-app-plus 客户端的契约对齐点：
//   - 包体是一个 zip：根目录含 manifest.json + 清单 integrity.entries 声明的资源文件
//     （图标/字体/位图，白名单扩展名）。
//   - manifest 的 6 项必填：packId / schemaVersion / minAppVersion /
//     displayName / payload / integrity（客户端只消费 payload.iconStrokes、
//     payload.fontFileName、integrity.entries、integrity.signature）。
//   - 完整性由**应用层**判（ADR-0003）：逐文件 sha256 + 整包签名。整包签名
//     是「对规范化摘要签名」，客户端持有验签公钥；服务端**不产生也不校验**
//     该签名的真伪（服务端不是信任根，见 docs/research/skin-decoupling-routes.md
//     「预签名 URL 只解谁能取，不解是不是官方包」）。服务端职责是：
//     ① 逐文件 sha256 与清单一致（防止上传半成品/被篡改的包）；
//     ② 路径与扩展名白名单（防止目录穿越 / 夹带 dex/so）；
//     ③ 把 B 端给的 signature 原样随包下发，由客户端验签。
//
// 本文件只放**纯类型与纯判定**（不碰 zip / HTTP / SQL / gin），
// 让契约能被纯 JVM 式的 Go 单测直接覆盖。
package skin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"nongyu-backend-plus/internal/apperr"
)

const (
	// SupportedSchemaMajor 与客户端 SkinPackManifest.SUPPORTED_SCHEMA_MAJOR 对齐（=1）。
	// 主版本高于此值客户端会整包拒绝，因此服务端也必须拒绝入库，避免下发必然失败
	// 的包。这是跨仓契约常量，改客户端时**必须同步**改这里。
	SupportedSchemaMajor = 1

	// ManifestFileName 是包根清单文件名。
	ManifestFileName = "manifest.json"

	// MaxPackBytes 是单个皮肤包（zip）的体积上限（20 MiB）。
	// 皮肤包只含图标/字体/位图，比帖子附件的 5 MiB 宽，但仍需硬限挡住
	// 「上传一个几 GB 的 zip 把内存/对象存储打满」。
	MaxPackBytes int64 = 20 << 20

	// MaxManifestBytes 是 manifest.json 的体积上限（256 KiB）。
	MaxManifestBytes = 256 << 10

	// MaxEntryCount 是包内文件数上限。防止 zip 炸弹式的海量小文件。
	MaxEntryCount = 512

	// objectKeyPrefix 是皮肤包对象 key 的前缀（与 posts/ 分区隔离，单桶+前缀）。
	objectKeyPrefix = "skins"
)

// AllowedExtensions 与客户端 SkinPackIntegrity.ALLOWED_EXTENSIONS 逐项对齐。
// 白名单而非黑名单：皮肤包需要的文件类型是封闭已知的，白名单能把"没想到的类型"
// 一并挡掉（json / 字体 / 位图）。
var AllowedExtensions = map[string]bool{
	"json": true,
	"ttf":  true, "otf": true, "ttc": true,
	"png": true, "webp": true, "jpg": true, "jpeg": true,
}

// ForbiddenExecutableExtensions 与客户端同名集合对齐，单独列出只为给出可读拒绝原因。
var ForbiddenExecutableExtensions = map[string]bool{
	"dex": true, "jar": true, "so": true, "apk": true, "aab": true,
	"class": true, "odex": true, "oat": true, "vdex": true,
	"dylib": true, "sh": true, "bat": true, "exe": true,
}

// safeSegmentRe 与客户端 SkinStoreLayout.isSafeSegment 同语义：
// packId / version 会成为磁盘目录名，只许字母数字与 -_.，且不含 ".."、
// 不许 "." / ".." 整体。
var safeSegmentRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// sha256HexRe 校验清单里的 sha256 必须是 64 位小写十六进制（客户端 parseMap
// 的判据：isDigit() || it in 'a'..'f'，长度 64）。
var sha256HexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// IsSafeSegment 判断 packId / version 能否安全用作磁盘目录名 / URL 路径段。
// 与客户端 isSafeSegment 对齐（含空、.、..、含 .. 一律否）。
func IsSafeSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	if strings.Contains(s, "..") {
		return false
	}
	return safeSegmentRe.MatchString(s)
}

// Entry 是清单 integrity.entries 里的一项：规范化相对路径 + sha256。
type Entry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Integrity 是 manifest 的完整性块。
// Signature 是 B 端对 canonicalPayload(entries) 的签名（base64），服务端原样
// 存储与下发，**不校验真伪**（信任根在客户端公钥）。空签名客户端必拒，故服务端
// 把它列为必填，避免下发必然失败的包。
type Integrity struct {
	Entries   []Entry `json:"entries"`
	Signature string  `json:"signature"`
}

// Payload 是 manifest 的资源载荷段。
// 客户端消费 iconStrokes（图标名→线宽系数）与 fontFileName（包内字体路径）。
// Colors 段按 ADR-0004 已 deprecated，服务端不建模、不解析。
type Payload struct {
	IconStrokes  map[string]float64 `json:"iconStrokes,omitempty"`
	FontFileName string             `json:"fontFileName,omitempty"`
}

// Manifest 是皮肤包清单（包根 manifest.json）。
// 只建模客户端真正会读的字段；ADR-0003「结构预留、值为空」的 cover/author/
// 投放条件/A-B/增量包/回滚标记刻意不建模——同主版本未知字段本就该被忽略。
type Manifest struct {
	PackID        string    `json:"packId"`
	SchemaVersion int       `json:"schemaVersion"`
	MinAppVersion int       `json:"minAppVersion"`
	DisplayName   string    `json:"displayName"`
	Payload       Payload   `json:"payload"`
	Integrity     Integrity `json:"integrity"`
}

// Pack 是一条已入库皮肤包的元数据（不含包体字节）。
// ObjectKey 指向 MinIO 里的 zip；ManifestJSON 是解析并校验通过后**规范化**
// 的清单文本（随目录接口与详情下发，客户端直接 parseJson）。
type Pack struct {
	ID            int64
	PackID        string
	Version       string
	SchemaVersion int
	MinAppVersion int
	DisplayName   string
	ObjectKey     string
	PackSHA256    string
	PackSize      int64
	ManifestJSON  string
	CreatedAt     time.Time
	CreatedBy     string
}

// Current 是「全局单指针」视图（ADR-0003 / 0004）：服务端只提供当前皮肤，
// 所有用户同一套；B 端上传新版即换指针。不做用户可选列表。
type Current struct {
	PackID        string
	Version       string
	SchemaVersion int
	MinAppVersion int
	DisplayName   string
	ObjectKey     string
	PackSHA256    string
	PackSize      int64
	ManifestJSON  string
	UpdatedAt     time.Time
}

// ObjectKey 生成皮肤包对象 key：skins/{packId}/{version}/{id}.zip。
//
// id 由每次上传**唯一生成**（不按 packId/version 确定性派生），这是刻意的：
// 同一 (packId, version) 的重复上传必须失败且**不得覆盖/删除已生效那版的包体**。
// 若 key 确定性派生，「重复上传 → Create 失败 → 回滚删对象」会把线上包体删掉；
// 唯一 key 让回滚只删本次刚写的那一个对象，与已登记版本互不影响。
func ObjectKey(packID, version, id string) string {
	return fmt.Sprintf("%s/%s/%s/%s.zip", objectKeyPrefix, packID, version, id)
}

// NewObjectID 生成一个对象 key 的随机段（16 字节 hex，32 字符）。
// 用 crypto/rand：几十万次上传的碰撞概率可忽略，且不可被外部猜中。
func NewObjectID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// CheckPathAllowed 与客户端 SkinPackIntegrity.checkPathAllowed 逐条对齐：
// 空路径 / 绝对路径 / 目录穿越 / 目录条目 / 无扩展名 / 禁用可执行扩展名 /
// 未知扩展名。返回 nil = 合法，否则返回人类可读的拒绝原因。
//
// 服务端在解包**之前**对 zip 内每个文件名、以及 manifest.entries 里每个 path
// 都跑一遍，双保险（zip 里可能有清单没声明的夹带文件）。
func CheckPathAllowed(p string) error {
	if strings.TrimSpace(p) == "" {
		return errors.New("包内存在空路径条目")
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return fmt.Errorf("包内存在绝对路径条目：%s", p)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("包内条目试图目录穿越：%s", p)
	}
	if strings.HasSuffix(p, "/") {
		return fmt.Errorf("包内条目是目录而不是文件：%s", p)
	}
	// 反斜杠在客户端被当作路径分隔符取 basename；这里直接拒绝（zip 规范用 /）。
	if strings.Contains(p, `\`) {
		return fmt.Errorf("包内条目含非法反斜杠：%s", p)
	}
	name := path.Base(p)
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 || dot == len(name)-1 {
		return fmt.Errorf("包内条目没有扩展名：%s", p)
	}
	ext := strings.ToLower(name[dot+1:])
	if ForbiddenExecutableExtensions[ext] {
		return fmt.Errorf("皮肤包禁止包含可执行文件：%s（.%s）", p, ext)
	}
	if !AllowedExtensions[ext] {
		return fmt.Errorf("皮肤包包含未知扩展名：%s（.%s）", p, ext)
	}
	return nil
}

// validateEntry 校验单条 entry 的形状（路径合法 + sha256 为 64 位小写 hex）。
func validateEntry(e Entry) error {
	if err := CheckPathAllowed(e.Path); err != nil {
		return err
	}
	if !sha256HexRe.MatchString(e.SHA256) {
		return fmt.Errorf("条目 %s 的 sha256 非法（须 64 位小写十六进制）", e.Path)
	}
	return nil
}

// ValidateManifest 校验一份已解析的清单是否满足下发契约。
//
// 这是**纯函数**：不碰 IO，可被单测直接构造覆盖。对齐客户端 parseMap 的
// 6 项必填 + entries 非空/合法/不重复 + 字体路径须出现在 entries 里 +
// schemaVersion 主版本不高于客户端支持 + signature 非空。
func ValidateManifest(m *Manifest) error {
	if m == nil {
		return apperr.Errorf(apperr.CodeValidationFailed, "manifest 缺失")
	}
	if strings.TrimSpace(m.PackID) == "" || !IsSafeSegment(m.PackID) {
		return apperr.Errorf(apperr.CodeValidationFailed, "packId 缺失或含非法字符（只许字母数字与 -_.）")
	}
	if m.SchemaVersion <= 0 {
		return apperr.Errorf(apperr.CodeValidationFailed, "schemaVersion 必须为正整数")
	}
	if m.SchemaVersion > SupportedSchemaMajor {
		return apperr.Errorf(apperr.CodeSchemaRejected, "schemaVersion 主版本 %d 高于客户端支持的 %d", m.SchemaVersion, SupportedSchemaMajor)
	}
	if m.MinAppVersion < 0 {
		return apperr.Errorf(apperr.CodeValidationFailed, "minAppVersion 不能为负")
	}
	if strings.TrimSpace(m.DisplayName) == "" {
		return apperr.Errorf(apperr.CodeValidationFailed, "displayName 缺失")
	}
	if strings.TrimSpace(m.Integrity.Signature) == "" {
		return apperr.Errorf(apperr.CodeValidationFailed, "integrity.signature 缺失（客户端会因缺签名整包拒绝）")
	}
	if len(m.Integrity.Entries) == 0 {
		return apperr.Errorf(apperr.CodeValidationFailed, "integrity.entries 为空（没有任何可替换资源）")
	}
	if len(m.Integrity.Entries) > MaxEntryCount {
		return apperr.Errorf(apperr.CodeValidationFailed, "integrity.entries 超过 %d 条", MaxEntryCount)
	}

	seen := make(map[string]bool, len(m.Integrity.Entries))
	for _, e := range m.Integrity.Entries {
		if err := validateEntry(e); err != nil {
			return apperr.Errorf(apperr.CodeValidationFailed, "%v", err)
		}
		if seen[e.Path] {
			return apperr.Errorf(apperr.CodeValidationFailed, "integrity.entries 路径重复：%s", e.Path)
		}
		seen[e.Path] = true
	}

	// 字体路径若声明，必须是合法相对路径且出现在 entries 里（客户端 parseMap 同判据）。
	if f := strings.TrimSpace(m.Payload.FontFileName); f != "" {
		if err := CheckPathAllowed(f); err != nil {
			return apperr.Errorf(apperr.CodeValidationFailed, "fontFileName 非法：%v", err)
		}
		if !seen[f] {
			return apperr.Errorf(apperr.CodeValidationFailed, "fontFileName %s 未出现在 integrity.entries 里", f)
		}
	}
	return nil
}

// Sha256Hex 计算字节的 sha256 小写十六进制（与客户端 sha256Hex 同口径）。
func Sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
