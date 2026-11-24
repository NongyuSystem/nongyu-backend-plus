-- 0012_app_releases.sql：应用发布域（Spec #80）。
--
-- 一张表装「应用的一个版本」：
--   app_releases —— 已发布安装包的元数据（包体在对象存储里，不在库里）
--
-- 三个刻意的取舍，改表前先读 Spec #80 的 Implementation Decisions：
--   1. **没有激活指针表**：本域「最新」= version_code 最大者，用 ORDER BY
--      直接算出来。不引入第二个真值来源，也就不存在「指针指向不存在的版本」
--      这类悬空状态。
--   2. **没有 min_sdk 之外的 APK 内容列**：服务端不解析安装包，
--      不存包内解析出来的任何东西（那是本域明确不做的）。
--   3. **没有下载 URL 列**：下载地址由服务端按 object_key 流式代理生成，
--      库里存 URL 会让「存储位置变了但库里还是旧地址」成为一类故障。
--
-- 命名口径与 0005/0007 一致：snake_case 表名、timestamptz 时间、
-- (app_id, version_code) 复合唯一保证「版本不可变」。

BEGIN;

CREATE TABLE IF NOT EXISTS app_releases (
    id                       BIGSERIAL   PRIMARY KEY,
    -- app_id 是 applicationId（Android 包名），即 GLOSSARY 里「应用」的身份。
    app_id                   TEXT        NOT NULL,
    -- version_code 是「最新」判定的唯一尺度：整数、单调。
    version_code             BIGINT      NOT NULL CHECK (version_code > 0),
    -- version_name 仅供展示（如 1.4.2），不参与任何比较。
    version_name             TEXT        NOT NULL,
    -- min_sdk 可选：安装包要求的最低 Android API，0 表示未填。
    min_sdk                  INTEGER     NOT NULL DEFAULT 0,
    -- 支持版本下限：低于它客户端必须阻断使用。
    min_supported_version_code BIGINT    NOT NULL DEFAULT 0
                             CHECK (min_supported_version_code >= 0),
    changelog                TEXT        NOT NULL DEFAULT '',
    -- 对象存储里的 key（releases/{appId}/{versionCode}/{随机id}.apk）。
    -- **不下发给客户端**：暴露 key 等于暴露可直接抓取的对象地址。
    object_key               TEXT        NOT NULL,
    -- 整包 sha256（小写 hex），下载时经响应头回给客户端做完整性自证。
    sha256                   TEXT        NOT NULL,
    size                     BIGINT      NOT NULL CHECK (size >= 0),
    created_by               TEXT        NOT NULL DEFAULT '',
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 版本不可变：同一应用同一版本码只能登记一次。
    -- 这是「同版本号永远对应同一套字节」的**唯一**实现手段（见 Contract 的说明）。
    CONSTRAINT app_releases_ref_unique UNIQUE (app_id, version_code)
);

-- 「取最新」的支撑索引：按应用过滤 + 版本码倒序取第一条。
-- 与唯一约束 (app_id, version_code) 的方向相反，故单独建。
CREATE INDEX IF NOT EXISTS app_releases_latest_idx
    ON app_releases (app_id, version_code DESC);

COMMIT;
