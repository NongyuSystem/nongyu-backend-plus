-- 0005_content.sql：内容域（公告 + 帖子）的首批表。
--
-- 依据 docs/specs/spec-六类常见功能.md 的「数据契约」节（第①层「阻塞开工」，
-- 字段名与取值已定死）。域模型依据 docs/tech/决策-公告域模型与作用域.md
-- 与 docs/tech/决策-帖子域模型与契约.md。
--
-- 命名口径：表名用 snake_case；Go 侧字段名是 camelCase，映射在 pgstore 实现里。
-- 时间一律 timestamptz（不带时区的时间在跨时区部署时会静默错位）。
-- 软删统一用 deleted_at timestamptz NULL，不做物理删除（审计可追溯）。

BEGIN;

-- ---------- 公告 ----------

CREATE TABLE IF NOT EXISTS announcements (
    id                 BIGSERIAL PRIMARY KEY,
    title              TEXT        NOT NULL,
    body               TEXT        NOT NULL,
    -- 状态闭集：草稿 / 已发布 / 已撤回。用 CHECK 而不是枚举类型，
    -- 是因为枚举类型加值需要 ALTER TYPE，迁移成本更高。
    status             TEXT        NOT NULL DEFAULT 'draft'
                       CHECK (status IN ('draft', 'published', 'revoked')),
    published_at       TIMESTAMPTZ,
    effective_from     TIMESTAMPTZ,
    expires_at         TIMESTAMPTZ,
    pinned             BOOLEAN     NOT NULL DEFAULT FALSE,
    -- 每次「发布/重发」自增；与 id 一起构成通知幂等键。
    publish_revision   INTEGER     NOT NULL DEFAULT 0,

    -- 受众三件：范围 + 该范围下使用的条件。
    audience_scope     TEXT        NOT NULL DEFAULT 'all'
                       CHECK (audience_scope IN ('all', 'role', 'college_grade')),
    -- 按角色定向时的角色集合。用 INTEGER[] 而不是逗号串：
    -- 查询与约束都更直接，且不会出现 "1,2" 与 "2,1" 两种等价表示。
    audience_roles     INTEGER[]   NOT NULL DEFAULT '{}',
    audience_college   TEXT        NOT NULL DEFAULT '',
    audience_grade     TEXT        NOT NULL DEFAULT '',

    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at         TIMESTAMPTZ,

    -- 自洽性约束与 Go 侧 validateAudience 对齐（双保险：代码能绕过，
    -- 数据库这一层不该能绕过）。
    CONSTRAINT announcements_audience_role_needs_roles
        CHECK (audience_scope <> 'role' OR cardinality(audience_roles) > 0),
    CONSTRAINT announcements_audience_college_grade_needs_a_dimension
        CHECK (audience_scope <> 'college_grade'
               OR audience_college <> '' OR audience_grade <> ''),
    CONSTRAINT announcements_audience_all_has_no_conditions
        CHECK (audience_scope <> 'all'
               OR (cardinality(audience_roles) = 0
                   AND audience_college = '' AND audience_grade = '')),
    CONSTRAINT announcements_expiry_after_effective
        CHECK (effective_from IS NULL OR expires_at IS NULL
               OR expires_at > effective_from),
    -- 已发布的公告必须有发布时间；否则列表排序键会是 NULL。
    CONSTRAINT announcements_published_needs_published_at
        CHECK (status <> 'published' OR published_at IS NOT NULL)
);

-- 读者侧与 latest 的主查询：已发布、未删、按置顶 + 发布时间排序。
-- 部分索引（只覆盖未删且已发布的行）比全表索引小得多，正好命中热路径。
CREATE INDEX IF NOT EXISTS idx_announcements_published_order
    ON announcements (pinned DESC, published_at DESC, id DESC)
    WHERE deleted_at IS NULL AND status = 'published';

-- 管理端列表：含草稿与已撤回，同样的排序键。
CREATE INDEX IF NOT EXISTS idx_announcements_admin_order
    ON announcements (pinned DESC, published_at DESC NULLS LAST, id DESC)
    WHERE deleted_at IS NULL;

-- ---------- 帖子 ----------

CREATE TABLE IF NOT EXISTS posts (
    id           BIGSERIAL PRIMARY KEY,
    -- 板块闭集：feedback（反馈）/ courtyard（留言）。
    -- 刻意不使用「广场」一词（见 GLOSSARY.md 的 _Avoid_）。
    board        TEXT        NOT NULL CHECK (board IN ('feedback', 'courtyard')),
    title        TEXT        NOT NULL,
    body         TEXT        NOT NULL,
    -- 附件只存对象 key，不存 URL：URL 是预签名、短命、按需生成的，
    -- 落库会在 TTL 之后全部失效（Spec「已知边界」第 2 条）。
    attachments  TEXT[]      NOT NULL DEFAULT '{}',
    author_id    BIGINT      NOT NULL,
    published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    edited_at    TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at   TIMESTAMPTZ
);

-- 列表主查询：按板块过滤 + 发布时间倒序 + 游标翻页。
CREATE INDEX IF NOT EXISTS idx_posts_board_published
    ON posts (board, published_at DESC, id DESC)
    WHERE deleted_at IS NULL;

-- 管理端与「我的帖子」按作者取。
CREATE INDEX IF NOT EXISTS idx_posts_author
    ON posts (author_id, published_at DESC)
    WHERE deleted_at IS NULL;

-- ---------- 帖子留言（两层语义：帖子 ↔ 留言，无嵌套） ----------

CREATE TABLE IF NOT EXISTS post_replies (
    id           BIGSERIAL PRIMARY KEY,
    post_id      BIGINT      NOT NULL REFERENCES posts (id),
    body         TEXT        NOT NULL,
    author_id    BIGINT      NOT NULL,
    published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at   TIMESTAMPTZ
    -- 刻意没有 parent_reply_id：本 effort 明确不做嵌套回复
    -- （map #43 子票 #49 Q11）。加这一列就等于开了嵌套的口子。
);

CREATE INDEX IF NOT EXISTS idx_post_replies_post
    ON post_replies (post_id, published_at, id)
    WHERE deleted_at IS NULL;

-- ---------- 点赞 / 收藏（靶子 = post | comment） ----------

CREATE TABLE IF NOT EXISTS post_reactions (
    id          BIGSERIAL PRIMARY KEY,
    -- 靶子类型闭集。comment 指 post_replies 的一行。
    target_type TEXT   NOT NULL CHECK (target_type IN ('post', 'comment')),
    target_id   BIGINT NOT NULL,
    -- 反应类型：like（点赞）/ favorite（收藏）。
    reaction    TEXT   NOT NULL CHECK (reaction IN ('like', 'favorite')),
    user_id     BIGINT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 同一用户对同一靶子的同一反应只能有一条：取消即删行。
-- 用唯一索引而不是「先查再插」，这样并发双击也不会产生两行。
CREATE UNIQUE INDEX IF NOT EXISTS uq_post_reactions_target_user
    ON post_reactions (target_type, target_id, reaction, user_id);

-- 反向查询（「我点赞过哪些」本期不做，但管理端统计会用到按靶子聚合）。
CREATE INDEX IF NOT EXISTS idx_post_reactions_target
    ON post_reactions (target_type, target_id, reaction);

COMMIT;
