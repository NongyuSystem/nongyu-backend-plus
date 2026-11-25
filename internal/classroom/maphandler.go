package classroom

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/apperr"
)

// 本文件是校区底图的路由与 HTTP 适配（规格 §7）。

// RegisterMapRoutes 挂载读者侧地图端点（需登录，由全局会话中间件负责）。
func RegisterMapRoutes(r gin.IRouter, svc *Service) {
	r.GET("/classrooms/maps/:campus", mapImageHandler(svc))
	r.GET("/classrooms/maps/:campus/meta", mapMetaHandler(svc))
}

// RegisterMapAdmin 挂载管理端地图端点。
func RegisterMapAdmin(r gin.IRouter, svc *Service) {
	r.GET("/admin/classrooms/maps", mapListHandler(svc))
	r.POST("/admin/classrooms/maps", mapUploadHandler(svc))
	r.PUT("/admin/classrooms/maps/:campus/calibration", mapCalibrationHandler(svc))
}

// ---------- 响应体 ----------

type calibrationView struct {
	AR   float64 `json:"ar"`
	AI   float64 `json:"ai"`
	TX0  float64 `json:"tx0"`
	TY0  float64 `json:"ty0"`
	LNG0 float64 `json:"lng0"`
	LAT0 float64 `json:"lat0"`
	KX   float64 `json:"kx"`
	KY   float64 `json:"ky"`
}

type cornerView struct {
	PX  float64 `json:"px"`
	PY  float64 `json:"py"`
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

type mapView struct {
	Campus          string                `json:"campus"`
	CampusLabel     string                `json:"campusLabel"`
	Width           int                   `json:"width"`
	Height          int                   `json:"height"`
	CoordSystem     string                `json:"coordSystem"`
	ContentType     string                `json:"contentType"`
	SizeBytes       int64                 `json:"sizeBytes"`
	ObjectKey       string                `json:"objectKey"`
	Uploaded        bool                  `json:"uploaded"`
	Calibrated      bool                  `json:"calibrated"`
	Calibration     *calibrationView      `json:"calibration"`
	Corners         map[string]cornerView `json:"corners"`
	CalibrationNote string                `json:"calibrationNote,omitempty"`
	// Hint 用一句话说明该校区当前能做什么，省得客户端自己拼条件。
	Hint string `json:"hint"`
}

func toMapView(m *Map) mapView {
	v := mapView{
		Campus:          string(m.Campus),
		CampusLabel:     m.Campus.Label(),
		Width:           m.Width,
		Height:          m.Height,
		CoordSystem:     m.CoordSystem,
		ContentType:     m.ContentType,
		SizeBytes:       m.SizeBytes,
		ObjectKey:       m.ObjectKey,
		Uploaded:        m.Uploaded(),
		Calibrated:      m.Calibrated(),
		CalibrationNote: m.CalibrationNote,
		Hint:            MapSubmissionHint(m),
	}
	if c := m.Cal; c != nil && m.Calibrated() {
		v.Calibration = &calibrationView{
			AR: c.AR, AI: c.AI, TX0: c.TX0, TY0: c.TY0,
			LNG0: c.LNG0, LAT0: c.LAT0, KX: c.KX, KY: c.KY,
		}
	}
	if cs := m.Corners(); cs != nil {
		v.Corners = make(map[string]cornerView, len(cs))
		for name, c := range cs {
			v.Corners[name] = cornerView{PX: c.PX, PY: c.PY, Lat: c.Lat, Lng: c.Lng}
		}
	}
	return v
}

// ---------- 读者侧 ----------

// mapImageHandler 流式下发底图字节（规格 §7.1）。
//
// 走**后端流式代理**而不是 PresignGet：底图是校内地理数据，不把 MinIO
// 暴露给公网、无 CORS、无预签名过期窗口；也让端到端测试不必起 MinIO
// （与皮肤包域同一判断，见 platform/objectstore.go 的注释）。
func mapImageHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		m, rc, err := svc.OpenMapImage(c.Request.Context(), c.Param("campus"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		defer rc.Close()

		c.Header("Content-Type", m.ContentType)
		// 底图是静态资产：强缓存一天。换图时 object_key 含内容指纹，
		// 客户端带 ?v=<objectKey> 的 URL 自然失效，无需后端主动清缓存。
		c.Header("Cache-Control", "public, max-age=86400")
		// 让客户端能拿到内容指纹，便于自己构造带版本的 URL。
		c.Header("ETag", `"`+m.ObjectKey+`"`)

		c.Status(http.StatusOK)
		if _, err := io.Copy(c.Writer, rc); err != nil {
			// 响应头已发出，无法再改状态码；记录到 gin 的错误通道即可。
			_ = c.Error(err)
		}
	}
}

// mapMetaHandler 下发底图元信息（规格 §7.2）。
func mapMetaHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		m, err := svc.GetMap(c.Request.Context(), c.Param("campus"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toMapView(m)})
	}
}

// ---------- 管理端 ----------

func mapListHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		ms, err := svc.ListMaps(c.Request.Context())
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		out := make([]mapView, 0, len(ms))
		for _, m := range ms {
			out = append(out, toMapView(m))
		}
		// 把还没登记的校区也列出来：管理员需要知道「成都还没传图」，
		// 而不是只看已有的两行去猜缺了什么。
		have := make(map[Campus]bool, len(ms))
		for _, m := range ms {
			have[m.Campus] = true
		}
		for _, campus := range SortedCampuses() {
			if !have[campus] {
				out = append(out, mapView{
					Campus:      string(campus),
					CampusLabel: campus.Label(),
					Hint:        "未登记（上传底图后自动创建）",
				})
			}
		}
		c.JSON(http.StatusOK, gin.H{"data": out})
	}
}

// mapUploadHandler 上传底图（规格 §7.3）。
func mapUploadHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		campus := c.PostForm("campus")
		if campus == "" {
			campus = c.Query("campus")
		}
		fh, err := c.FormFile("file")
		if err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed,
				"缺少文件字段 file"))
			return
		}
		// 早于读文件就挡掉超大请求：MaxBytesReader 让 c.Request.Body 有上限，
		// 避免先读完 1 GB 再判断超限。
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxMapBytes)

		f, err := fh.Open()
		if err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "无法读取上传文件"))
			return
		}
		defer f.Close()

		// 多读一个字节，用来判断「刚好超限」。
		body, err := io.ReadAll(io.LimitReader(f, MaxMapBytes+1))
		if err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "读取上传内容失败"))
			return
		}
		if len(body) > MaxMapBytes {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed,
				"底图超过 %d MiB 上限", MaxMapBytes>>20))
			return
		}

		m, err := svc.UploadMap(c.Request.Context(), MapUploadInput{
			Campus:   campus,
			FileName: fh.Filename,
			Body:     body,
		})
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toMapView(m)})
	}
}

type calibrationRequest struct {
	AR   float64 `json:"ar"`
	AI   float64 `json:"ai"`
	TX0  float64 `json:"tx0"`
	TY0  float64 `json:"ty0"`
	LNG0 float64 `json:"lng0"`
	LAT0 float64 `json:"lat0"`
	KX   float64 `json:"kx"`
	KY   float64 `json:"ky"`
	Note string  `json:"note"`
}

// mapCalibrationHandler 写入换算参数（规格 §6.1）。
//
// 参数只能来自人工标定（控制点拟合），服务端不生成、不猜值。
// 这个端点存在的意义：让「标定」这件事有明确入口与留痕，
// 而不是让人直接改数据库（改了也没人知道是哪天、用哪批控制点）。
func mapCalibrationHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req calibrationRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "请求体非法"))
			return
		}
		m, err := svc.SetMapCalibration(c.Request.Context(), c.Param("campus"), Calibration{
			AR: req.AR, AI: req.AI, TX0: req.TX0, TY0: req.TY0,
			LNG0: req.LNG0, LAT0: req.LAT0, KX: req.KX, KY: req.KY,
		}, req.Note)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toMapView(m)})
	}
}
