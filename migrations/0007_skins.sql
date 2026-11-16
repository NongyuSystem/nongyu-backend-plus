-- 0007_skins.sql：皮肤包上传/下载服务（issue #53 / ADR-0003、0004、0006）。
--
-- 两类数据落在 Postgres：
--   skin_packs        —— 已登记皮肤包的元数据（包体 zip 在对象存储 MinIO，不在库里）
--   skin_current      —— 全局单指针（ADR-0003：服务端只提供「当前皮肤」，所有用户同一套）
--
-- 命名口径与 0005/0006 一致：snake_case 表名、timestamptz 时间、
-- (pack_id, version) 复合唯一保证「版本不可变」。
--
-- 为什么 version 要 B 端显式给、而不是自增：客户端落盘路径是
-- filesDir/skins/{packId}/{version}/，version 是客户端缓存/回滚/两套并存的
-- 身份的一部分，必须由发布方稳定给出，不能由服务端插入顺序决定。

BEGIN;

CREATE TABLE IF NOT EXISTS skin_packs (
    id              BIGSERIAL PRIMARY KEY,
    pack_id         TEXT        NOT NULL,
    version         TEXT        NOT NULL,
    schema_version  INTEGER     NOT NULL,
    min_app_version INTEGER     NOT NULL,
    display_name    TEXT        NOT NULL,
    object_key      TEXT        NOT NULL,
    pack_sha256     TEXT        NOT NULL,
    pack_size       BIGINT      NOT NULL,
    -- manifest.json 原文（客户端直接 parseJson；服务端不重新序列化，避免改变
    -- 字段顺序/签名所依赖的内容）。用 TEXT 而非 JSONB 正是为了「原样透传」。
    manifest_json   TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by      TEXT        NOT NULL DEFAULT '',

    -- 版本不可变：同一 (pack_id, version) 只能登记一次。
    CONSTRAINT skin_packs_ref_unique UNIQUE (pack_id, version)
);

CREATE INDEX IF NOT EXISTS skin_packs_created_at_idx ON skin_packs (created_at DESC);

-- 全局单指针：整张表只有一行（id=1 固定）。
-- 用一行表而不是「给 skin_packs 加 is_current 布尔列」：布尔列要防并发出现
-- 两行同时为真，得靠部分唯一索引；单行表用 upsert 天然原子，且「当前是哪套」
-- 是一个独立的、会被频繁读的事实，不该每次都扫一遍 skin_packs。
CREATE TABLE IF NOT EXISTS skin_current (
    id          INTEGER     PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    pack_id     TEXT        NOT NULL,
    version     TEXT        NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 指针必须指向一个真实存在的包（避免悬空指针下发不存在的对象 key）。
    CONSTRAINT skin_current_ref_fk
        FOREIGN KEY (pack_id, version)
        REFERENCES skin_packs (pack_id, version)
        ON DELETE RESTRICT
);

COMMIT;
