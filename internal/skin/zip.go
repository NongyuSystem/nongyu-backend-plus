package skin

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"nongyu-backend-plus/internal/apperr"
)

const (
	// MaxTotalUncompressed 是 zip 解压后所有文件字节总和上限（64 MiB）。
	// zip 炸弹防护：一个几十 KB 的 zip 可以解开成 GB 级内容，只限 zip 本体
	// （MaxPackBytes）挡不住。皮肤包正常远小于此值。
	MaxTotalUncompressed int64 = 64 << 20
)

// VerifyInput 是一次上传校验的输入（纯内存，便于单测构造）。
type VerifyInput struct {
	// PackBytes 是完整的 zip 字节（服务端从 multipart 读入，已受 MaxPackBytes 限）。
	PackBytes []byte
}

// VerifyResult 是校验通过后的产物。
type VerifyResult struct {
	Manifest     *Manifest // 已解析并校验通过的清单
	ManifestJSON string    // 包内 manifest.json 的原文（原样下发，保证与包内一致）
	PackSHA256   string    // 整个 zip 的 sha256（目录接口的整包校验值）
	PackSize     int64     // zip 字节数
	EntryCount   int       // 资源文件数（不含 manifest.json）
}

// VerifyPack 校验一个皮肤包 zip，返回解析后的清单与整包摘要。
//
// 校验链（与客户端「网络 → 校验 → 落盘」对称，服务端在**入库前**先挡一道）：
//  1. zip 本体非空、体积不超 MaxPackBytes、能被 archive/zip 打开；
//  2. 逐文件名跑 CheckPathAllowed（挡绝对路径 / 目录穿越 / 可执行文件 / 未知扩展名）；
//     文件名不重复；解压后总字节不超 MaxTotalUncompressed（zip 炸弹）；
//  3. 包根必须有且只有一个 manifest.json，且体积不超 MaxManifestBytes；
//  4. 解析 manifest.json → ValidateManifest（6 项必填 / entries 合法 / 主版本 / 签名非空）；
//  5. 包内除 manifest.json 外的每个文件都必须在 integrity.entries 里声明
//     （挡「清单没声明的夹带」）；每个声明的 entry 都必须在包里存在；
//  6. 逐文件 sha256 必须与清单一致（挡半成品 / 篡改）。
//
// 注意服务端**不校验整包签名的真伪**：签名是 B 端私钥对规范化摘要签的，
// 信任根（公钥）在客户端，服务端只把它原样存下来随包下发。理由见
// docs/research/skin-decoupling-routes.md：预签名 URL / 传输只解「谁能取」，
// 不解「是不是官方包」，完整性最终由应用侧判。
func VerifyPack(in VerifyInput) (*VerifyResult, error) {
	raw := in.PackBytes
	if len(raw) == 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "皮肤包为空")
	}
	if int64(len(raw)) > MaxPackBytes {
		return nil, apperr.Errorf(apperr.CodePayloadTooLarge, "皮肤包超过 %d 字节", MaxPackBytes)
	}

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "不是合法的 zip 包：%v", err)
	}
	if len(zr.File) == 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "皮肤包 zip 内没有任何文件")
	}
	if len(zr.File) > MaxEntryCount+1 { // +1 = manifest.json
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "皮肤包文件数超过 %d", MaxEntryCount)
	}

	// 逐文件读入内存（已受 MaxTotalUncompressed 限），同时做路径与重复检查。
	files := make(map[string][]byte, len(zr.File))
	var total int64
	var manifestRaw []byte
	for _, f := range zr.File {
		name := f.Name
		// zip 目录条目以 "/" 结尾，直接跳过（CheckPathAllowed 也会挡，这里给清晰原因）。
		if strings.HasSuffix(name, "/") {
			continue
		}
		if err := CheckPathAllowed(name); err != nil {
			return nil, apperr.Errorf(apperr.CodeValidationFailed, "%v", err)
		}
		if _, dup := files[name]; dup {
			return nil, apperr.Errorf(apperr.CodeValidationFailed, "皮肤包内文件名重复：%s", name)
		}
		// 先看声明的解压后大小，挡住明显的炸弹，再实际读时再累加一次。
		if f.UncompressedSize64 > uint64(MaxTotalUncompressed) {
			return nil, apperr.Errorf(apperr.CodePayloadTooLarge, "文件 %s 解压后过大", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, apperr.Errorf(apperr.CodeValidationFailed, "无法打开包内文件 %s：%v", name, err)
		}
		// 单文件也受总量上限约束；用 LimitReader 读「上限+1」，超了就判失败，
		// 不让一个畸形声明绕过。
		limited := io.LimitReader(rc, MaxTotalUncompressed+1)
		b, rerr := io.ReadAll(limited)
		_ = rc.Close()
		if rerr != nil {
			return nil, apperr.Errorf(apperr.CodeValidationFailed, "读取包内文件 %s 失败：%v", name, rerr)
		}
		total += int64(len(b))
		if total > MaxTotalUncompressed {
			return nil, apperr.Errorf(apperr.CodePayloadTooLarge, "皮肤包解压后总大小超过 %d 字节", MaxTotalUncompressed)
		}
		if name == ManifestFileName {
			if int64(len(b)) > MaxManifestBytes {
				return nil, apperr.Errorf(apperr.CodeValidationFailed, "manifest.json 超过 %d 字节", MaxManifestBytes)
			}
			manifestRaw = b
			continue // manifest.json 不进资源文件集合
		}
		files[name] = b
	}

	if manifestRaw == nil {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "皮肤包根目录缺少 manifest.json")
	}

	var m Manifest
	if err := json.Unmarshal(manifestRaw, &m); err != nil {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "manifest.json 不是合法 JSON：%v", err)
	}
	if err := ValidateManifest(&m); err != nil {
		return nil, err
	}

	// 交叉校验：声明集合 == 包内资源集合，且逐文件 sha256 一致。
	declared := make(map[string]string, len(m.Integrity.Entries))
	for _, e := range m.Integrity.Entries {
		declared[e.Path] = e.SHA256
	}
	for name := range files {
		if _, ok := declared[name]; !ok {
			return nil, apperr.Errorf(apperr.CodeValidationFailed, "包内存在清单未声明的文件：%s", name)
		}
	}
	for name, want := range declared {
		b, ok := files[name]
		if !ok {
			return nil, apperr.Errorf(apperr.CodeValidationFailed, "清单声明的文件缺失：%s", name)
		}
		if got := Sha256Hex(b); got != want {
			return nil, apperr.Errorf(apperr.CodeValidationFailed, "文件 %s 的 sha256 不符（清单 %s，实际 %s）", name, want, got)
		}
	}

	return &VerifyResult{
		Manifest:     &m,
		ManifestJSON: string(manifestRaw),
		PackSHA256:   Sha256Hex(raw),
		PackSize:     int64(len(raw)),
		EntryCount:   len(files),
	}, nil
}

// MarshalCatalogEntry 把 pack 元数据编成目录/列表接口的单项（内部共用）。
// 单独成函数避免目录与列表两处各拼一遍字段导致漂移。
func MarshalCatalogEntry(p *Pack) CatalogEntry {
	return CatalogEntry{
		PackID:        p.PackID,
		Version:       p.Version,
		SchemaVersion: p.SchemaVersion,
		MinAppVersion: p.MinAppVersion,
		DisplayName:   p.DisplayName,
		PackSHA256:    p.PackSHA256,
		PackSize:      p.PackSize,
		Manifest:      json.RawMessage(p.ManifestJSON),
		CreatedAt:     p.CreatedAt,
	}
}

// CatalogEntry 是目录/列表接口里的一条（不含对象 key 等内部字段）。
// Manifest 用 json.RawMessage：原文透传，客户端可直接 parseJson，
// 服务端不重新序列化、不改变字段顺序与签名所依赖的内容。
type CatalogEntry struct {
	PackID        string          `json:"packId"`
	Version       string          `json:"version"`
	SchemaVersion int             `json:"schemaVersion"`
	MinAppVersion int             `json:"minAppVersion"`
	DisplayName   string          `json:"displayName"`
	PackSHA256    string          `json:"packSha256"`
	PackSize      int64           `json:"packSize"`
	Manifest      json.RawMessage `json:"manifest"`
	CreatedAt     time.Time       `json:"createdAt"`
}

// ErrNoCurrentPointer 是「还没有任何已激活皮肤包」的哨兵（repo 层返回），
// 供 service 映射成 404。目录接口在没有包时必须如实回空，而不是编造一个包。
var ErrNoCurrentPointer = errors.New("skin: 当前指针为空")
