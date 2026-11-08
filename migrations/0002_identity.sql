-- 0002_identity.sql
-- 身份域：用户 / 画像 / 登录方式 / 会话。
--
-- 数据边界见 docs/adr/0002-身份仓数据边界.md：
--   - 密码镜像与身份证号加密存储，用两把独立密钥；
--   - 所有密文字段一律以 bytea 存放，绝不明文；
--   - 本表不含任何令牌原文。

-- ── 用户：一人一行，主键自增，学号唯一 ──────────────────────────────
CREATE TABLE IF NOT EXISTS users (
    id          bigserial   PRIMARY KEY,
    student_no  char(9)     NOT NULL UNIQUE,
    -- 0 普通用户 / 1 管理员 / 2 超级管理员（沿用旧农屿三档）
    role        smallint    NOT NULL DEFAULT 0 CHECK (role IN (0, 1, 2)),
    -- 1 正常 / 0 禁用
    status      smallint    NOT NULL DEFAULT 1 CHECK (status IN (0, 1)),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- ── 画像：与用户 1:1；学校档案字段 + 农屿扩展字段 ─────────────────
CREATE TABLE IF NOT EXISTS user_profiles (
    user_id         bigint      PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,

    -- 来自统一身份认证
    name            text        NOT NULL DEFAULT '',
    -- 身份证号：加密存放（ADR-0002 决定落库且允许接口返回），此处只存密文
    id_card_sealed  bytea,
    -- 由服务端从身份证号派生，供登录方式④校验（不存明文身份证号）
    id_card_suffix4 char(4),

    -- 来自教务 bjiben.asp（统一身份认证不下发这些）
    college         text        NOT NULL DEFAULT '',
    major           text        NOT NULL DEFAULT '',
    class_name      text        NOT NULL DEFAULT '',
    grade           text        NOT NULL DEFAULT '',

    -- 农屿扩展：只允许本人修改
    qq              text        NOT NULL DEFAULT '',
    avatar_url      text        NOT NULL DEFAULT '',

    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- ── 登录方式：一个用户可绑多条；每种方式的标识唯一 ────────────────
CREATE TABLE IF NOT EXISTS login_methods (
    id          bigserial   PRIMARY KEY,
    user_id     bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- school / password / mailcode / idcard（将来 oauth:<provider>）
    kind        text        NOT NULL,
    -- 反查用：学号、邮箱、OAuth 的 openid 等
    identifier  text        NOT NULL DEFAULT '',
    -- 凭据密文（农屿账密的密码镜像）；为空表示该方式尚未设置凭据
    secret_ref  bytea,
    bound_at    timestamptz NOT NULL DEFAULT now(),

    -- 同一 kind 下同一 identifier 只能绑一个用户
    UNIQUE (kind, identifier),
    -- 同一用户同一 kind 只允许一条（避免重复绑定）
    UNIQUE (user_id, kind)
);

CREATE INDEX IF NOT EXISTS idx_login_methods_user ON login_methods (user_id);

-- ── 会话：Postgres 是权威，Redis 只是热路径副本 ────────────────────
CREATE TABLE IF NOT EXISTS sessions (
    id          text        PRIMARY KEY,
    user_id     bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- app / admin 两套，互不影响
    scope       text        NOT NULL CHECK (scope IN ('app', 'admin')),
    -- App 侧单设备：换设备即顶替旧会话
    device_id   text        NOT NULL DEFAULT '',
    expires_at  timestamptz NOT NULL,
    revoked_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- 查「这个用户当前有哪些有效会话」、踢旧设备
CREATE INDEX IF NOT EXISTS idx_sessions_user_scope ON sessions (user_id, scope);
-- 供清理过期会话
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions (expires_at);
