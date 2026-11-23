-- 0009_post_type_rule.sql：帖子类型的「结构化字段规则」表。
--
-- 设计意图（2026-11-23 维护者拍板）：
--
--   帖子类型（board）会**由管理员持续新增**——教师评价、课程评价，
--   以后还有别的。若每个类型都在 posts 表上加固定列（teacher_name、
--   course_name、...），每加一个类型就要改表结构 + 改 Go 结构体 + 改校验，
--   三处联动改，且历史迁移不可回改。
--
--   所以改成：
--     1) posts 只加**一个** detail JSONB 字段，容纳任意多个业务字段；
--     2) 本表登记「某类型需要哪些字段、每个字段什么类型/是否必填」，
--        校验时读这张表，**数据驱动**地校验 detail —— 加新类型只加一行，
--        不动表结构、不动代码。
--
-- 与 posts.board 的关系：两者用同一个取值域，都引用 post_types 表，
-- 保证「能发帖的类型」与「有规则的类型」不会各自漂移。

BEGIN;

-- ---------- 类型登记表 ----------
--
-- 为什么要单独一张表而不是直接用 posts.board 的 CHECK 约束：
-- 类型会被管理员新增，CHECK 约束加值需要改迁移（不可回改），
-- 而表可以 INSERT。这是「配置数据 vs 结构定义」的分界。
CREATE TABLE IF NOT EXISTS post_types (
    code        TEXT        PRIMARY KEY,
    -- 展示名，给管理端与客户端用；可为空（客户端可用内置文案）。
    name        TEXT        NOT NULL DEFAULT '',
    -- 是否允许发帖。管理员可停用某类型而不删历史数据。
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    -- 排序权重，小者在前（列表筛选器的展示顺序）。
    sort_order  INTEGER     NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 内置四种类型。ON CONFLICT DO NOTHING 让本迁移可重复执行
-- （与其它迁移的幂等风格一致）。
INSERT INTO post_types (code, name, sort_order) VALUES
    ('feedback',       '反馈',     10),
    ('courtyard',      '院子',     20),
    ('teacher_review', '教师评价', 30),
    ('course_review',  '课程评价', 40)
ON CONFLICT (code) DO NOTHING;

-- ---------- 字段规则表 ----------
--
-- 一行 = 「某类型的 detail 里应有这样一个字段」。
-- 校验时按 (type_code) 取出全部行，逐条校验 detail。
CREATE TABLE IF NOT EXISTS post_type_rule (
    id          BIGSERIAL   PRIMARY KEY,
    -- 属于哪个类型。
    type_code   TEXT        NOT NULL REFERENCES post_types(code) ON DELETE CASCADE,
    -- detail 里的字段名（JSON 的 key）。
    field       TEXT        NOT NULL,
    -- 字段值类型：string / int / number / bool。
    --
    -- 刻意只支持这四种标量：detail 是「帖子的结构化说明」，
    -- 不是任意嵌套文档。嵌套需求出现时再加 array/object，
    -- 而不是一开始就留个万能 JSON。
    value_type  TEXT        NOT NULL
                CHECK (value_type IN ('string', 'int', 'number', 'bool')),
    -- 是否必填。
    required    BOOLEAN     NOT NULL DEFAULT TRUE,
    -- string 类型的长度上下限（其它类型忽略）。
    min_len     INTEGER     NOT NULL DEFAULT 0,
    max_len     INTEGER     NOT NULL DEFAULT 0,   -- 0 = 不限制
    -- number/int 的取值区间（空表示不限制）。
    min_value   NUMERIC,
    max_value   NUMERIC,
    -- 校验失败时给用户看的提示。
    label       TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 同一类型下字段名唯一。
    CONSTRAINT uq_post_type_rule_field UNIQUE (type_code, field)
);

-- 教师评价：教师 + 课程，外加 1-5 分评分。
INSERT INTO post_type_rule
    (type_code, field, value_type, required, min_len, max_len, min_value, max_value, label)
VALUES
    ('teacher_review', 'teacherName', 'string', TRUE, 1, 64, NULL, NULL, '教师姓名'),
    ('teacher_review', 'courseName',  'string', TRUE, 1, 128, NULL, NULL, '课程名称'),
    ('teacher_review', 'rating',      'int',    TRUE, 0, 0, 1, 5, '评分（1-5）')
ON CONFLICT (type_code, field) DO NOTHING;

-- 课程评价：课程 + 授课教师，外加评分。
-- 注意字段顺序与教师评价相反（课程是主语），但字段集合相同——
-- 这正是用规则表而不用固定列的好处：两个类型可以各有不同的必填组合。
INSERT INTO post_type_rule
    (type_code, field, value_type, required, min_len, max_len, min_value, max_value, label)
VALUES
    ('course_review', 'courseName',  'string', TRUE, 1, 128, NULL, NULL, '课程名称'),
    ('course_review', 'teacherName', 'string', FALSE, 1, 64, NULL, NULL, '授课教师'),
    ('course_review', 'rating',      'int',    TRUE, 0, 0, 1, 5, '评分（1-5）')
ON CONFLICT (type_code, field) DO NOTHING;

-- feedback / courtyard 不登记任何规则行 = 这两个类型的 detail 必须为空。
-- 这是刻意的：规则表没有行，就代表「这个类型没有专属字段」。

-- ---------- posts.board 改为引用 post_types（真正的解耦） ----------
--
-- 0005 建表时写死了 `CHECK (board IN ('feedback','courtyard'))`。
-- 若保留 CHECK，管理员新增类型时**仍然要改迁移**才能放宽取值域——
-- 那「数据驱动」就是假的：加一行数据不够，还要发一次版。
--
-- 改成外键之后，新增类型只需往 post_types 插一行，posts 立刻接受新取值。
-- 这同时带来两个好处：
--   * 数据库层面保证「发帖用的类型一定是登记过的」，比 CHECK 更强；
--   * 停用类型只需 UPDATE enabled，不必改约束。
--
-- 对已有数据的安全性：本仓历史数据只有 feedback/courtyard，而这两个值
-- 上一步已写入 post_types，所以加外键不会失败。若将来在有脏数据的库上执行，
-- 会在 ADD CONSTRAINT 这一步报错——那正是我们想要的（宁可迁移失败，
-- 也不要悄悄留下「帖子引用了不存在的类型」）。
ALTER TABLE posts DROP CONSTRAINT IF EXISTS posts_board_check;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'posts_board_fkey'
    ) THEN
        ALTER TABLE posts
            ADD CONSTRAINT posts_board_fkey
            FOREIGN KEY (board) REFERENCES post_types(code);
    END IF;
END $$;

-- ---------- posts 加 detail 字段 ----------
--
-- 用 JSONB 而不是 JSON：要能按下标/键查询（如「所有 4 分以上的课程评价」）。
-- 默认 '{}' 而不是 NULL：让「没有专属字段」与「字段为空对象」是同一个值，
-- 少一种状态少一类 bug。
ALTER TABLE posts ADD COLUMN IF NOT EXISTS detail JSONB NOT NULL DEFAULT '{}'::jsonb;

-- 按 detail 里的字段查（如按 teacherName 搜评价）。GIN 索引支持 @> 包含查询。
CREATE INDEX IF NOT EXISTS idx_posts_detail_gin
    ON posts USING GIN (detail)
    WHERE deleted_at IS NULL;

COMMIT;
