package app

import (
	"context"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/content/announcement"
	"nongyu-backend-plus/internal/platform"
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
// 关于学院/年级的读取链：会话里只有 userID，画像要按**学号**取，
// 所以是两跳 `userID → User.StudentNo → StudentBrief{College, Grade}`。
// 两跳都有缓存（见 profileCache），否则每个带会话的公告请求都要打两次库。
//
// 失败语义（**关键**）：任一跳失败都**留空而不是通配**。
// 若把「取不到画像」当成「命中所有学院/年级」，定向公告就会退化成
// 全员公告——那是隐私事故，比「暂时看不见」严重得多。
func announcementViewerOf(c *gin.Context) announcement.Viewer {
	userID, ok := server.CurrentUserID(c)
	if !ok || userID == 0 {
		// 未登录读者：只能看到「全员」公告。
		return announcement.Viewer{}
	}
	role, _ := server.CurrentRole(c)
	v := announcement.Viewer{
		Authenticated: true,
		Role:          role,
	}

	// 画像可能为 nil（未配身份域时）：此时学院/年级保持空，定向公告不可见。
	if viewerProfileOf != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 800*time.Millisecond)
		defer cancel()
		if brief, ok := viewerProfileOf(ctx, userID); ok {
			v.College = brief.College
			v.Grade = brief.Grade
		}
	}
	return v
}

// announcementUserIDOf 取当前会话用户 id，供公告域记审计用。
//
// 与 announcementViewerOf 分开是有意的：读者视图需要「角色」这个业务事实，
// 审计只需要「谁」。把两者混在一个函数里会让内容域对身份域的依赖变宽。
func announcementUserIDOf(c *gin.Context) (int64, bool) {
	return server.CurrentUserID(c)
}

// profileResolver 是 `userID → 学籍画像` 的解析函数（可为 nil）。
//
// 用包级变量而不是字段传递：它是装配期一次性注入的只读依赖，
// 而 announcementViewerOf 的签名被公告域固定成 `func(*gin.Context) Viewer`
// （不能塞进来）。**只在装配期写一次，之后只读**，因此无需加锁。
var viewerProfileOf func(ctx context.Context, userID int64) (platform.StudentBrief, bool)

// installProfileResolver 由装配层在身份域可用时调用。
func installProfileResolver(r func(ctx context.Context, userID int64) (platform.StudentBrief, bool)) {
	viewerProfileOf = r
}

// newProfileResolver 构造带缓存的解析器。
//
// 为什么要缓存：公告列表是**公开读且高频**的端点，若每个带会话的请求都做
// 两次数据库往返（查 user 拿学号、查画像拿学院年级），读放大很明显。
// 画像变化是低频事件（学生转专业/升年级），缓存 5 分钟足够。
//
// 缓存的是「userID → brief」而不是分开缓存两跳：中间层（学号）没有独立价值，
// 分开缓存只会多一份失效逻辑。
func newProfileResolver(users platform.UserRepo, briefs platform.StudentBriefReader) func(context.Context, int64) (platform.StudentBrief, bool) {
	if users == nil || briefs == nil {
		return nil
	}
	const ttl = 5 * time.Minute
	cache := &profileCache{m: map[int64]profileEntry{}}

	return func(ctx context.Context, userID int64) (platform.StudentBrief, bool) {
		if e, ok := cache.get(userID); ok {
			return e.brief, e.found
		}
		// 第一跳：userID → 学号。查不到（或非学生账号）就记「无画像」。
		u, err := users.ByID(ctx, userID)
		if err != nil || u.StudentNo == "" {
			cache.put(userID, profileEntry{found: false}, ttl)
			return platform.StudentBrief{}, false
		}
		// 第二跳：学号 → 画像。
		b, err := briefs.Brief(ctx, u.StudentNo)
		if err != nil {
			// **失败不缓存**：查不到画像可能是暂时性故障（DB 抖动），
			// 缓存「没有」会让该用户 5 分钟内一直看不到定向公告。
			return platform.StudentBrief{}, false
		}
		cache.put(userID, profileEntry{brief: b, found: true}, ttl)
		return b, true
	}
}

// profileEntry 是一条缓存。
type profileEntry struct {
	brief platform.StudentBrief
	// found=false 表示「这个人没有画像」（如教职工账号），
	// 与「没查到」区分：前者可以缓存，后者不缓存。
	found bool
	at    time.Time
}

// profileCache 是带 TTL 的极简缓存。
//
// 不引第三方 LRU：条目数量级是「在线用户」，且过期即可丢弃，
// 简单 map + TTL 足够；真正的容量上限由 ttl 自然约束。
type profileCache struct {
	mu sync.RWMutex
	m  map[int64]profileEntry
}

func (c *profileCache) get(userID int64) (profileEntry, bool) {
	c.mu.RLock()
	e, ok := c.m[userID]
	c.mu.RUnlock()
	if !ok {
		return profileEntry{}, false
	}
	if time.Since(e.at) > 5*time.Minute {
		return profileEntry{}, false
	}
	return e, true
}

func (c *profileCache) put(userID int64, e profileEntry, _ time.Duration) {
	e.at = time.Now()
	c.mu.Lock()
	c.m[userID] = e
	c.mu.Unlock()
}
