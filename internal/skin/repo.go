package skin

import (
	"context"
	"errors"
)

// ErrVersionExists 表示同一 (packId, version) 已登记（版本不可变）。
var ErrVersionExists = errors.New("skin: 该 packId/version 已存在")

// ErrVersionNotFound 表示按 (packId, version) 找不到包。
var ErrVersionNotFound = errors.New("skin: 该 packId/version 不存在")

// Repo 是皮肤包元数据的持久化接缝。
//
// 业务域只定义接口，实现放 internal/platform/pgstore（PG）与内存两种，
// 由装配层按「配了 DATABASE_URL 就全切 PG」的不变式选择（见 app/repos.go）。
// 接口刻意小：包体字节不在库里（在对象存储），库里只存元数据 + 全局单指针。
type Repo interface {
	// Create 登记一条皮肤包。Pack.ID / CreatedAt 由实现回填。
	// 同一 (PackID, Version) 已存在时返回 ErrVersionExists（不可覆盖）：
	// 版本不可变是客户端「filesDir/skins/{packId}/{version} + 原子 rename +
	// 当前指针」这套落盘语义的前提，重复上传不同内容会让已缓存旧包的设备
	// 与新设备看到同一版本号却是两套字节。
	Create(ctx context.Context, p *Pack) error

	// GetByRef 按 (packId, version) 取一条；不存在返回 ErrVersionNotFound。
	GetByRef(ctx context.Context, packID, version string) (*Pack, error)

	// List 列出已登记的包（按创建时间倒序，新的在前），最多 limit 条。
	List(ctx context.Context, limit int) ([]*Pack, error)

	// Current 返回全局单指针指向的包；没有任何已激活包时返回 ErrNoCurrentPointer。
	Current(ctx context.Context) (*Current, error)

	// Activate 把全局单指针原子地切到 (packId, version)。
	// 该记录必须已存在，否则返回 ErrVersionNotFound。
	Activate(ctx context.Context, packID, version string) error
}
