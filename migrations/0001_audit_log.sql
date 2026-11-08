-- 0001_audit_log.sql
-- 审计流水表：记录「谁、何时、访问了哪个逻辑目标、结果如何」。
--
-- 铁律（ADR-0001 第 4 条）：本表绝不存储票据、密文、验证码、上游响应体。
-- 只允许写 user_id / action / target / result / request_id 这几个标识性字段。

CREATE TABLE IF NOT EXISTS audit_log (
    id          bigserial PRIMARY KEY,
    -- request_id 是一次请求链路的追踪号（即「审计流水号」）。
    request_id  text        NOT NULL,
    -- 已认证身份（来自 school.CtxUserID）；未认证场景可为空串。
    user_id     text        NOT NULL DEFAULT '',
    -- 业务动作：login / logout / address / fetch / captcha / login_accel / renew
    action      text        NOT NULL,
    -- 逻辑目标名：jiaowu / auth / webvpn
    target      text        NOT NULL DEFAULT '',
    -- 结果码：ok / target_denied / outbound_error / captcha_error / ...
    result      text        NOT NULL DEFAULT '',
    -- 事件发生时刻（由应用时钟给出，便于测试注入）
    occurred_at timestamptz NOT NULL,
    -- 入库时刻（数据库时钟）
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- 按用户查「这个人都做过什么」
CREATE INDEX IF NOT EXISTS idx_audit_log_user_time
    ON audit_log (user_id, occurred_at DESC);

-- 按动作类型查「最近谁登录失败了」
CREATE INDEX IF NOT EXISTS idx_audit_log_action_time
    ON audit_log (action, occurred_at DESC);

-- 按流水号串起一次请求的全部动作
CREATE INDEX IF NOT EXISTS idx_audit_log_request
    ON audit_log (request_id);
