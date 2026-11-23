package classroom

import (
	"time"
)

// Building 是一个教学楼栋（不是具体教室）。
//
// 例：`10-B` 代表「十教 B 栋」，`1` 代表「一教」。
type Building struct {
	ID int64
	// Key 是归一化后的楼栋键（`10-B` / `1`），查询用它精确匹配。
	Key string
	// No 是楼栋号（`10` / `1`），Zone 是分区字母（`B` / 空）。
	No   string
	Zone string
	// DisplayName 是展示名（管理员填，如「十教 B 栋」）。
	DisplayName string
	// Aliases 是别名，便于搜索（如「十教B」「十教 B 栋」）。
	Aliases []string
	Campus  string
	// Lat/Lng 为 nil 表示坐标未录入（此时前端只能显示地址文字）。
	Lat *float64
	Lng *float64
	// AddressText 是地址的文字描述。
	AddressText string
	Note        string

	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time

	// Routes 只在「详情」场景填充（列表不带，避免 N+1）。
	Routes []*Route
}

// HasGeo 判断是否已录入坐标。
func (b *Building) HasGeo() bool { return b != nil && b.Lat != nil && b.Lng != nil }

// Route 是一条「进入路线」。
//
// 用**点状经纬度 + 每点说明**表达：管理员手工录少量关键点
// （门口、拐角、电梯），前端连成线并逐点显示说明。
type Route struct {
	ID         int64
	BuildingID int64
	Title      string
	// Description 是整体说明（电梯在哪、要不要刷卡）。
	Description string
	// StartLabel 是起点标签（「南门」「地铁站」），便于按起点筛选。
	StartLabel  string
	Recommended bool
	SortOrder   int
	Enabled     bool

	// Points 按 Seq 升序。
	Points []RoutePoint

	CreatedAt time.Time
	UpdatedAt time.Time
}

// RoutePoint 是路线上的一点。
type RoutePoint struct {
	Seq int
	Lat float64
	Lng float64
	// Label 是该点的说明（「进门后左转到电梯」）。
	Label string
}

// LookupResult 是「按教室名查楼栋」的结果。
//
// 刻意包含 RoomNo 与 BuildingKey：前端要能显示
// 「10-B213 → 十教B 栋（房间 B213）」，把原始输入和归并结果都呈现出来，
// 用户才知道自己是被归并到哪栋。
type LookupResult struct {
	// Input 是用户传入的原始教室名。
	Input string
	// BuildingKey 是归一化后的楼栋键（为空表示无法识别）。
	BuildingKey string
	// RoomNo 是房间号（`B213`），仅用于展示。
	RoomNo string
	// Building 为 nil 表示「识别出了键但没有对应的楼栋数据」
	// （管理员还没录入这栋楼）——这与「无法识别」是两种不同情况，
	// 前端应提示「该楼栋暂未录入」而不是「教室名无效」。
	Building *Building
}
