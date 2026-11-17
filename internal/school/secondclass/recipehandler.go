// Package secondclass 的写类配方端点：GET /school/secondclass/recipe/:recipeId。
//
// 形状照 RegisterCache（规格 §6.1）：由 app 装配层挂到 gin 引擎上，认证由全局
// 中间件链统一负责（会话 + NY1 签名），业务 handler 不自行鉴权、不新增任何豁免、
// 端点不进 publicPaths / publicReadablePaths。身份只从 school.CtxUserID 上下文取。
//
// 与读类 cache_miss 捎带配方的差别：写操作（报名/签到）不经过 GET，没有「未命中」
// 这个时机，必须独立入口；响应直接是完整 Recipe 对象（规格 §6.2「配方（写类）」），
// 不包一层信封、成功不带 code。
package secondclass

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RegisterRecipe 挂载写类配方下发端点。由 app 装配层在引擎就绪后调用；
// 全局签名+会话链已由引擎统一施加，本函数不注册任何豁免或 publicPath。
func RegisterRecipe(r gin.IRouter, svc *Service) {
	r.GET("/school/secondclass/recipe/:recipeId", recipeGetHandler(svc))
}

// recipeGetHandler 按稳定 id 下发一条写类配方。
//
// 纯静态数据：不依赖缓存层是否可用（未配 Redis 时仍可用，规格 §4.6 / §8.2），
// 也不做任何出站。下发本身记审计 secondclass.recipe_issue（规格 §6.11 / 决策 §四）。
func recipeGetHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 身份只从中间件写入的上下文取，永不读 query/body 的 userID（规格 §3.3）。
		userID, ok := requireUserID(c)
		if !ok {
			return
		}

		recipeID := c.Param("recipeId")
		recipe, ok := RecipeForID(recipeID)
		if !ok {
			// 未知 id 回 400（不是 404、不是 200 空）；错误码取规格 §3.2 表 A 的
			// target_denied——「请求的逻辑目标不在白名单」，与 cachehandler.go 的
			// 受控词表口径一致。绝不把用户输入拼进别的键或 URL。
			c.JSON(http.StatusBadRequest, errorResponse{
				Code:    "target_denied",
				Message: "不支持的配方 id",
			})
			return
		}

		// 配方是编译期静态数据，不含任何 token/GPS/扫码时间戳具体值；审计只记
		// 「谁问了哪个动作的配方」这六字段事实，不记配方体（规格 §3.4 / §3.6）。
		svc.audit(c.Request.Context(), userID, "secondclass.recipe_issue", "ok", recipeID)
		c.JSON(http.StatusOK, recipe)
	}
}
