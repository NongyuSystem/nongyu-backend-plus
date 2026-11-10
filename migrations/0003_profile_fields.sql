-- 0003：扩充用户画像字段 + 登录审计三元组（issue #29）
--
-- 来源分两类（实测 2026-11-10，见 docs/tech/实测-川农一次登录覆盖多站点的唯一解.md）：
--   A. 教务档案页 bjiben.asp（一张 go-admin-tgt 换票后即可取）
--   B. 客户端请求头（最后登录 IP / 设备 / 时间）
--   C. ywtb getUserInfo（身份证、生日；它需要 ywtb 自己的 Bearer）
--
-- 全部用 IF NOT EXISTS / ADD COLUMN IF NOT EXISTS，保证重复执行不报错。

-- ── A. 教务档案页 bjiben.asp 的字段 ──────────────────────────────
-- 实测表头：考生号 | 学号 | 姓名 | 性别 | 系别 | 专业 | 学制 | 培养层次 |
--          年级 | 班级 | 新专业 | 新班级 | 学籍状态 | 入学日期 | 民族 |
--          政治面貌 | 家庭通讯地址 | 火车站点 | 个人电话 | 家庭电话 | 特长
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS exam_no           text NOT NULL DEFAULT '';
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS gender            text NOT NULL DEFAULT '';
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS study_duration    text NOT NULL DEFAULT '';
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS education_level   text NOT NULL DEFAULT '';
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS new_major         text NOT NULL DEFAULT '';
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS new_class_name    text NOT NULL DEFAULT '';
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS enrollment_status text NOT NULL DEFAULT '';
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS enrollment_date   text NOT NULL DEFAULT '';
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS nation            text NOT NULL DEFAULT '';
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS political_status  text NOT NULL DEFAULT '';
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS home_address      text NOT NULL DEFAULT '';
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS train_station     text NOT NULL DEFAULT '';
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS personal_phone    text NOT NULL DEFAULT '';
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS home_phone        text NOT NULL DEFAULT '';
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS specialty         text NOT NULL DEFAULT '';

-- ── 农屿扩展字段（本人可改）─────────────────────────────────────
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS nickname          text NOT NULL DEFAULT '';
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS email             text NOT NULL DEFAULT '';

-- ── C. ywtb 才有的字段 ─────────────────────────────────────────
-- 生日：用于推算年龄（身份证本身仍加密存放在 id_card_sealed）。
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS birthday          text NOT NULL DEFAULT '';
-- ywtb 头像地址。
ALTER TABLE user_profiles ADD COLUMN IF NOT EXISTS avatar_source_url text NOT NULL DEFAULT '';

-- ── B. 登录审计三元组（客户端请求头 + 登录时刻）─────────────────
-- 存在 users 表而不是 profile：它们是「账号」属性，不是「画像」。
-- 用 text 而非 inet：客户端 IP 可能带端口或为空，inet 会拒收非法值导致登录失败。
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login_ip     text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login_device text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login_at     timestamptz;

-- 审核说明（写入仓库 docs，此处留注释）：
--   身份证号**不新增列**：已有 id_card_sealed（密文）字段，唯一来源是 ywtb getUserInfo。
--   年龄**不新增列**：由 birthday 派生即可，避免冗余与不一致。
