package track

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/apperr"
)

// UserIDFunc 从请求上下文取当前会话用户（由装配层注入）。
type UserIDFunc func(c *gin.Context) (int64, bool)

// Register 挂载埋点写入口（需会话 + NY1，由装配层负责）。
func Register(r gin.IRouter, svc *Service, userIDOf UserIDFunc) {
	if userIDOf == nil {
		userIDOf = func(*gin.Context) (int64, bool) { return 0, false }
	}
	r.POST("/track/events", reportHandler(svc, userIDOf))
}

// RegisterAdmin 挂载埋点汇总（admin，由装配层负责权限门槛）。
func RegisterAdmin(r gin.IRouter, svc *Service) {
	r.GET("/track/events/summary", summaryHandler(svc))
}

// reportRequest 是上报入参。
//
// 用裸 map 接每一条而不是强类型结构体：脱敏的第一步就是「顶层白名单」，
// 必须先看到原始键名才能判断哪些该丢。强类型结构体会让未知字段在
// 反序列化阶段就被静默忽略，无法计数 droppedTopLevel。
type reportRequest struct {
	Events []map[string]any `json:"events"`
}

func reportHandler(svc *Service, userIDOf UserIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, ok := userIDOf(c)
		if !ok {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份"))
			return
		}

		// 1MB 硬限：超了在读取阶段就断，不把大 body 读进内存。
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxBodyBytes)

		var in reportRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			if isMaxBytesErr(err) {
				apperr.WriteGin(c, apperr.Errorf(apperr.CodePayloadTooLarge, "请求体超过 1MB"))
				return
			}
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "请求体非法"))
			return
		}

		res, err := svc.Report(c.Request.Context(), uid, requestIDOf(c), in.Events)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		// 注意：**缓冲满不是错误**，正常 200 返回，dropped 单独给出。
		c.JSON(http.StatusOK, res)
	}
}

func summaryHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		from, err := parseTimeQuery(c.Query("from"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		to, err := parseTimeQuery(c.Query("to"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		counts, err := svc.Summary(c.Request.Context(), from, to)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		// 转成 string 键，保证 JSON 输出稳定（EventType 是自定义 string 类型）。
		out := make(map[string]int, len(counts))
		for k, v := range counts {
			out[string(k)] = v
		}
		c.JSON(http.StatusOK, gin.H{"counts": out})
	}
}

// parseTimeQuery 解析 RFC3339 时间参数；空串返回零值（由 service 补默认窗）。
func parseTimeQuery(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, apperr.Errorf(apperr.CodeValidationFailed, "时间参数格式应为 RFC3339")
	}
	return t, nil
}

// requestIDOf 取请求追踪号（既有中间件写入 header 与 gin context）。
func requestIDOf(c *gin.Context) string {
	if v := c.GetString("requestID"); v != "" {
		return v
	}
	return c.GetHeader("X-Request-Id")
}

// isMaxBytesErr 判断是否是 MaxBytesReader 触发的体积超限。
func isMaxBytesErr(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}
