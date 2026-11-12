-- 0006_message_track.sql：站内消息域与用户行为埋点域的表。
--
-- 依据 docs/specs/spec-六类常见功能.md 的「站内消息」「用户行为埋点」节，
-- 域模型依据 docs/tech/决策-消息域模型.md 与 docs/tech/决策-用户行为埋点.md。
--
-- 为什么单独一个迁移文件而不是并进 0005：
--   0005 是内容域（公告 + 帖子），这两张是另外两个域；分文件让「某个域回滚」
--   时不必动另一个域的表。迁移只增不改（已发布的迁移文件不得再编辑）。
--
-- 命名与类型口径沿用 0005：snake_case、timestamptz、
-- 软删用 deleted_at NULL、状态闭集用 CHECK 而不是枚举类型。

BEGIN;

-- ---------- 站内消息（通知 + 内部信合一的 per-user 投递行） ----------

CREATE TABLE IF NOT EXISTS messages (
    id                 BIGSERIAL PRIMARY KEY,
    recipient_id       BIGINT      NOT NULL,
    -- 形态闭集：通知（服务端派生） / 内部信（用户互发）。
    kind               TEXT        NOT NULL
                       CHECK (kind IN ('notification', 'internal_mail')),

    -- 幂等键三元（不含 recipient 与 kind：它们已在上面的唯一索引里，
    -- 这里只存「从哪来」。内部信这两列为空串。
    source_domain      TEXT        NOT NULL DEFAULT '',
    source_object_id   TEXT        NOT NULL DEFAULT '',

    -- 触发方写入时的**快照**（决策-消息域模型.md D1）：
    -- 读路径不按 source 实时渲染，来源被软删后消息仍可读、无跨域 join。
    title              TEXT        NOT NULL DEFAULT '',
    body               TEXT        NOT NULL DEFAULT '',

    -- 合并次数：同幂等键重复触发时累加，不产生第二行。
    hits               INTEGER     NOT NULL DEFAULT 1,

    -- 内部信的发送者（实名可见）；通知恒为 NULL。
    sender_id          BIGINT,

    read_at            TIMESTAMPTZ,
    deleted_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 站内消息必须能回答「这个收件人的未读数」与「他的消息列表」，
    -- 两条查询都以 recipient 起头，故加复合索引而不是单列索引。
    CONSTRAINT messages_notification_needs_source CHECK (
        kind <> 'notification'
        OR (source_domain <> '' AND source_object_id <> '')
    )
);

CREATE INDEX IF NOT EXISTS idx_messages_recipient_created
    ON messages (recipient_id, created_at DESC, id DESC);

-- 未读数查询：只扫未读未删的行。用部分索引而不是全表，
-- 因为绝大多数消息最终都会变成已读，全表索引会持续膨胀。
CREATE INDEX IF NOT EXISTS idx_messages_unread
    ON messages (recipient_id)
    WHERE read_at IS NULL AND deleted_at IS NULL;

-- **通知的部分唯一索引**（Spec 硬约束 / 决策-消息域模型.md Q3）：
-- 同 (recipient, 域, 对象) 的通知只能有一行，重复触发靠 upsert 累加 hits。
--
-- 只对 kind='notification' 建：内部信没有来源，source 两列都是空串，
-- 若一并纳入唯一索引，某人收到的所有内部信会互相冲突（只能剩一条）。
CREATE UNIQUE INDEX IF NOT EXISTS uq_messages_notification_source
    ON messages (recipient_id, kind, source_domain, source_object_id)
    WHERE kind = 'notification';

-- ---------- 用户行为埋点 ----------

CREATE TABLE IF NOT EXISTS track_event (
    id                 BIGSERIAL PRIMARY KEY,

    -- 幂等键：客户端生成，重复上报不重复落库。
    event_id           TEXT        NOT NULL,

    -- 事件类型闭集（7 个）。llm_proxy_fail 仅服务端写，
    -- 但表结构不区分来源——那条约束在 handler 层（客户端上报直接拒）。
    event_type         TEXT        NOT NULL
                       CHECK (event_type IN (
                           'screen_view', 'button_click', 'perf',
                           'app_open', 'heartbeat', 'crash', 'llm_proxy_fail'
                       )),
    event_name         TEXT        NOT NULL DEFAULT '',

    -- 客户端时钟（毫秒）与服务端接收时刻分开存：
    -- 客户端时钟可被篡改/有偏差，分析与对账要用 received_at。
    client_ts_ms       BIGINT      NOT NULL DEFAULT 0,

    session_id         TEXT        NOT NULL DEFAULT '',
    app_version        TEXT        NOT NULL DEFAULT '',
    platform           TEXT        NOT NULL
                       CHECK (platform IN ('ios', 'android', 'web')),

    -- props 是脱敏后的自由 JSON（顶层白名单 + 敏感键剔除 + 4KB 截断之后）。
    -- 用 jsonb 而不是 text：将来要按维度聚合时不必再转换。
    props              JSONB,

    request_id         TEXT        NOT NULL DEFAULT '',
    -- **服务端注入**，忽略客户端自报（否则任何人可伪造他人行为）。
    user_id            BIGINT      NOT NULL,

    received_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 提升幂等性到存储层：即使服务端去重逻辑有 bug，重复 event_id 也进不来。
CREATE UNIQUE INDEX IF NOT EXISTS uq_track_event_event_id
    ON track_event (event_id);

-- summary 端点只做「按类型 + 时间窗计数」，故以 received_at 起头。
CREATE INDEX IF NOT EXISTS idx_track_event_received_type
    ON track_event (received_at DESC, event_type);

-- 按用户查行为轨迹（当前不暴露端点，但排障时要用）。
CREATE INDEX IF NOT EXISTS idx_track_event_user_received
    ON track_event (user_id, received_at DESC);

COMMIT;
