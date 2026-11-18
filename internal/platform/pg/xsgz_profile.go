package pg

// 学工系统（xsgz）完整档案的 Postgres 落库实现：把一份学生档案一次事务写进 4 张表。
//
// TODO(结构整改·P0-b)：本文件是**学工域业务逻辑放在最底层包**的现存遗留——
// 按分层它应由 school/xsgz 提供落库能力，platform 只该有「连接与事务」原语。
// 本次结构整改先按原文件名独立成文件，让错位一眼可见；真正迁移到 school/xsgz
// 会带来跨包改动（调用点在装配层），放在后续 commit 单独完成。
// 见 _artifacts/structure-lead-diagnosis.md §3。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ── 学工系统（xsgz）完整档案落库 ──────────────────────────────────
//
// 唯一数据源：xsgz detail 端点。客户端完成统一登录后把 main-app-access-token
// 交给后端，后端取数并落到这里。jiaowu / ywtb 的取数逻辑已 disable。
//
// 落库策略（对应迁移 0004）：
//   - student_profiles：用户清单里的常用字段，正规列
//   - student_profile_groups：13 个分组的完整原文（jsonb），不丢任何字段
//   - student_family_members / student_resumes：列表分组独立成表
//
// 全部在一个事务里完成；重复同步按 student_no 覆盖。

// XSGZProfileInput 是落库所需的输入（由 schoolauth.XSGZStudentDetail 转换而来）。
//
// 刻意不直接依赖 schoolauth 包（platform 是最底层，不能反向依赖业务域）：
// 调用方负责把 detail 的 Groups / Typed 拆成这个结构。
type XSGZProfileInput struct {
	StudentNo string
	// 常用字段（对应 student_profiles 的列）
	Fields map[string]string
	// 分组原文：key = 分组名，value = 该分组的完整 JSON
	Groups map[string]string
	// 列表分组原文：每个元素是一条完整 JSON
	FamilyMembers []string
	Resumes       []string
	// 身份证密文与派生后四位（由上层加密后传入，本层不解释）
	IDCardSealed  []byte
	IDCardSuffix4 string
	Age           int
}

// SaveXSGZProfile 把学工档案完整落库（事务内 4 张表一起写）。
//
// 幂等：按 student_no 覆盖；分组与列表分组采用「先删后插」，保证与本次同步
// 结果完全一致，不会残留上游已删除的家庭成员/简历。
func (s *PGIdentityStore) SaveXSGZProfile(ctx context.Context, in XSGZProfileInput) error {
	if strings.TrimSpace(in.StudentNo) == "" {
		return fmt.Errorf("platform: SaveXSGZProfile 缺少学号")
	}
	f := func(k string) string { return in.Fields[k] }

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// ① 主表（常用字段）
	_, err = tx.Exec(ctx, `
		INSERT INTO student_profiles (
			student_no, one_card_no, student_name, sex, sex_name, nationality, nationality_name,
			id_card_sealed, id_card_suffix4, birthday, photo,
			college_no, college_name, specialty_no, specialty_name, grade, class_no, class_name,
			campus_name, dorm_address, study_years, in_school_date, student_state_name, current_state_name,
			examinee_no, high_school, source_location,
			email, mobile_phone, wechat, qq,
			emergency_contact, emergency_relationship, emergency_telephone, emergency_address,
			family_address, family_detail_address, family_telephone, family_postal_no,
			name_pinyin, blood_type_name, height, weight, native_place,
			nickname, avatar_url, age, synced_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,
			$8,$9,$10,$11,
			$12,$13,$14,$15,$16,$17,$18,
			$19,$20,$21,$22,$23,$24,
			$25,$26,$27,
			$28,$29,$30,$31,
			$32,$33,$34,$35,
			$36,$37,$38,$39,
			$40,$41,$42,$43,$44,
			$45,$46,$47, now(), now()
		)
		ON CONFLICT (student_no) DO UPDATE SET
			one_card_no=EXCLUDED.one_card_no, student_name=EXCLUDED.student_name,
			sex=EXCLUDED.sex, sex_name=EXCLUDED.sex_name,
			nationality=EXCLUDED.nationality, nationality_name=EXCLUDED.nationality_name,
			id_card_sealed=EXCLUDED.id_card_sealed, id_card_suffix4=EXCLUDED.id_card_suffix4,
			birthday=EXCLUDED.birthday, photo=EXCLUDED.photo,
			college_no=EXCLUDED.college_no, college_name=EXCLUDED.college_name,
			specialty_no=EXCLUDED.specialty_no, specialty_name=EXCLUDED.specialty_name,
			grade=EXCLUDED.grade, class_no=EXCLUDED.class_no, class_name=EXCLUDED.class_name,
			campus_name=EXCLUDED.campus_name, dorm_address=EXCLUDED.dorm_address,
			study_years=EXCLUDED.study_years, in_school_date=EXCLUDED.in_school_date,
			student_state_name=EXCLUDED.student_state_name, current_state_name=EXCLUDED.current_state_name,
			examinee_no=EXCLUDED.examinee_no, high_school=EXCLUDED.high_school,
			source_location=EXCLUDED.source_location,
			email=EXCLUDED.email, mobile_phone=EXCLUDED.mobile_phone,
			wechat=EXCLUDED.wechat, qq=EXCLUDED.qq,
			emergency_contact=EXCLUDED.emergency_contact,
			emergency_relationship=EXCLUDED.emergency_relationship,
			emergency_telephone=EXCLUDED.emergency_telephone,
			emergency_address=EXCLUDED.emergency_address,
			family_address=EXCLUDED.family_address,
			family_detail_address=EXCLUDED.family_detail_address,
			family_telephone=EXCLUDED.family_telephone, family_postal_no=EXCLUDED.family_postal_no,
			name_pinyin=EXCLUDED.name_pinyin, blood_type_name=EXCLUDED.blood_type_name,
			height=EXCLUDED.height, weight=EXCLUDED.weight, native_place=EXCLUDED.native_place,
			-- 农屿扩展字段：保留本人已填值，不被学校数据覆盖
			nickname=student_profiles.nickname, avatar_url=student_profiles.avatar_url,
			age=EXCLUDED.age, synced_at=now(), updated_at=now()`,
		in.StudentNo,
		f("oneCardNo"), f("studentName"), f("sex"), f("sexName"), f("nationality"), f("nationalityName"),
		in.IDCardSealed, in.IDCardSuffix4, f("birthday"), f("photo"),
		f("collegeNo"), f("collegeName"), f("specialtyNo"), f("specialtyName"), f("grade"), f("classNo"), f("className"),
		f("campusName"), f("dormAddress"), f("studyYears"), f("inSchoolDate"), f("studentStateName"), f("currentStateName"),
		f("examineeNo"), f("highSchool"), f("sourceLocation"),
		f("email"), f("mobilePhone"), f("wechat"), f("qq"),
		f("emergencyContact"), f("emergencyContactRelationship"), f("emergencyContactTelephone"), f("emergencyContactAddress"),
		f("familyAddress"), f("familyDetailAddress"), f("familyTelephone"), f("familyPostalNo"),
		f("namePinyin"), f("bloodTypeName"), f("height"), f("weight"), f("nativePlace"),
		f("nickname"), f("avatarUrl"), in.Age)
	if err != nil {
		return fmt.Errorf("写 student_profiles 失败：%w", err)
	}

	// ② 13 个分组的完整原文（jsonb）
	_, err = tx.Exec(ctx, `DELETE FROM student_profile_groups WHERE student_no = $1`, in.StudentNo)
	if err != nil {
		return err
	}
	for key, payload := range in.Groups {
		if payload == "" {
			payload = "{}"
		}
		if _, err = tx.Exec(ctx,
			`INSERT INTO student_profile_groups (student_no, group_key, payload) VALUES ($1,$2,$3::jsonb)`,
			in.StudentNo, key, payload); err != nil {
			return fmt.Errorf("写分组 %s 失败：%w", key, err)
		}
	}

	// ③ 家庭成员列表
	if _, err = tx.Exec(ctx, `DELETE FROM student_family_members WHERE student_no = $1`, in.StudentNo); err != nil {
		return err
	}
	for i, raw := range in.FamilyMembers {
		if _, err = tx.Exec(ctx, `
			INSERT INTO student_family_members
				(student_no, ordinal, uuid, name, relationship, relationship_name, unit, occupation, contact_number, is_guarder, payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb)`,
			in.StudentNo, i,
			JSONStringField(raw, "uuid"), JSONStringField(raw, "name"),
			JSONStringField(raw, "relationship"), JSONStringField(raw, "relationshipName"),
			JSONStringField(raw, "unit"), JSONStringField(raw, "occupation"),
			JSONStringField(raw, "contactNumber"), JSONStringField(raw, "isGuarder"),
			orEmptyObject(raw)); err != nil {
			return fmt.Errorf("写家庭成员 #%d 失败：%w", i, err)
		}
	}

	// ④ 教育经历列表
	if _, err = tx.Exec(ctx, `DELETE FROM student_resumes WHERE student_no = $1`, in.StudentNo); err != nil {
		return err
	}
	for i, raw := range in.Resumes {
		if _, err = tx.Exec(ctx, `
			INSERT INTO student_resumes
				(student_no, ordinal, uuid, begin_date, end_date, study_unit, position, award, payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)`,
			in.StudentNo, i,
			JSONStringField(raw, "uuid"), JSONStringField(raw, "beginDate"), JSONStringField(raw, "endDate"),
			JSONStringField(raw, "studyUnit"), JSONStringField(raw, "position"), JSONStringField(raw, "award"),
			orEmptyObject(raw)); err != nil {
			return fmt.Errorf("写教育经历 #%d 失败：%w", i, err)
		}
	}

	return tx.Commit(ctx)
}

// JSONStringField 从一条 JSON 对象里安全取字符串字段（number/bool 也能取字面量）。
func JSONStringField(raw, key string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &m) != nil {
		return ""
	}
	v, ok := m[key]
	if !ok || len(v) == 0 || string(v) == "null" {
		return ""
	}
	if v[0] == '"' {
		var s string
		if json.Unmarshal(v, &s) == nil {
			return s
		}
	}
	return string(v)
}

func orEmptyObject(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	return raw
}

// BuildXSGZProfileInput 把学工 detail 的「分组原文 + 常用字段」转成落库输入。
//
// 放在 platform 层、参数是纯数据结构，避免 platform 反向依赖 schoolauth。
func BuildXSGZProfileInput(
	studentNo string,
	groups map[string]json.RawMessage,
	typed XSGZTypedLite,
	familyMembers, resumes []json.RawMessage,
	idCardSealed []byte, idCardSuffix4 string,
) XSGZProfileInput {
	fields := map[string]string{
		"oneCardNo": typed.OneCardNo, "studentName": typed.StudentName,
		"sex": typed.Sex, "sexName": typed.SexName,
		"nationality": typed.Nationality, "nationalityName": typed.NationalityName,
		"birthday": typed.Birthday, "photo": typed.Photo,
		"collegeNo": typed.CollegeNo, "collegeName": typed.CollegeName,
		"specialtyNo": typed.SpecialtyNo, "specialtyName": typed.SpecialtyName,
		"grade": typed.Grade, "classNo": typed.ClassNo, "className": typed.ClassName,
		"campusName": typed.CampusName, "dormAddress": typed.DormAddress,
		"studyYears": typed.StudyYears, "inSchoolDate": typed.InSchoolDate,
		"studentStateName": typed.StudentStateName, "currentStateName": typed.CurrentStateName,
		"examineeNo": typed.ExamineeNo, "highSchool": typed.HighSchool,
		"sourceLocation": typed.SourceLocation,
		"email":          typed.Email, "mobilePhone": typed.MobilePhone,
		"wechat": typed.Wechat, "qq": typed.QQ,
		"emergencyContact":             typed.EmergencyContact,
		"emergencyContactRelationship": typed.EmergencyRelationship,
		"emergencyContactTelephone":    typed.EmergencyTelephone,
		"emergencyContactAddress":      typed.EmergencyAddress,
		"familyAddress":                typed.FamilyAddress,
		"familyDetailAddress":          typed.FamilyDetailAddress,
		"familyTelephone":              typed.FamilyTelephone, "familyPostalNo": typed.FamilyPostalNo,
		"namePinyin": typed.NamePinyin, "bloodTypeName": typed.BloodTypeName,
		"height": typed.Height, "weight": typed.Weight, "nativePlace": typed.NativePlace,
		"nickname": typed.Nickname, "avatarUrl": typed.AvatarURL,
	}
	g := make(map[string]string, len(groups))
	for k, v := range groups {
		g[k] = string(v)
	}
	fm := make([]string, 0, len(familyMembers))
	for _, v := range familyMembers {
		fm = append(fm, string(v))
	}
	rs := make([]string, 0, len(resumes))
	for _, v := range resumes {
		rs = append(rs, string(v))
	}
	return XSGZProfileInput{
		StudentNo: studentNo, Fields: fields, Groups: g,
		FamilyMembers: fm, Resumes: rs,
		IDCardSealed: idCardSealed, IDCardSuffix4: idCardSuffix4, Age: typed.Age,
	}
}

// XSGZTypedLite 是 schoolauth.XSGZTyped 的镜像（platform 层不依赖业务域）。
type XSGZTypedLite struct {
	OneCardNo, StudentName, Sex, SexName                                          string
	Nationality, NationalityName, Birthday, Photo                                 string
	CollegeNo, CollegeName, SpecialtyNo, SpecialtyName                            string
	Grade, ClassNo, ClassName, CampusName, DormAddress                            string
	StudyYears, InSchoolDate, StudentStateName, CurrentStateName                  string
	ExamineeNo, HighSchool, SourceLocation                                        string
	Email, MobilePhone, Wechat, QQ                                                string
	EmergencyContact, EmergencyRelationship, EmergencyTelephone, EmergencyAddress string
	FamilyAddress, FamilyDetailAddress, FamilyTelephone, FamilyPostalNo           string
	NamePinyin, BloodTypeName, Height, Weight, NativePlace                        string
	Nickname, AvatarURL                                                           string
	Age                                                                           int
}
