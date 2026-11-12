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
		minRole platform.Role
		want    int
	}{
		{"无角色一律拒绝", nil, platform.RoleAdmin, http.StatusForbidden},
		{"无角色即使门槛为 0 也拒绝", nil, platform.RoleUser, http.StatusForbidden},
		{"role=0 进不了 role>=1 的端点", intPtr(0), platform.RoleAdmin, http.StatusForbidden},
		{"role=1 恰好达到门槛", intPtr(1), platform.RoleAdmin, http.StatusOK},
		{"role=1 进不了 role>=2 的端点", intPtr(1), platform.RoleSuperUser, http.StatusForbidden},
		{"role=2 达到 role>=2 门槛", intPtr(2), platform.RoleSuperUser, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.GET("/admin/x", func(c *gin.Context) {
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
