package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/server"
)

// AdminOnly 的关键性质是 fail-closed：缺角色、角色不足都必须拒，
// 且「缺角色」不能与「role=0」混为一谈（后者是合法普通用户）。
func TestAdminOnly_FailClosed(t *testing.T) {
	cases := []struct {
		name    string
		role    *int // nil 表示上下文里根本没有角色
		userID  string
		minRole platform.Role
		want    int
	}{
		{"无角色一律拒绝", nil, "7", platform.RoleAdmin, http.StatusForbidden},
		{"无角色即使门槛为 0 也拒绝", nil, "7", platform.RoleUser, http.StatusForbidden},
		{"role=0 进不了 role>=1 的端点", intPtr(0), "7", platform.RoleAdmin, http.StatusForbidden},
		{"role=1 恰好达到门槛", intPtr(1), "7", platform.RoleAdmin, http.StatusOK},
		{"role=1 进不了 role>=2 的端点", intPtr(1), "7", platform.RoleSuperUser, http.StatusForbidden},
		{"role=2 达到 role>=2 门槛", intPtr(2), "7", platform.RoleSuperUser, http.StatusOK},

		// 身份缺失必须拒：CtxUserID 空或 "0" 说明鉴权中间件没跑或跑错，
		// 不能当成「某个普通用户」放过去（与 RequireRole 的 UserID==0 判据一致）。
		{"缺用户 ID 即使角色够也拒", intPtr(2), "", platform.RoleAdmin, http.StatusForbidden},
		{"用户 ID 为 0 即使角色够也拒", intPtr(2), "0", platform.RoleAdmin, http.StatusForbidden},

		// 非法角色（越界）必须拒，不能因为「大于门槛」就放过。
		{"非法角色拒绝", intPtr(99), "7", platform.RoleAdmin, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.GET("/admin/x", func(c *gin.Context) {
				if tc.userID != "" {
					c.Set(server.CtxUserID, tc.userID)
				}
				if tc.role != nil {
					c.Set(server.CtxRole, *tc.role)
				}
				// 用中间件链而不是在 handler 里判，模拟真实装配顺序。
			}, server.AdminOnly(tc.minRole), func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"ok": true})
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/admin/x", nil)
			r.ServeHTTP(w, req)

			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func intPtr(v int) *int { return &v }
