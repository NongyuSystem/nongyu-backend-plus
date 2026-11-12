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
// 关于与 identity/session.RequireRole 的关系：那个函数是**领域层**的判定，
// 入参是 session.Principal；本中间件工作在 HTTP 层的 gin.Context 上，拿不到
// Principal，而 server 又不能 import identity（会造成框架层反向依赖业务域）。
// 因此这里复现同一套 fail-closed 语义，并把判据写成与 RequireRole 逐条对应：
//
//	RequireRole 的 Role.Valid()  → 这里 platform.Role(role).Valid()
//	RequireRole 的 UserID == 0   → 这里 userID == 0 也拒
//	RequireRole 的 Role < min    → 这里同样
//
// 若将来要真正共用，正确做法是把 Principal 提到 platform 层，
// 而不是让 server 去 import identity/session。
//
// 语义是 **fail-closed**：
//   - 上下文里没有角色（未认证/中间件顺序错）→ 403，绝不按「默认合格」放行；
//   - 角色不合法（不在 0/1/2 闭集内）→ 403；
//   - 用户 ID 缺失或为 0 → 403；
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
		// 角色必须在合法闭集内。非法角色（如越界数字）一律拒绝，
		// 与 RequireRole 的 Role.Valid() 判据一致。
		r := platform.Role(role)
		if !r.Valid() || !minRole.Valid() {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    "forbidden",
				"message": "权限判定失败（角色未知）",
			})
			return
		}
		// 调用者身份必须已知。CtxUserID 缺失或为 0 说明鉴权中间件没跑或跑错，
		// 不能当成「某个普通用户」放过去。
		if uid, ok := CurrentUserID(c); !ok || uid == 0 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    "forbidden",
				"message": "调用者身份未知",
			})
			return
		}
		if r < minRole {
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
