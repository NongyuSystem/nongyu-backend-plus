package skin

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/platform"
)

// UserIDFunc 从请求上下文取当前会话用户标识（由装配层注入，业务域不 import server）。
// 皮肤上传只把它当「操作者」记进 CreatedBy，不据此判权限——权限由装配层挂的
// admin scope + AdminOnly 中间件保证。
type UserIDFunc func(c *gin.Context) (string, bool)

// CatalogBasePath 是目录接口拼 downloadUrl 时用的公开路径前缀。
const CatalogBasePath = "/skins"

// ---------- 路由注册 ----------

// RegisterPublic 挂载 App 侧只读路由（公开可读：无需用户会话，但仍要 NY1 签名）。
//
// 这些路由挂在 engine 上，受全局「请求签名 + 认证」中间件约束：
//   - 请求签名：由装配层的 installAppSigning 挂，客户端必须带 NY1 头；
//   - 免用户会话：路径须加进 server.publicReadablePaths（见装配层），否则
//     未登录设备的「首帧后拉取」会被 401 拦住 —— 那会让 ADR-0006 的
//     「冷启动只读本地、首帧后再刷新」整条链断掉。
//
// 全部是 GET/HEAD：皮肤是全局单指针下发，不含用户私有数据，可公开读。
func RegisterPublic(r gin.IRouter, svc *Service) {
	r.GET("/skins/current", currentHandler(svc))
	r.GET("/skins/:packId/:version", detailHandler(svc))
	r.GET("/skins/:packId/:version/pack", downloadHandler(svc))
}

// RegisterAdmin 挂载 B 端（管理台）路由。装配层负责 admin scope + 角色门槛。
// 上传放 /admin/* 而非 App 侧：NY1 请求签名中间件会把整个 body 读进内存且
// 上限仅 1 MiB（appsign.go appSignMaxBody），而皮肤包可达 20 MiB；
// 管理台由浏览器调用、走 admin 会话 + role 三档，正是既有 /admin/* 的形态。
func RegisterAdmin(r gin.IRouter, svc *Service, userIDOf UserIDFunc) {
	if userIDOf == nil {
		userIDOf = func(*gin.Context) (string, bool) { return "", false }
	}
	r.POST("/admin/skins", uploadHandler(svc, userIDOf))
	r.GET("/admin/skins", listHandler(svc))
	r.POST("/admin/skins/:packId/:version/activate", activateHandler(svc))
}

// ---------- App 侧 ----------

// currentHandler 是「目录接口」：客户端首帧后拉的就是它。
// 没有已激活包时返回 200 + {"hasPack":false}，客户端据此回落内置默认皮肤，
// 不返回 404 —— 404 会被很多 HTTP 客户端当成异常，而「没有皮肤」是正常态。
func currentHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		cat, err := svc.Catalog(c.Request.Context(), CatalogBasePath)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, cat)
	}
}

// detailHandler 返回单个包的目录级详情。
func detailHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		e, err := svc.Detail(c.Request.Context(), c.Param("packId"), c.Param("version"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, e)
	}
}

// downloadHandler 是包体下载（后端流式代理对象存储）。
//
// 用 c.DataFromReader 流式转发，不把 20 MiB 读进内存；设置 Content-Disposition
// 与整包 sha256 到响应头，客户端可在写盘前先比对（ADR-0003 应用层完整性）。
// 头部 Content-Length 由 DataFromReader 的第三个参数给出（来自 PackSize）。
func downloadHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, rc, err := svc.Download(c.Request.Context(), c.Param("packId"), c.Param("version"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		defer rc.Close()
		c.Header("Content-Disposition", `attachment; filename="`+p.PackID+"-"+p.Version+`.zip"`)
		c.Header("Content-Type", "application/zip")
		c.Header("X-Nongyu-Pack-Sha256", p.PackSHA256)
		c.DataFromReader(http.StatusOK, p.PackSize, "application/zip", rc, nil)
	}
}

// ---------- 管理端 ----------

// uploadHandler 处理 multipart/form-data 上传。
//
// 字段：version（必填）、activate（可选，"true" 立即设为当前）、file（zip 本体）。
// 体积两道闸：① 整个请求体 MaxBytesReader（MaxPackBytes + multipart 开销）；
// ② file 部件单独再包一道 MaxBytesReader(MaxPackBytes)，超限映射成 413。
// 与 post.Upload 同手法：用 MultipartReader 逐部件流式读，不用 ParseMultipartForm
// （它会把整个文件读进内存 / 落到临时文件）。
func uploadHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		operator, _ := userIDOf(c)

		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxPackBytes+(1<<20))
		mr, err := c.Request.MultipartReader()
		if err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeUnsupportedMedia, "请求不是 multipart/form-data"))
			return
		}

		var version, activate string
		var packBytes []byte
		for {
			part, perr := mr.NextPart()
			if perr == io.EOF {
				break
			}
			if perr != nil {
				apperr.WriteGin(c, tooLargeOrValidation(perr))
				return
			}
			switch part.FormName() {
			case "version":
				b, rerr := io.ReadAll(io.LimitReader(part, 256))
				_ = part.Close()
				if rerr != nil {
					apperr.WriteGin(c, tooLargeOrValidation(rerr))
					return
				}
				version = strings.TrimSpace(string(b))
			case "activate":
				b, rerr := io.ReadAll(io.LimitReader(part, 16))
				_ = part.Close()
				if rerr != nil {
					apperr.WriteGin(c, tooLargeOrValidation(rerr))
					return
				}
				activate = strings.TrimSpace(string(b))
			case "file":
				// 单文件硬限：超 MaxPackBytes 时 MaxBytesReader 读会返回 *MaxBytesError。
				limited := http.MaxBytesReader(c.Writer, part, MaxPackBytes)
				b, rerr := io.ReadAll(limited)
				_ = part.Close()
				if rerr != nil {
					apperr.WriteGin(c, tooLargeOrValidation(rerr))
					return
				}
				packBytes = b
			default:
				_ = part.Close()
			}
		}
		if len(packBytes) == 0 {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "缺少 file 字段或文件为空"))
			return
		}

		p, err := svc.Upload(c.Request.Context(), UploadInput{
			PackBytes: packBytes,
			Version:   version,
			CreatedBy: operator,
			Activate:  activate == "true" || activate == "1",
		})
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusCreated, MarshalCatalogEntry(p))
	}
}

// listHandler 返回已登记皮肤包列表。
func listHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.Query("limit"))
		out, err := svc.List(c.Request.Context(), limit)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": out})
	}
}

// activateHandler 把全局单指针切到某个已登记版本（发布 / 回滚）。
func activateHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Activate(c.Request.Context(), c.Param("packId"), c.Param("version")); err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "packId": c.Param("packId"), "version": c.Param("version")})
	}
}

// tooLargeOrValidation 把 *http.MaxBytesError 映射成 payload_too_large（413），
// 其余 multipart 读取错误映射成 validation_failed（400）。
func tooLargeOrValidation(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return apperr.Errorf(apperr.CodePayloadTooLarge, "皮肤包超过 %d 字节", MaxPackBytes)
	}
	return apperr.Errorf(apperr.CodeValidationFailed, "multipart 读取失败：%v", err)
}

// 编译期确认：Service 用到的对象存储接缝仍是 platform.ObjectStore（Get 已加入）。
var _ = platform.ErrObjectNotFound
