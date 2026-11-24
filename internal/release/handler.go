package release

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/apperr"
)

// UserIDFunc 从请求上下文取当前会话用户标识（由装配层注入，业务域不 import server）。
//
// 上传只把它当「操作者」记进 CreatedBy，不据此判权限——权限由装配层挂的
// admin scope + AdminOnly 中间件保证。
type UserIDFunc func(c *gin.Context) (string, bool)

// ---------- 路由注册 ----------

// RegisterSession 挂载 App 侧只读路由。
//
// 这些路由挂在 engine 上，受全局「请求签名 + 认证」中间件约束：
//   - 请求签名：由装配层的 installAppSigning 挂，客户端必须带 NY1 头；
//   - **用户会话必需**：本域路径不进 publicReadablePaths。
//
// 为什么查版本也要会话（而不是像「公开信息」那样免登录）：见 Spec #80 的
// 「已知边界」——维护者决定基本所有业务都要认证，未登录设备无法升级是
// 明确接受的后果，不是遗漏。
func RegisterSession(r gin.IRouter, svc *Service) {
	r.GET(LatestBasePath, latestHandler(svc))
	r.GET(LatestBasePath+"/pack", latestPackHandler(svc))
	r.GET(platformBasePath+"/:versionCode/pack", packHandler(svc))
}

// RegisterAdmin 挂载 B 端（管理台）路由。装配层负责 admin scope + 角色门槛。
//
// 上传放 /admin/* 而非 App 侧的原因与皮肤包域相同：NY1 请求签名中间件会把
// 整个 body 读进内存且上限仅 1 MiB，而安装包可达数百 MB；管理台由浏览器
// 调用、走 admin 会话 + role 三档，正是既有 /admin/* 的形态。
func RegisterAdmin(r gin.IRouter, svc *Service, userIDOf UserIDFunc) {
	if userIDOf == nil {
		userIDOf = func(*gin.Context) (string, bool) { return "", false }
	}
	r.POST("/admin/releases", uploadHandler(svc, userIDOf))
}

// ---------- 响应形状 ----------

// latestView 是 /latest 的响应体。
//
// 刻意只给客户端**需要**的字段：objectKey 与对象存储信息一律不下发。
type latestView struct {
	// HasRelease=false 表示该应用还没有任何已发布版本（正常态，不是错误）。
	HasRelease              bool   `json:"hasRelease"`
	ApplicationID           string `json:"applicationId,omitempty"`
	VersionCode             int64  `json:"versionCode,omitempty"`
	VersionName             string `json:"versionName,omitempty"`
	MinSdk                  int    `json:"minSdk,omitempty"`
	MinSupportedVersionCode int64  `json:"minSupportedVersionCode,omitempty"`
	Changelog               string `json:"changelog,omitempty"`
	Size                    int64  `json:"size,omitempty"`
	SHA256                  string `json:"sha256,omitempty"`
	// DownloadURL 只指向本服务，**不是**对象存储地址或预签名 URL。
	DownloadURL string `json:"downloadUrl,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
}

// releaseView 是上传成功后的响应体（不含对象 key 等内部字段）。
type releaseView struct {
	ApplicationID           string `json:"applicationId"`
	VersionCode             int64  `json:"versionCode"`
	VersionName             string `json:"versionName"`
	MinSdk                  int    `json:"minSdk,omitempty"`
	MinSupportedVersionCode int64  `json:"minSupportedVersionCode,omitempty"`
	Changelog               string `json:"changelog,omitempty"`
	Size                    int64  `json:"size"`
	SHA256                  string `json:"sha256"`
	DownloadURL             string `json:"downloadUrl"`
	CreatedAt               string `json:"createdAt"`
}

func toLatestView(rec *Release) latestView {
	return latestView{
		HasRelease:              true,
		ApplicationID:           rec.AppID,
		VersionCode:             rec.VersionCode,
		VersionName:             rec.VersionName,
		MinSdk:                  rec.MinSdk,
		MinSupportedVersionCode: rec.MinSupportedVersionCode,
		Changelog:               rec.Changelog,
		Size:                    rec.Size,
		SHA256:                  rec.SHA256,
		DownloadURL:             DownloadPath(rec.VersionCode),
		UpdatedAt:               rec.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

func toReleaseView(rec *Release) releaseView {
	return releaseView{
		ApplicationID:           rec.AppID,
		VersionCode:             rec.VersionCode,
		VersionName:             rec.VersionName,
		MinSdk:                  rec.MinSdk,
		MinSupportedVersionCode: rec.MinSupportedVersionCode,
		Changelog:               rec.Changelog,
		Size:                    rec.Size,
		SHA256:                  rec.SHA256,
		DownloadURL:             DownloadPath(rec.VersionCode),
		CreatedAt:               rec.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// ---------- App 侧 handler ----------

// appIDParam 是查询/下载端点里 applicationId 的 query 参数名。
//
// 为什么用 query 而不是路径段：`applicationId` 含点号（com.nongyu.app），
// 而本仓路由用 gin 的路径参数，点号在路径段里会与「文件扩展名」类路由
// 语义混淆；同时路径里已经有 :versionCode 这个数字段，再塞一个带点号的段
// 会让 URL 形状变得难读。query 参数对两者都安全。
const appIDParam = "applicationId"

// latestHandler 返回最新版本的元数据。
//
// 没有已发布版本时返回 200 + {"hasRelease": false}，不返回 404：
// 「尚无版本」是正常态（新应用第一次发布之前），404 会被很多 HTTP 客户端
// 当成异常处理，逼客户端把「正常」写成 `if err == NotFound`。
func latestHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		appID := c.Query(appIDParam)
		rec, err := svc.Latest(c.Request.Context(), appID)
		if err != nil {
			if errors.Is(err, ErrNoRelease) {
				c.JSON(http.StatusOK, latestView{HasRelease: false})
				return
			}
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, toLatestView(rec))
	}
}

// latestPackHandler 下载最新版本的安装包。
func latestPackHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		rec, err := svc.Latest(c.Request.Context(), c.Query(appIDParam))
		if err != nil {
			if errors.Is(err, ErrNoRelease) {
				apperr.WriteGin(c, apperr.Errorf(apperr.CodeNotFound, "该应用还没有任何已发布版本"))
				return
			}
			apperr.WriteGin(c, err)
			return
		}
		writePack(c, svc, rec)
	}
}

// packHandler 下载指定 versionCode 的安装包。
func packHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.Param("versionCode")
		code, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed,
				"versionCode 必须是整数，得到 %q", raw))
			return
		}
		rec, err := svc.Get(c.Request.Context(), c.Query(appIDParam), code)
		if err != nil {
			if errors.Is(err, ErrReleaseNotFound) {
				apperr.WriteGin(c, apperr.Errorf(apperr.CodeNotFound,
					"应用 %s 的版本 %d 不存在", c.Query(appIDParam), code))
				return
			}
			apperr.WriteGin(c, err)
			return
		}
		writePack(c, svc, rec)
	}
}

// writePack 把安装包以流式方式写进响应。
//
// 流式是硬要求：安装包可达数百 MB，读进内存会让并发下载变成内存放大。
// 头里带整包 sha256 与长度，客户端下完即可校验字节完整性（本域不校验
// 包内容，但「你下到的和服务器存的是不是同一份」必须可证）。
func writePack(c *gin.Context, svc *Service, rec *Release) {
	rc, err := svc.OpenPack(c.Request.Context(), rec)
	if err != nil {
		apperr.WriteGin(c, err)
		return
	}
	defer func() { _ = rc.Close() }()

	c.Header("Content-Type", ApkContentType)
	c.Header("Content-Disposition", "attachment")
	c.Header(SHA256Header, rec.SHA256)
	c.Header("Content-Length", strconv.FormatInt(rec.Size, 10))
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, rc); err != nil {
		// 头已发出，无法改成错误响应；只记录（客户端会因长度不符 + sha256
		// 不匹配而发现截断，这正是 SHA256Header 存在的意义之一）。
		_ = c.Error(err)
	}
}

// ---------- 管理端 handler ----------

// uploadHandler 处理 multipart 上传。
//
// 字段：applicationId / versionCode / versionName / minSupportedVersionCode /
// changelog（可选）/ minSdk（可选）/ file（必填）。
func uploadHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		operator, _ := userIDOf(c)

		// 整体请求上限：包体上限 + 给其他字段留的余量。先在这里挡住
		// 「上传一个几 GB 的文件把内存打满」。
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxPackBytes+(1<<20))

		var (
			packBytes    []byte
			appID        string
			versionName  string
			changelog    string
			versionCode  int64
			minSupported int64
			minSdk       int
			haveCode     bool
		)

		mr, err := c.Request.MultipartReader()
		if err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed,
				"请求必须是 multipart/form-data：%v", err))
			return
		}
		for {
			part, perr := mr.NextPart()
			if perr != nil {
				if errors.Is(perr, io.EOF) {
					break
				}
				apperr.WriteGin(c, tooLargeOrValidation(perr))
				return
			}
			switch part.FormName() {
			case "file":
				limited := http.MaxBytesReader(c.Writer, part, MaxPackBytes)
				b, rerr := io.ReadAll(limited)
				_ = part.Close()
				if rerr != nil {
					apperr.WriteGin(c, tooLargeOrValidation(rerr))
					return
				}
				packBytes = b
			case "applicationId":
				appID = readPartString(part)
			case "versionName":
				versionName = readPartString(part)
			case "changelog":
				changelog = readPartString(part)
			case "versionCode":
				v := readPartString(part)
				n, cerr := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
				if cerr != nil {
					_ = part.Close()
					apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed,
						"versionCode 必须是整数，得到 %q", v))
					return
				}
				versionCode, haveCode = n, true
			case "minSupportedVersionCode":
				v := readPartString(part)
				if strings.TrimSpace(v) == "" {
					break
				}
				n, cerr := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
				if cerr != nil {
					apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed,
						"minSupportedVersionCode 必须是整数，得到 %q", v))
					return
				}
				minSupported = n
			case "minSdk":
				v := readPartString(part)
				if strings.TrimSpace(v) == "" {
					break
				}
				n, cerr := strconv.Atoi(strings.TrimSpace(v))
				if cerr != nil {
					apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed,
						"minSdk 必须是整数，得到 %q", v))
					return
				}
				minSdk = n
			default:
				_ = part.Close()
			}
		}

		if len(packBytes) == 0 {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "缺少 file 字段或文件为空"))
			return
		}
		if !haveCode {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "缺少 versionCode 字段"))
			return
		}

		rec, uerr := svc.Upload(c.Request.Context(), UploadInput{
			PackBytes:               packBytes,
			AppID:                   appID,
			VersionCode:             versionCode,
			VersionName:             versionName,
			MinSdk:                  minSdk,
			MinSupportedVersionCode: minSupported,
			Changelog:               changelog,
			CreatedBy:               operator,
		})
		if uerr != nil {
			apperr.WriteGin(c, uerr)
			return
		}
		c.JSON(http.StatusCreated, toReleaseView(rec))
	}
}

// readPartString 读完一个文本 part 并返回内容（失败返回空串）。
func readPartString(part interface {
	Read([]byte) (int, error)
	Close() error
}) string {
	b, err := io.ReadAll(io.LimitReader(part, 1<<20))
	_ = part.Close()
	if err != nil {
		return ""
	}
	return string(b)
}

// tooLargeOrValidation 把 *http.MaxBytesError 映射成 payload_too_large（413），
// 其余 multipart 读取错误映射成 validation_failed（400）。
func tooLargeOrValidation(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return apperr.Errorf(apperr.CodePayloadTooLarge, "安装包超过 %d 字节", MaxPackBytes)
	}
	return apperr.Errorf(apperr.CodeValidationFailed, "multipart 读取失败：%v", err)
}

// 编译期确认：JSON 编解码仍走标准库（响应体是裸结构体，不做自定义序列化）。
var _ = json.Marshal
