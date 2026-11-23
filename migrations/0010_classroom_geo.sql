-- 0010_classroom_geo.sql：教室楼栋地址 + 进入路线 + 高德密钥池。
--
-- 需求（2026-11-23 维护者）：
--   1. 通过「教室」查到地图上的地址。**粒度是楼栋，不是具体教室**：
--        `10-B201` 与 `10-B213` 都是「十教B」；
--        `1-404`   与 `1-301`   都是「一教」。
--   2. 楼栋由管理员直接添加（管理端 CRUD）。
--   3. 可以添加「复杂的地址」= 进入路线，由管理员添加。
--      路线用**多点经纬度**（点状路线）+ 每点说明。
--   4. 位置存经纬度；**客户端不存死高德 key**，改由服务端密钥池下发。
--
-- 归一化策略：不逐个教室录入关联，而是从教室名**按规则提取楼栋键**。
--   `10-B201` → 楼栋键 `10-B`；`1-404` → 楼栋键 `1`。
-- 楼栋表里存同一个键，查询时对入参做同样归一化再精确匹配。
-- 这样「教务新开一间 B215」也不需要补数据。

BEGIN;

-- ---------- 楼栋 ----------
CREATE TABLE IF NOT EXISTS classroom_building (
    id           BIGSERIAL   PRIMARY KEY,

    -- **楼栋键**：从教室名归一化得到的稳定标识，查询时用它精确匹配。
    -- 形如 `10-B`（十教B）、`1`（一教）。唯一。
    building_key TEXT        NOT NULL,

    -- 楼栋号（原样，如 `10`、`1`），与 key 分开存便于展示与排序。
    building_no  TEXT        NOT NULL,
    -- 分区字母（如 `B`），无分区时为空串。
    zone         TEXT        NOT NULL DEFAULT '',

    -- 展示名（管理员填，如「十教B 栋」「第一教学楼」）。
    display_name TEXT        NOT NULL,
    -- 别名，便于搜索（逗号分隔或数组；数组更利于精确匹配）。
    aliases      TEXT[]      NOT NULL DEFAULT '{}',

    campus       TEXT        NOT NULL DEFAULT '',

    -- 经纬度。用 double precision 而非 numeric：地图坐标是连续的浮点量，
    -- numeric 的高精度在这里没有意义，且会让 Go 侧多一层转换。
    latitude     DOUBLE PRECISION,
    longitude    DOUBLE PRECISION,

    -- 地址文字描述（如「XX 路 1 号 十教 B 栋」）。
    address_text TEXT        NOT NULL DEFAULT '',
    -- 备注（如「B 栋入口在楼东侧」）。
    note         TEXT        NOT NULL DEFAULT '',

    enabled      BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 楼栋键唯一：一个归一化键只能对应一个楼栋，否则查询无法确定答案。
    CONSTRAINT uq_classroom_building_key UNIQUE (building_key),
    -- 纬度/经度必须成对出现（只有一半的坐标是无意义的）。
    CONSTRAINT ck_classroom_building_geo CHECK (
        (latitude IS NULL AND longitude IS NULL)
        OR (latitude IS NOT NULL AND longitude IS NOT NULL)
    ),
    -- 温区校验：挡掉明显的输错（如纬度 200）。
    CONSTRAINT ck_classroom_building_lat CHECK (
        latitude IS NULL OR (latitude >= -90 AND latitude <= 90)
    ),
    CONSTRAINT ck_classroom_building_lng CHECK (
        longitude IS NULL OR (longitude >= -180 AND longitude <= 180)
    )
);

-- 按楼栋键查（主查询路径）。
CREATE INDEX IF NOT EXISTS idx_classroom_building_key
    ON classroom_building (building_key) WHERE enabled;

-- 按楼栋号 + 分区查（管理端列表、按楼栋号反查）。
CREATE INDEX IF NOT EXISTS idx_classroom_building_no
    ON classroom_building (building_no, zone);

-- ---------- 进入路线 ----------
--
-- 一个楼栋可以有多条路线（从南门进 / 从地铁站进 / 从侧门进…）。
-- 路线用「多点经纬度」表达：每点带坐标 + 说明，前端连成线。
CREATE TABLE IF NOT EXISTS classroom_route (
    id           BIGSERIAL   PRIMARY KEY,
    building_id  BIGINT      NOT NULL
                 REFERENCES classroom_building(id) ON DELETE CASCADE,

    -- 路线标题（如「从南门进入」「从地铁 2 号线步行」）。
    title        TEXT        NOT NULL,
    -- 路线整体说明（电梯在哪、要不要刷卡等）。
    description  TEXT        NOT NULL DEFAULT '',

    -- 起点标签（如「南门」「地铁站」），便于客户端按起点筛选。
    start_label  TEXT        NOT NULL DEFAULT '',

    -- 是否推荐。列表可优先展示。
    recommended  BOOLEAN     NOT NULL DEFAULT FALSE,

    sort_order   INTEGER     NOT NULL DEFAULT 0,
    enabled      BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_classroom_route_building
    ON classroom_route (building_id, sort_order) WHERE enabled;

-- ---------- 路线上的点 ----------
--
-- 「点状路线」：按 seq 排序，每点一个坐标 + 一句说明。
-- 刻意不做「折线编码」（polyline encoding）：管理员手工录入的是少量关键点
-- （拐角、门口、电梯），几百个点才需要编码压缩，这里不需要。
CREATE TABLE IF NOT EXISTS classroom_route_point (
    id           BIGSERIAL   PRIMARY KEY,
    route_id     BIGINT      NOT NULL
                 REFERENCES classroom_route(id) ON DELETE CASCADE,

    -- 点的序位（从 1 开始，按升序连成线）。
    seq          INTEGER     NOT NULL,
    latitude     DOUBLE PRECISION NOT NULL,
    longitude    DOUBLE PRECISION NOT NULL,
    -- 该点的说明（如「进门后左转到电梯」）。
    label        TEXT        NOT NULL DEFAULT '',

    CONSTRAINT uq_classroom_route_point_seq UNIQUE (route_id, seq),
    CONSTRAINT ck_classroom_route_point_lat CHECK (latitude >= -90 AND latitude <= 90),
    CONSTRAINT ck_classroom_route_point_lng CHECK (longitude >= -180 AND longitude <= 180)
);

CREATE INDEX IF NOT EXISTS idx_classroom_route_point_route
    ON classroom_route_point (route_id, seq);

-- ---------- 高德 API 密钥池 ----------
--
-- 维护者的附加需求：客户端**不存死 key**，通过服务端密钥池取用。
--
-- 为什么需要「池」而不是一个 key：
--   * 高德的个人 key 有 QPS/日调用量配额，单 key 高峰会被限流；
--   * 多把 key 轮换能提高可用配额，且某把失效时可摘除而不影响全站。
--
-- 存的是**密文**：key 属敏感凭据（ADR-0002 的加密铁律）。
-- 加密在应用层用 TICKET_KEY 派生（与身份证号同一套 Cipher），
-- 因此本表只有 cipher 列，没有明文列。
CREATE TABLE IF NOT EXISTS amap_key (
    id           BIGSERIAL   PRIMARY KEY,

    -- 平台/用途标识（如 android / ios / web / server），便于按端分发不同 key。
    -- 高德的 key 是按平台绑定的，混用会验签失败。
    platform     TEXT        NOT NULL DEFAULT 'android',

    -- 密文 key（AES-256-GCM，见 platform.Cipher.Seal）。
    key_cipher   BYTEA       NOT NULL,

    -- 展示用后四位，便于管理员辨认是哪把 key（不含明文）。
    key_hint     TEXT        NOT NULL DEFAULT '',

    -- 限流配额（每秒请求数上限）。客户端拿到 key 后自己直连高德，
    -- 因此这里的配额只用于**服务端取用接口**的粗粒度节流，
    -- 不能替代高德侧的配额控制。
    qps_limit    INTEGER     NOT NULL DEFAULT 10,

    enabled      BOOLEAN     NOT NULL DEFAULT TRUE,
    -- 最近一次被取用的时间（用于轮换与排查）。
    last_used_at TIMESTAMPTZ,
    -- 被取用次数（观测）。
    used_count   BIGINT      NOT NULL DEFAULT 0,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_amap_key_platform
    ON amap_key (platform) WHERE enabled;

COMMIT;
