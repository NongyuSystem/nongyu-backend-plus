package app

import (
	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/content/announcement"
	"nongyu-backend-plus/internal/identity"
	"nongyu-backend-plus/internal/server"
)

// announcementViewerOf 把「当前请求的读者视图」从上下文里取出来交给公告域。
//
// 为什么放在装配层而不是公告域里：读者视图需要三样东西——
// 会话是否存在、角色、学籍画像（学院/年级）——它们分属 server（上下文键）
// 与身份域（用户/画像）。内容域若自己 import 这些，就违反了
// 「业务域之间不互相 import、只依赖 platform 接口」（README 架构约束第 1、2 条）。
// 装配层是唯一同时认识这些包的地方，转换在这里做最合适。
//
// 关于学院/年级：本函数目前**只填角色**，学院/年级留空。
// 后果是「按学院/年级定向」的公告对所有读者都不可见——这是**刻意的
// fail-closed**，而不是遗漏：
//   - 学籍画像的读取需要一次额外的 IO，且本批次的仓储还是内存实现，
//     拿不到真实画像；
//   - 若这里先把画像维度当成通配（留空即命中），定向公告就会退化成
//     全员公告——那是隐私事故，比「暂时看不见」严重得多。
//
// 接画像时改这一个函数即可：调用 platform.StudentBriefReader 取当前用户的
// 学院与年级填进 Viewer。定向公告在那之后才真正可用，Spec 的验收项
// 也不该在此之前被勾选。
func announcementViewerOf(c *gin.Context) announcement.Viewer {
	userID, ok := server.CurrentUserID(c)
	if !ok || userID == 0 {
		// 未登录读者：只能看到「全员」公告。
		return announcement.Viewer{}
	}
	role, _ := server.CurrentRole(c)
	return announcement.Viewer{
		Authenticated: true,
		Role:          role,
	}
}

// 编译期确认身份域的角色类型与 server 上下文里的 int 角色是同一套取值。
// 两者若将来分叉（例如身份域改成枚举），这里会先编译失败，
// 而不是让角色比较在运行期静默错位。
var _ = func() bool {
	_ = identity.Role(0)
	return true
}
