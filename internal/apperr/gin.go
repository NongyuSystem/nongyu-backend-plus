package apperr

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// ErrorResponse 是全仓统一的错误响应体。
//
// 只在**错误**时出现；成功响应不带 code 字段（与既有端点一致）。
// 字段名 camelCase，与 Spec 的信封约定一致。
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteGin 把错误写进 gin 响应：领域错误按码映射状态，其它一律 500。
//
// 为什么把这个放进 apperr 而不是各域各写一份：此前 announcement / message /
// jiaowu / presence 各有一份 writeError，其中「码 → 状态」的判据已经开始分叉
// （jiaowu 用 school.Code、presence 直接接 status）。判据收敛到一处后，
// 新增端点不可能再写出「同一个码映射成不同状态」的漂移。
//
// 非领域错误一律 500 且不回显内部细节——绝不把 DB 故障包成 4xx 骗客户端。
func WriteGin(c *gin.Context, err error) {
	code, ok := CodeOf(err)
	if !ok {
		// **真实原因必须进日志**：响应体给客户端通用的「服务内部错误」
		// （不泄露实现细节），但服务端自己一定要能定位。
		// 早先这里直接丢弃 err，导致 5xx 在日志里查不到任何线索——
		// 线上排查时只能靠猜。
		slog.Error("未归类的内部错误",
			"err", err,
			"method", c.Request.Method,
			"path", c.FullPath(),
			"requestId", c.GetHeader("X-Request-Id"),
		)
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Code:    "internal",
			Message: "服务内部错误",
		})
		return
	}
	c.JSON(HTTPStatus(code), ErrorResponse{Code: string(code), Message: err.Error()})
}
