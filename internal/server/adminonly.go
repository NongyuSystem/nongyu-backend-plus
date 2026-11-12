package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
)

// AdminOnly 是管理端端点的角色门槛中间件。
//
// 它只解决「**角色够不够**」这一件事；「是不是 admin 会话」由装配层把管理端
// 路由组挂在 RevocableAuthMiddleware(…, ScopeAdmin) 上来保证。两件事分开，
// 是因为它们来源不同：scope 是会话属性，role 是用户属性（以 DB 为准）。
//
// 为什么要有它（而不是在各 handler 里写 c.GetInt(CtxRole) >= 1）：
// 角色判定散写必然漏一处，而漏掉的那一处就是提权入口。集中成一个中间件后，
// 新增管理端点时不可能「忘记判角色」——不挂中间件反而显眼
// （见 map #43 子票 #46 Q2 的决议）。
//
// 参数类型用 platform.Role 而不是 int：仓库里角色只有一套取值定义
// （platform.RoleAdmin 等），门槛与用户实体的角色必须是同一类型，
// 否则每次调用都要转换，转换处就是取值错位的入口。
//
// 语义是 **fail-closed**：
//   - 上下文里没有角色（未认证/中间件顺序错）→ 403，绝不按「默认合格」放行；
//   - 角色低于门槛 → 403；
//   - 只有 role 严格 >= minRole 才放行。
func AdminOnly(minRole platform.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, ok := currentRole(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    "forbidden",
				"message": "需要管理端身份",
			})
			return
		}
		if platform.Role(role) < minRole {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    "forbidden",
				"message": "权限不足",
			})
			return
		}
		c.Next()
	}
}

// currentRole 读取中间件写入上下文的角色。
//
// 单独包一层是为了让「没有角色」与「角色为 0」可区分：CtxRole 缺失时
// gin 的 GetInt 会返回 0，而 0 是合法的普通用户角色 —— 若不区分，
// 中间件顺序装错时普通用户会被当成 role=0 放行门槛为 0 的端点。
func currentRole(c *gin.Context) (int, bool) {
	v, exists := c.Get(CtxRole)
	if !exists {
		return 0, false
	}
	role, ok := v.(int)
	if !ok {
		return 0, false
	}
	return role, true
}
