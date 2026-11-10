// Package user 是身份域的「用户与画像」子域。
//
// 职责边界：
//   - 用户实体：学号、角色、状态
//   - 画像：学校档案字段（只允许服务端同步写入）+ 农屿扩展字段（只允许本人修改）
//   - 对外提供窄读接口 StudentBriefReader，供其它业务域读最小字段集
//
// 不负责：登录、会话、令牌。那些在 login / session 子域。
package user

import (
	"context"

	"nongyu-backend-plus/internal/identity"
	"nongyu-backend-plus/internal/platform"
)

// Service 编排用户与画像的读写。
//
// 字段分权是这里最重要的规则（见 ADR-0002 第 4 条）：
//   - SyncSchoolProfile 是**唯一**能写学校档案字段的入口，只允许登录子域在学校认证成功后调用；
//   - UpdateExtras 是**唯一**能写农屿扩展字段的入口，只允许本人调用；
//   - 客户端提交的画像字段永远不能走 SyncSchoolProfile。
type Service struct {
	users    platform.UserRepo
	profiles platform.ProfileRepo
}

// New 构造用户子域服务。
func New(users platform.UserRepo, profiles platform.ProfileRepo) *Service {
	return &Service{users: users, profiles: profiles}
}

// SchoolProfile 是「学校认证后要同步下来的档案」。
// 刻意与 platform.Profile 分开：这里只含有权由学校写入的字段，
// 防止调用方顺手把 QQ、头像等扩展字段一起覆盖掉。
type SchoolProfile struct {
	Name      string
	College   string
	Major     string
	ClassName string
	Grade     string
	// IDCardSealed 是身份证号密文；由登录子域加密后传入，本层不解释内容。
	IDCardSealed []byte
	// IDCardSuffix4 是从身份证号派生的后四位（明文，供登录方式④校验）。
	IDCardSuffix4 string

	// ── 以下来自教务档案页 bjiben.asp（issue #29）──────────────────
	ExamNo           string // 考生号
	Gender           string // 性别
	StudyDuration    string // 学制
	EducationLevel   string // 培养层次
	NewMajor         string // 新专业
	NewClassName     string // 新班级
	EnrollmentStatus string // 学籍状态
	EnrollmentDate   string // 入学日期
	Nation           string // 民族
	PoliticalStatus  string // 政治面貌
	HomeAddress      string // 家庭通讯地址
	TrainStation     string // 火车站点
	PersonalPhone    string // 个人电话
	HomePhone        string // 家庭电话
	Specialty        string // 特长

	// ── 以下来自 ywtb getUserInfo（需它自己的 Bearer）──────────────
	Birthday        string // 生日（推算年龄用）
	AvatarSourceURL string // ywtb 头像地址
}

// Extras 是农屿自建、只允许本人修改的扩展字段。
type Extras struct {
	QQ        string
	AvatarURL string
}

// Ensure 按学号取得用户；不存在则创建（无感注册的落点）。
// 返回用户与该次是否新建。
func (s *Service) Ensure(ctx context.Context, studentNo string, role identity.Role) (platform.User, bool, error) {
	u, err := s.users.ByStudentNo(ctx, studentNo)
	if err == nil {
		return u, false, nil
	}
	if !platform.IsNotFound(err) {
		return platform.User{}, false, identity.WrapError(identity.CodeInternal, "查询用户失败", err)
	}
	created, cerr := s.users.Create(ctx, studentNo, role)
	if cerr != nil {
		// 并发下可能被别人抢先创建：再查一次。
		if platform.IsAlreadyExists(cerr) {
			if again, gerr := s.users.ByStudentNo(ctx, studentNo); gerr == nil {
				return again, false, nil
			}
		}
		return platform.User{}, false, identity.WrapError(identity.CodeInternal, "创建用户失败", cerr)
	}
	return created, true, nil
}

// ByStudentNo 按学号查用户。
func (s *Service) ByStudentNo(ctx context.Context, studentNo string) (platform.User, error) {
	u, err := s.users.ByStudentNo(ctx, studentNo)
	if platform.IsNotFound(err) {
		return platform.User{}, identity.NewError(identity.CodeUnauthorized, "用户不存在")
	}
	if err != nil {
		return platform.User{}, identity.WrapError(identity.CodeInternal, "查询用户失败", err)
	}
	return u, nil
}

// Profile 读画像。
func (s *Service) Profile(ctx context.Context, userID int64) (platform.Profile, error) {
	p, err := s.profiles.ByUserID(ctx, userID)
	if platform.IsNotFound(err) {
		return platform.Profile{}, identity.NewError(identity.CodeUnauthorized, "画像不存在")
	}
	if err != nil {
		return platform.Profile{}, identity.WrapError(identity.CodeInternal, "读取画像失败", err)
	}
	return p, nil
}

// SyncSchoolProfile 写入学校档案字段。
//
// **只允许登录子域在学校认证成功后调用**；它保留既有的扩展字段（QQ、头像），
// 绝不因为同步学校数据而清空用户自己填的内容。
func (s *Service) SyncSchoolProfile(ctx context.Context, userID int64, sp SchoolProfile) (platform.Profile, error) {
	existing, err := s.profiles.ByUserID(ctx, userID)
	if err != nil && !platform.IsNotFound(err) {
		return platform.Profile{}, identity.WrapError(identity.CodeInternal, "读取画像失败", err)
	}
	// 保留扩展字段：这是「按字段分权」的落点。
	p := platform.Profile{
		UserID:        userID,
		QQ:            existing.QQ,
		AvatarURL:     existing.AvatarURL,
		Name:          sp.Name,
		College:       sp.College,
		Major:         sp.Major,
		ClassName:     sp.ClassName,
		Grade:         sp.Grade,
		IDCardSealed:  sp.IDCardSealed,
		IDCardSuffix4: sp.IDCardSuffix4,

		// 教务档案页字段（#29）
		ExamNo:           sp.ExamNo,
		Gender:           sp.Gender,
		StudyDuration:    sp.StudyDuration,
		EducationLevel:   sp.EducationLevel,
		NewMajor:         sp.NewMajor,
		NewClassName:     sp.NewClassName,
		EnrollmentStatus: sp.EnrollmentStatus,
		EnrollmentDate:   sp.EnrollmentDate,
		Nation:           sp.Nation,
		PoliticalStatus:  sp.PoliticalStatus,
		HomeAddress:      sp.HomeAddress,
		TrainStation:     sp.TrainStation,
		PersonalPhone:    sp.PersonalPhone,
		HomePhone:        sp.HomePhone,
		Specialty:        sp.Specialty,

		// ywtb 字段（#29）
		Birthday:        sp.Birthday,
		AvatarSourceURL: sp.AvatarSourceURL,

		// 农屿扩展字段：同样保留本人已填的值，不被学校数据覆盖。
		Nickname: existing.Nickname,
		Email:    existing.Email,
	}
	if err := s.profiles.Upsert(ctx, p); err != nil {
		return platform.Profile{}, identity.WrapError(identity.CodeInternal, "写入画像失败", err)
	}
	return p, nil
}

// UpdateExtras 更新农屿扩展字段（只允许本人调用；学校档案字段不受影响）。
func (s *Service) UpdateExtras(ctx context.Context, userID int64, ex Extras) (platform.Profile, error) {
	existing, err := s.profiles.ByUserID(ctx, userID)
	if platform.IsNotFound(err) {
		// 画像尚未建立（用户还没同步过学校档案）：此时不允许只写扩展字段，
		// 否则会造出一条「学校字段全空」的画像。
		return platform.Profile{}, identity.NewError(identity.CodeUnauthorized, "画像尚未建立，请先完成学校认证登录")
	}
	if err != nil {
		return platform.Profile{}, identity.WrapError(identity.CodeInternal, "读取画像失败", err)
	}
	existing.QQ = ex.QQ
	existing.AvatarURL = ex.AvatarURL
	if err := s.profiles.Upsert(ctx, existing); err != nil {
		return platform.Profile{}, identity.WrapError(identity.CodeInternal, "写入画像失败", err)
	}
	return existing, nil
}

// Brief 实现 platform.StudentBriefReader：只回最小字段集，供其它业务域读。
func (s *Service) Brief(ctx context.Context, studentNo string) (platform.StudentBrief, error) {
	u, err := s.users.ByStudentNo(ctx, studentNo)
	if platform.IsNotFound(err) {
		return platform.StudentBrief{}, platform.ErrNotFound
	}
	if err != nil {
		return platform.StudentBrief{}, err
	}
	p, err := s.profiles.ByUserID(ctx, u.ID)
	if platform.IsNotFound(err) {
		// 画像还没建：仍返回学号，其余为空，而不是整体失败。
		return platform.StudentBrief{StudentNo: u.StudentNo}, nil
	}
	if err != nil {
		return platform.StudentBrief{}, err
	}
	return platform.StudentBrief{
		StudentNo: u.StudentNo,
		Name:      p.Name,
		College:   p.College,
		Major:     p.Major,
		ClassName: p.ClassName,
		Grade:     p.Grade,
	}, nil
}

// 编译期断言：user.Service 必须能被当作 StudentBriefReader 注入。
var _ platform.StudentBriefReader = (*Service)(nil)
