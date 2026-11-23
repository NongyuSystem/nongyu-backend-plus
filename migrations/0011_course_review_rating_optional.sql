-- 0011_course_review_rating_optional.sql：课程评价的评分改为选填。
--
-- 背景（2026-11-23 维护者）：
--   课程评价里评分不是必填 —— 用户只想留言时，不该被强制打星。
--   不打星的评价，detail 里不含 rating 字段；列表展示时不显示星。
--
-- 为什么用 UPDATE 而不是重改 0009：
--   0009 在已部署环境已执行，改历史迁移不会重跑；新开一条才生效。
--
-- detail 是 JSONB，选填字段缺失即"未评分"，不需要改 posts 表结构。

BEGIN;

UPDATE post_type_rule
   SET required = FALSE
 WHERE type_code = 'course_review'
   AND field = 'rating';

COMMIT;
