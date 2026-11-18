package model

import "time"

// TicketRef 是「按用户存一段不透明字节」的过期候选引用。
// 只带定位与排序所需的最小信息，不暴露 sealed 内容。
// 下沉到 model 让 redis 子包无需反向依赖 platform 根。
type TicketRef struct {
	UserID    string
	ExpiresAt time.Time
}
