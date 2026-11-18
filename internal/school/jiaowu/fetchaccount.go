package jiaowu

import "context"

// FetchAccount 是教务公共页专用取数账号，只有两套凭据，没有任何业务数据。
//
// 与「学生账号」的区别：学生账号对应 app 用户、有学号/画像/会话；
// 取数账号只存在于管理员维护的池里，仅用于抓取全站共有、不需登录的教务页。
// 两套凭据都不允许出现在日志、审计或给客户端的响应里。
type FetchAccount struct {
	ID           int64
	Label        string
	TGT          string // go-admin-tgt 值
	CookieHeader string // 整套 auth 域 cookie（含 laravel_session 等），换票/续期必需
	VPNTicket    string // wengine_vpn_ticket
	Enabled      bool
}

// FetchAccountPool 给公共页取数提供一套可用凭据。
//
// 实现由装配层注入（PG 或内存），jiaowu 子域只认这个接口。
// Pick 返回一套凭据；没有可用账号时返回 ErrFetchAccountUnavailable。
// 凭据用完后调用方若发现失效，调 MarkFailure 让池临时换一个。
type FetchAccountPool interface {
	Pick(ctx context.Context) (FetchAccount, error)
	MarkFailure(ctx context.Context, id int64) error
}

// ErrFetchAccountUnavailable 表示池里没有可用取数账号。
// 公共页此时不能回退到「用学生自己的凭据」——那会串数据。
var ErrFetchAccountUnavailable = errFetchAccountUnavailable{}

type errFetchAccountUnavailable struct{}

func (errFetchAccountUnavailable) Error() string {
	return "jiaowu: 没有可用的公共取数账号，请联系管理员配置"
}

// IsPublicDataType 判断该 dataType 是否「全站共有、用取数账号池」。
//
// 分界线：
//   - 公共（全站共有、不随学生变化）：通知/教学通知/竞赛/开课查询/
//     教室信息/教师信息/教师课表/教室课表。
//   - 私有（按学生不同，用学生自己的 tgt+网关票）：课表/成绩/考试/
//     学分进度/排名/培养方案/个人档案。
//
// 公共页缓存写全局键 <prefix>cache:public:<dataType>，所有学生共享；
// 私有页缓存写 <prefix>cache:<userID>:<dataType>。
var publicDataTypes = map[DataType]bool{
	DataTypeNotice:          true,
	DataTypeTeachingNotice:  true,
	DataTypeCompetition:     true,
	DataTypeKaike:           true,
	DataTypeClassroomInfo:   true,
	DataTypeTeacherInfo:     true,
	DataTypeTeacherCourse:   true,
	DataTypeClassroomCourse: true,
}

func IsPublicDataType(dt DataType) bool { return publicDataTypes[dt] }
