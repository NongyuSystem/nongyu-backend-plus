-- 0008_fetch_accounts.sql：教务「取数账号」池（公共服务专用）。
--
-- 背景：通知/竞赛/教师课表等全站共有、不需登录的教务页，不该让每个学生
-- 各自用自己的 tgt + 网关票去取，也不该按 <userID> 各缓存一份。
-- 改为：一组专用取数账号（只有 go-admin-tgt 整套 auth cookie + wengine_vpn_ticket，
-- 没有学号、没有角色、没有任何业务数据），由管理员维护；公共页统一用池里的
-- 账号取数，缓存写全局键 <prefix>cache:public:<dataType>，所有学生共享一份。
--
-- 安全：
--   - tgt_sealed / vpn_ticket_sealed 存的是 AES-256-GCM 密文（密钥由 TICKET_KEY 派生），
--     列里绝不出现明文凭据；管理员列表接口也不回显。
--   - 只有 adminGroup（ScopeAdmin + RoleAdmin）能增删改查，普通 app 会话够不着。
--   - 复用现有 TGTManager 的滚动续期：tgt_sealed 存的是 TGTBundle JSON 密文，
--     EnsureFresh 解出整套 cookie 后自动换新 tgt 并回写。
--
-- 与 0001~0007 口径一致：snake_case、timestamptz、IF NOT EXISTS。

BEGIN;

CREATE TABLE IF NOT EXISTS jiaowu_fetch_accounts (
    id                  BIGSERIAL PRIMARY KEY,
    -- 管理员可读的备注名，仅用于辨认，不参与鉴权。
    label               TEXT        NOT NULL DEFAULT '',
    -- TGTBundle{TGT, CookieHeader} 的 JSON 再经 AES-GCM 封装后的密文（bytea）。
    tgt_sealed          BYTEA       NOT NULL,
    -- wengine_vpn_ticket 的 AES-GCM 密文（bytea）。管理员手动更新。
    vpn_ticket_sealed   BYTEA       NOT NULL,
    -- 停用后不再被池选中，但记录保留（便于回滚/审计）。
    enabled             BOOLEAN     NOT NULL DEFAULT TRUE,
    -- 连续取数失败次数；达到阈值可临时跳过，由池实现决定策略。
    failure_count       INTEGER     NOT NULL DEFAULT 0,
    last_used_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 同一标签不重复，避免管理员误建两份同账号。
CREATE UNIQUE INDEX IF NOT EXISTS jiaowu_fetch_accounts_label_uidx
    ON jiaowu_fetch_accounts (label)
    WHERE label <> '';

COMMIT;
