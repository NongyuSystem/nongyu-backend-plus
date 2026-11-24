package release

import (
	"context"
	"errors"
)

// ErrReleaseExists 表示同一 (applicationId, versionCode) 已登记。
//
// 「版本不可变」：同一应用同一版本码只能登记一次。理由是这份记录会直接
// 决定客户端装到的是哪一份字节——同版本号两套字节会造成「装过的人和新装
// 的人不是同一个包」，是比皮肤包更严重的问题（安装包会被执行）。
var ErrReleaseExists = errors.New("release: 该 applicationId/versionCode 已存在")

// ErrReleaseNotFound 表示按 (applicationId, versionCode) 找不到记录。
var ErrReleaseNotFound = errors.New("release: 该 applicationId/versionCode 不存在")

// ErrNoRelease 表示该应用还没有任何已发布版本。
//
// 与 ErrReleaseNotFound 是两个不同的事实：「尚无版本」是正常态（新应用
// 第一次发布之前），而「某个指定版本不存在」是调用方给了错版本码。
// 服务层据此决定是返回「无版本」形状还是 404。
var ErrNoRelease = errors.New("release: 该应用还没有任何已发布版本")

// Repo 是应用发布元数据的持久化接缝。
//
// 业务域只定义接口，实现放本域的 store/ 子包（内存与 PG 两套），
// 由装配层按「配了 DATABASE_URL 就全切 PG」的既有不变式选择。
// 接口刻意小：包体字节不在库里（在对象存储），库里只存元数据。
type Repo interface {
	// Create 登记一条发布记录。ID / CreatedAt 由实现回填。
	// 同一 (AppID, VersionCode) 已存在时返回 ErrReleaseExists（不可覆盖）。
	Create(ctx context.Context, r *Release) error

	// GetByRef 按 (appId, versionCode) 取一条；不存在返回 ErrReleaseNotFound。
	GetByRef(ctx context.Context, appID string, versionCode int64) (*Release, error)

	// Latest 返回该应用 versionCode 最大的那条；一条都没有时返回 ErrNoRelease。
	//
	// 刻意不提供「激活指针」：本域用「取最大」这一条单调规则决定最新，
	// 不引入第二个真值来源（见 Spec #80 的 Implementation Decisions）。
	Latest(ctx context.Context, appID string) (*Release, error)
}
