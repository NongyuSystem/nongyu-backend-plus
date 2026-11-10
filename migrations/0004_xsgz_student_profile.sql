-- 0004：学工系统（xsgz）完整学生档案 —— 唯一数据源（issue #29 后续）
--
-- 背景（2026-11-10 实测）：
--   xsgz 的 GET /gateway/zovecenter-saip-xg-app-student/student/detail/<学号>
--   一次请求返回 13 个分组，覆盖用户清单里几乎所有字段（含身份证号、
--   家庭住址、考生号、紧急联系人、教育经历、家庭成员、经济情况）。
--   因此把它定为**唯一数据源**，弃用 jiaowu 的 GBK 页面解析与 ywtb 的
--   getUserInfo 取数（见代码里的 disable 标记）。
--
-- 设计取舍（重要）：
--   每个分组有 20~50 个字段，且学校随时可能加字段。若全部建成列，
--   迁移会极长且上游一加字段就漏。因此采用混合方案：
--     a) 用户明确要的字段 → student_profiles 的正规列（便于查询/约束）
--     b) 13 个分组的**完整原文** → student_profile_groups.payload (jsonb)
--        —— 一个字段都不丢，上游新增字段自动保留
--     c) 两个列表分组（家庭成员 / 教育经历）→ 独立子表，便于按行查询
--
-- 幂等：全部 IF NOT EXISTS / ADD COLUMN IF NOT EXISTS，可重复执行。

-- ── a) 正规列：用户清单里的字段 ──────────────────────────────────
CREATE TABLE IF NOT EXISTS student_profiles (
    student_no          char(9)     PRIMARY KEY,
    -- 来自 baseDto
    one_card_no         text        NOT NULL DEFAULT '',
    student_name        text        NOT NULL DEFAULT '',
    sex                 text        NOT NULL DEFAULT '',
    sex_name            text        NOT NULL DEFAULT '',
    nationality         text        NOT NULL DEFAULT '',   -- 民族
    nationality_name    text        NOT NULL DEFAULT '',
    -- 身份证号：加密存放（ADR-0002），明文绝不落库
    id_card_sealed      bytea,
    id_card_suffix4     char(4),
    birthday            text        NOT NULL DEFAULT '',
    photo               text        NOT NULL DEFAULT '',
    -- 来自 studentStatusDto
    college_no          text        NOT NULL DEFAULT '',
    college_name        text        NOT NULL DEFAULT '',  -- 学院
    specialty_no        text        NOT NULL DEFAULT '',
    specialty_name      text        NOT NULL DEFAULT '',  -- 专业
    grade               text        NOT NULL DEFAULT '',
    class_no            text        NOT NULL DEFAULT '',
    class_name          text        NOT NULL DEFAULT '',  -- 班级
    campus_name         text        NOT NULL DEFAULT '',
    dorm_address        text        NOT NULL DEFAULT '',  -- 宿舍地址
    study_years         text        NOT NULL DEFAULT '',  -- 学制
    in_school_date      text        NOT NULL DEFAULT '',  -- 入学时间
    student_state_name  text        NOT NULL DEFAULT '',
    current_state_name  text        NOT NULL DEFAULT '',
    instructors         jsonb       NOT NULL DEFAULT '[]'::jsonb, -- 班主任/辅导员
    -- 来自 examDto
    examinee_no         text        NOT NULL DEFAULT '',  -- 考生号
    high_school         text        NOT NULL DEFAULT '',
    source_location     text        NOT NULL DEFAULT '',  -- 生源所在地
    -- 来自 contactInfoDto
    email               text        NOT NULL DEFAULT '',
    mobile_phone        text        NOT NULL DEFAULT '',
    wechat              text        NOT NULL DEFAULT '',
    qq                  text        NOT NULL DEFAULT '',
    -- 来自 emergencyContactInfoDto
    emergency_contact   text        NOT NULL DEFAULT '',
    emergency_relationship text     NOT NULL DEFAULT '',
    emergency_telephone text        NOT NULL DEFAULT '',
    emergency_address   text        NOT NULL DEFAULT '',
    -- 来自 familyDto
    family_address      text        NOT NULL DEFAULT '',  -- 家庭住址
    family_detail_address text      NOT NULL DEFAULT '',
    family_telephone    text        NOT NULL DEFAULT '',
    family_postal_no    text        NOT NULL DEFAULT '',
    -- 来自 personalInfoDto
    name_pinyin         text        NOT NULL DEFAULT '',
    blood_type_name     text        NOT NULL DEFAULT '',
    height              text        NOT NULL DEFAULT '',
    weight              text        NOT NULL DEFAULT '',
    native_place        text        NOT NULL DEFAULT '',
    -- 农屿扩展字段（本人可改，不被学校数据覆盖）
    nickname            text        NOT NULL DEFAULT '',
    avatar_url          text        NOT NULL DEFAULT '',
    --
    age                 smallint,
    synced_at           timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- ── b) 13 个分组的完整原文（jsonb，不丢任何字段）────────────────
CREATE TABLE IF NOT EXISTS student_profile_groups (
    student_no  char(9)     NOT NULL REFERENCES student_profiles (student_no) ON DELETE CASCADE,
    group_key   text        NOT NULL,   -- baseDto / examDto / familyDto / ...
    payload     jsonb       NOT NULL,
    synced_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (student_no, group_key)
);

-- ── c) 列表型分组独立成表 ────────────────────────────────────────
CREATE TABLE IF NOT EXISTS student_family_members (
    student_no      char(9)     NOT NULL REFERENCES student_profiles (student_no) ON DELETE CASCADE,
    ordinal         smallint    NOT NULL,
    uuid            text        NOT NULL DEFAULT '',
    name            text        NOT NULL DEFAULT '',
    relationship    text        NOT NULL DEFAULT '',
    relationship_name text      NOT NULL DEFAULT '',
    unit            text        NOT NULL DEFAULT '',
    occupation      text        NOT NULL DEFAULT '',
    contact_number  text        NOT NULL DEFAULT '',
    is_guarder      text        NOT NULL DEFAULT '',
    payload         jsonb       NOT NULL,   -- 完整原文
    PRIMARY KEY (student_no, ordinal)
);

CREATE TABLE IF NOT EXISTS student_resumes (
    student_no  char(9)     NOT NULL REFERENCES student_profiles (student_no) ON DELETE CASCADE,
    ordinal     smallint    NOT NULL,
    uuid        text        NOT NULL DEFAULT '',
    begin_date  text        NOT NULL DEFAULT '',
    end_date    text        NOT NULL DEFAULT '',
    study_unit  text        NOT NULL DEFAULT '',
    position    text        NOT NULL DEFAULT '',
    award       text        NOT NULL DEFAULT '',
    payload     jsonb       NOT NULL,   -- 完整原文
    PRIMARY KEY (student_no, ordinal)
);
