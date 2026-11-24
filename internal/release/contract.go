// Package release 是「应用发布」业务域（Spec #80）。
//
// 它回答三件事：
//   - 某个应用**最新**的版本是哪一个（按 versionCode 最大者判定，不用激活指针）；
//   - 最新版本的安装包字节流；
//   - 指定版本码的安装包字节流。
//
// 「同产品」在本域的规范表述是「同一应用（Application）」——由 applicationId
// 唯一标识的一个可独立分发应用。详见 GLOSSARY 的「应用」「应用发布」。
//
// 两条与皮肤包域刻意相反的取舍（都写在契约文档里，改前先改契约）：
//  1. **不解析、不校验安装包**：applicationId 与版本号全部由管理端填写，
//     服务端只做格式检查。理由见 Spec #80 的「校验与边界」；
//  2. **不做激活指针、不做回滚**：「最新」= 取 versionCode 最大者。
//     由此产生的已知风险（误传更大 versionCode 会永久成为最新）是维护者
//     明确接受的，不是遗漏。
//
// 本文件只放**纯类型与纯判定**（不碰 HTTP / SQL / 对象存储），让契约能被
// 纯 Go 单测直接覆盖——与 internal/skin 的 contract.go 同一分层习惯。
package release

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"nongyu-backend-plus/internal/apperr"
)

const (
	// PlatformAndroid 是本域目前唯一支持的平台。
	//
	// 路由里保留 /android/ 这一段是刻意的：安装包与版本号在 Android 与 iOS
	// 上是两个完全不同的世界（versionCode 是整数 vs CFBundleVersion 是字符串），
	// 共用一条路径迟早要分叉。将来加 iOS 时新增 /ios/ 段即可，不必改既有路径。
	PlatformAndroid = "android"

	// ApkContentType 是 Android 安装包的标准 MIME 类型。
	ApkContentType = "application/vnd.android.package-archive"

	// SHA256Header 是下载响应里携带整包 sha256 的头名。
	//
	// 它不是「校验包」（本域不校验包内容），只回答「你下到的字节与服务器
	// 存的是不是同一份」。客户端下完比对即可发现传输截断/篡改。
	SHA256Header = "X-Nongyu-Apk-Sha256"

	// MaxPackBytes 是单个安装包的体积上限（300 MiB）。
	//
	// 取值依据：Android App Bundle / APK 的常见体量在数十到两百余 MB 之间，
	// 300 MiB 留出余量。它是**硬限**：上传端用 MaxBytesReader 在服务端执行，
	// 不靠客户端自律。
	MaxPackBytes int64 = 300 << 20

	// ReleaseBasePath 是下载地址的前缀（App 侧只读端点）。
	ReleaseBasePath = "/releases"

	// platformBasePath 是本域路由的平台位。
	platformBasePath = ReleaseBasePath + "/" + PlatformAndroid

	// LatestBasePath 是「最新版本」相关端点的前缀。
	LatestBasePath = platformBasePath + "/latest"

	// objectKeyPrefix 是安装包对象 key 的前缀，与 posts/、skins/ 分区隔离
	// （单桶 + 前缀的既有口径）。
	objectKeyPrefix = "releases"
)

// applicationIDPattern 是 applicationId 的最简合法性判定。
//
// 刻意**不**逐字对齐 Android 规范（那里还有「至少两段」「每段以字母开头」
// 等细则）：本域不解析安装包，applicationId 只是管理员填的一个标识，
// 过严的规则会把合法但少见的包名拒之门外，收益不抵成本。
// 这里挡住的是最危险的一类：空、含路径分隔符、含空白、以 .. 结尾——
// 因为它会成为对象 key 与 URL 路径的一部分。
var applicationIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

// Release 是一条应用发布记录（一个应用的一个版本）。
type Release struct {
	ID          int64
	AppID       string // applicationId（Android 包名），即「应用」的身份
	VersionCode int64  // 版本码：整数、单调，是「最新」判定的唯一尺度
	VersionName string // 版本名：人类可读（如 1.4.2），只作展示
	MinSdk      int    // 可选：安装包要求的最低 Android API，0 表示未填
	// MinSupportedVersionCode 是支持版本下限：低于它客户端必须阻断使用。
	MinSupportedVersionCode int64
	Changelog               string // 更新说明，可空
	ObjectKey               string // 对象存储里的 key（**不下发给客户端**）
	SHA256                  string // 整包 sha256（小写 hex）
	Size                    int64  // 字节数
	CreatedBy               string // 上传操作者（装配层注入）
	CreatedAt               time.Time
}

// UploadInput 是一次上传的输入（包体已在 handler 读入内存并受体积限）。
type UploadInput struct {
	PackBytes               []byte
	AppID                   string
	VersionCode             int64
	VersionName             string
	MinSdk                  int
	MinSupportedVersionCode int64
	Changelog               string
	CreatedBy               string
}

// ValidateApplicationID 判定一个 applicationId 是否可用作对象 key 与 URL 段。
func ValidateApplicationID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return apperr.Errorf(apperr.CodeValidationFailed, "applicationId 不能为空")
	}
	if !applicationIDPattern.MatchString(id) {
		return apperr.Errorf(apperr.CodeValidationFailed,
			"applicationId %q 不合法（应形如 com.example.app：字母开头的点分段，只含字母数字与下划线）", id)
	}
	return nil
}

// ValidateVersionCode 判定版本码是否合法：必须是正整数。
//
// 0 与负数被拒，因为它们会让「取最大」的语义出现歧义（一个 0 版本是否
// 真的发布过？），且 Android 的 versionCode 语义本就是正整数。
func ValidateVersionCode(code int64) error {
	if code <= 0 {
		return apperr.Errorf(apperr.CodeValidationFailed, "versionCode 必须是正整数，得到 %d", code)
	}
	return nil
}

// ValidateVersionName 判定版本名是否合法：非空。
func ValidateVersionName(name string) error {
	if strings.TrimSpace(name) == "" {
		return apperr.Errorf(apperr.CodeValidationFailed, "versionName 不能为空")
	}
	return nil
}

// ValidateMinSupported 判定支持版本下限是否合法：非负，且不得高于本版本码。
//
// 「下限高于本版本码」意味着这个包发布的那一刻就已经低于自己的下限——
// 客户端装上会立刻被阻断，是一条自相矛盾的记录，在入口挡住。
func ValidateMinSupported(minSupported, versionCode int64) error {
	if minSupported < 0 {
		return apperr.Errorf(apperr.CodeValidationFailed, "minSupportedVersionCode 不能为负，得到 %d", minSupported)
	}
	if minSupported > versionCode {
		return apperr.Errorf(apperr.CodeValidationFailed,
			"minSupportedVersionCode (%d) 不能高于本次发布的 versionCode (%d)", minSupported, versionCode)
	}
	return nil
}

// ObjectKey 生成安装包在对象存储里的 key：releases/{appId}/{versionCode}/{随机id}.apk。
//
// 随机后缀是刻意的（沿用皮肤包域实测抓到的教训）：确定性 key 在
// 「重复上传 → 登记失败 → 回滚删对象」这条路径上，会把**线上已生效那版**
// 的包体一起删掉。唯一 key 让回滚只删本次刚写的那个对象。
//
// appId 与 versionCode 都参与路径，是为了让运维在桶里能直接看懂
// 「这是哪个应用的哪一版」，而不必回查数据库。
func ObjectKey(appID string, versionCode int64, id string) string {
	return fmt.Sprintf("%s/%s/%d/%s.apk", objectKeyPrefix, appID, versionCode, id)
}

// NewObjectID 生成对象 key 里的随机段（16 字节 → 32 位 hex）。
func NewObjectID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("release: 生成对象 id 失败：%w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// DownloadPath 返回某个版本的下载路径（相对本服务的路径，不含 host）。
//
// 客户端拿到的 downloadUrl 只指向本服务，**绝不**是对象存储地址或预签名 URL：
// 后者会被外传成可直接抓取的直链，绕过会话与签名。
func DownloadPath(versionCode int64) string {
	return fmt.Sprintf("%s/%d/pack", platformBasePath, versionCode)
}

// LatestDownloadPath 返回最新版本的下载路径。
func LatestDownloadPath() string {
	return LatestBasePath + "/pack"
}
