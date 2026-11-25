-- 0013_classroom_map.sql：校区底图 + 像素坐标（spec-教室地图底图与像素坐标.md）。
--
-- 需求（2026-11-25 维护者）：
--   1. 加「传地图图片」接口，图片放 MinIO，需鉴权。
--   2. 「传位置」改为同时传**像素位置**与**地理位置**，并标出属于哪张图（校区）。
--   3. 管理员只用像素点位置；给用户的是两个都要；所有都要传校区。
--   4. **换算公式放数据库，不写死代码。**
--   5. 都要像素，但**允许无像素**（给其它未来接口兼容）。
--
-- 第 4 条是本次的关键决定。只有雅安做过人工标定（6 控制点，平均误差 1.51 m）；
-- 成都与都江堰没有参数。若把参数写死在代码里，实现者只能在「伪造公式」与
-- 「复用雅安参数」之间二选一，两者都会静默产出错误坐标。放进表里，
-- 「未标定」就是一个**可显式判定的状态**（没有这一行），而不是一个隐蔽的错误。
--
-- 第 5 条的落法：写入契约要求像素（管理端新建/更新必须传），
-- 但列允许为 NULL，使 0010 时代只录经纬度的存量数据继续合法。

BEGIN;

-- ---------- 校区底图 ----------
--
-- 一个校区一张图（campus 唯一）。换图 = 覆盖同一行并替换 object_key，
-- 而不是插第二行——「一个校区同时有两张底图」会让「该点在哪张图上」失去答案。
CREATE TABLE IF NOT EXISTS classroom_map (
    id             BIGSERIAL   PRIMARY KEY,

    -- 校区枚举：yaan / chengdu / dujiangyan（规格 §6.2）。
    --
    -- 维护者提出过用数字 0/1/2。最终选字符串：数字在库、日志、API 里都不可读，
    -- 排错时无法一眼确认；且数字一旦写进客户端就永久成为协议的一部分，改不动。
    campus         TEXT        NOT NULL,

    -- MinIO 对象键。含内容指纹（见上传实现），换图即换键，
    -- 客户端带 ?v=<objectKey> 的缓存自然失效，后端无需主动清缓存。
    object_key     TEXT        NOT NULL,
    content_type   TEXT        NOT NULL,
    size_bytes     BIGINT      NOT NULL,
    etag           TEXT        NOT NULL DEFAULT '',

    -- 图片真实尺寸。由上传时**解码图片字节**得出，不信客户端声明——
    -- 尺寸错会让所有像素坐标系统性偏移，且很难察觉。
    --
    -- 尺寸与体积**允许为 0**：雅安在本次迁移里只种「换算参数」，
    -- 底图字节要经 POST /admin/classrooms/maps 上传。未上传时
    -- object_key 为空串、尺寸为 0；上传后才有真实值。
    -- 用下方的 CHECK 保证「有对象键就必须有正尺寸」，而不是一味要求 > 0。
    width          INTEGER     NOT NULL DEFAULT 0,
    height         INTEGER     NOT NULL DEFAULT 0,

    -- 坐标系标识。雅安参数的拟合基准是 GCJ-02（高德），不是 WGS84。
    -- 显式存下来并在 API 响应里回给客户端：两者叠加会有数十到数百米偏移，
    -- 靠口头约定不安全。
    coord_system   TEXT        NOT NULL DEFAULT 'GCJ-02',

    -- ---- 像素 -> 经纬度的相似变换参数（Procrustes，复数形式）----
    --
    --   x   = ar*px + ai*py + tx0
    --   y   = ai*px - ar*py + ty0
    --   lng = lng0 + x / kx
    --   lat = lat0 + y / ky
    --
    -- ar/ai 是「旋转 + 尺度」的复数分量，tx0/ty0 是平移，
    -- lng0/lat0 是基准点，kx/ky 是该纬度下 1 经度、1 纬度对应的米数。
    --
    -- **八列全部可空**：全空 = 该校区尚未标定。半套参数是无意义的，
    -- 且会让逆变换静默出错，故用下面的 CHECK 保证「全有或全无」。
    ar             DOUBLE PRECISION,
    ai             DOUBLE PRECISION,
    tx0            DOUBLE PRECISION,
    ty0            DOUBLE PRECISION,
    lng0           DOUBLE PRECISION,
    lat0           DOUBLE PRECISION,
    kx             DOUBLE PRECISION,
    ky             DOUBLE PRECISION,

    -- 标定说明：控制点来源、残差、标定日期。运维排错第一眼要看的东西。
    calibration_note TEXT      NOT NULL DEFAULT '',

    enabled        BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 一校区一图：唯一的稳定答案。
    CONSTRAINT uq_classroom_map_campus UNIQUE (campus),

    -- 尺寸与体积的一致性：**有对象键就必须有正尺寸**；
    -- 未上传（object_key 为空串）时允许全为 0。这样「行存在」表达的是
    -- 「该校区已登记底图条目」，而「已上传」由 object_key 非空表达——
    -- 两者分开，雅安才能只种参数、不带图字节。
    CONSTRAINT ck_classroom_map_uploaded CHECK (
        (object_key = ''  AND width = 0 AND height = 0 AND size_bytes = 0)
        OR
        (object_key <> '' AND width > 0 AND height > 0 AND size_bytes > 0)
    ),

    -- 换算参数全有或全无。八个 IS NULL 必须同真同假：
    -- 用「全空」与「全非空」两个析取表达，中间态一律被拒。
    CONSTRAINT ck_classroom_map_calibration_all_or_nothing CHECK (
        (ar IS NULL AND ai IS NULL AND tx0 IS NULL AND ty0 IS NULL
         AND lng0 IS NULL AND lat0 IS NULL AND kx IS NULL AND ky IS NULL)
        OR
        (ar IS NOT NULL AND ai IS NOT NULL AND tx0 IS NOT NULL AND ty0 IS NOT NULL
         AND lng0 IS NOT NULL AND lat0 IS NOT NULL AND kx IS NOT NULL AND ky IS NOT NULL)
    ),
    -- 尺度与米数不能为 0（会让逆变换 det = -(ar²+ai²) = 0 直接除零）。
    CONSTRAINT ck_classroom_map_scale CHECK (
        (kx IS NULL OR kx <> 0) AND (ky IS NULL OR ky <> 0)
        AND (ar IS NULL OR (ar <> 0 OR ai <> 0))
    ),
    -- 基准点温区校验，挡掉明显输错（如纬度 200）。
    CONSTRAINT ck_classroom_map_lat0 CHECK (lat0 IS NULL OR (lat0 >= -90  AND lat0 <= 90)),
    CONSTRAINT ck_classroom_map_lng0 CHECK (lng0 IS NULL OR (lng0 >= -180 AND lng0 <= 180))
);

-- 读取主路径：按校区取启用的底图。
CREATE INDEX IF NOT EXISTS idx_classroom_map_campus
    ON classroom_map (campus) WHERE enabled;

-- ---------- 雅安参数种子行 ----------
--
-- 参数来自人工标定（yaan_px2ll.py / yaan_px2ll.kt）。
-- **只种换算参数与标定说明，不种图字节**：底图要经 POST /admin/classrooms/maps
-- 上传。object_key 置空串、尺寸置 0，表示「已标定但未上传」——
-- 由 ck_classroom_map_uploaded 明确区分这两种状态。
--
-- 用 WHERE NOT EXISTS 而非 ON CONFLICT：本仓迁移可能在已手工种过数据的环境执行，
-- 重复执行不应报错也不应覆盖运维后来重新标定的值。
INSERT INTO classroom_map
    (campus, object_key, content_type, size_bytes, width, height, coord_system,
     ar, ai, tx0, ty0, lng0, lat0, kx, ky, calibration_note)
SELECT
    'yaan', '', 'image/gif', 0, 0, 0, 'GCJ-02',
    0.19050900468989643,
    0.0002290787340230358,
    -1509.247805327019,
    525.6390234504005,
    102.995025,
    29.978648,
    96426.68366048264,
    110540.0,
    '6 控制点相似变换（Procrustes），平均误差 1.51 m，最大 1.94 m；'
    || '尺度 0.190509 m/px，旋转 0.069°；基准 GCJ-02。'
    || '参考图 12488×5586。'
WHERE NOT EXISTS (SELECT 1 FROM classroom_map WHERE campus = 'yaan');

-- ---------- classroom_building 增列：像素坐标 ----------
--
-- 管理员录楼栋时传像素（他看的是底图），后端算出经纬度一并存。
--
-- **维护者决定：不兼容旧数据，强制要传**（2026-11-25 原话「不兼容旧数据，
-- 强制要传」）。因此这里不是「可空 + 写入时校验」，而是**直接 NOT NULL**：
-- 存量行必须先回填像素才能升级，这一点会在下面的回填步骤里显式失败，
-- 而不是靠应用层静默放过。
ALTER TABLE classroom_building
    ADD COLUMN IF NOT EXISTS map_px DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS map_py DOUBLE PRECISION;

-- 存量数据的处置：有经纬度但无像素的行，无法自动反推像素——
-- 反推需要「该行所属校区的换算参数」，而现存数据的 campus 多为空串。
--
-- 所以这里**显式检查并拒绝**：若有存量行缺像素，整个迁移失败，
-- 由运维先补齐数据（或确认这些行可删）再重跑。宁可升级失败，
-- 也不静默把不合规数据留在库里——那会变成「有的行能用像素、有的不能」，
-- 上游必须处处判断，最终一定漏判。
DO $$
DECLARE
    bad_count BIGINT;
BEGIN
    SELECT count(*) INTO bad_count
      FROM classroom_building
     WHERE map_px IS NULL OR map_py IS NULL;
    IF bad_count > 0 THEN
        RAISE EXCEPTION
            'classroom_building 有 % 行缺少像素坐标；本次迁移不兼容旧数据，'
            '请先回填 map_px/map_py（及各行的 campus）后重跑。'
            '回填方式见 docs/specs/spec-教室地图底图与像素坐标.md §5。',
            bad_count;
    END IF;
END $$;

ALTER TABLE classroom_building
    ALTER COLUMN map_px SET NOT NULL,
    ALTER COLUMN map_py SET NOT NULL;

-- 像素取值为有限数且非负：图像坐标原点在左上，负数一定是错的。
-- 上限依赖各校区底图尺寸，属跨表规则，由 Service 校验（见 deriveGeoFromPixel）。
ALTER TABLE classroom_building
    DROP CONSTRAINT IF EXISTS ck_classroom_building_px_nonnegative;
ALTER TABLE classroom_building
    ADD CONSTRAINT ck_classroom_building_px_nonnegative CHECK (
        map_px >= 0 AND map_py >= 0
    );

-- 有像素就必须有校区：像素坐标系由校区底图定义，没有校区无法解释。
ALTER TABLE classroom_building
    DROP CONSTRAINT IF EXISTS ck_classroom_building_px_needs_campus;
ALTER TABLE classroom_building
    ADD CONSTRAINT ck_classroom_building_px_needs_campus CHECK (
        campus <> ''
    );

-- ---------- classroom_route_point 增列：像素坐标 ----------
--
-- 路线点同理强制必传。**不单独存 campus**：路线属于楼栋，两者校区不一致
-- 是无意义状态，用外键关系表达比冗余一列更稳（查楼栋即得校区）。
ALTER TABLE classroom_route_point
    ADD COLUMN IF NOT EXISTS map_px DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS map_py DOUBLE PRECISION;

DO $$
DECLARE
    bad_count BIGINT;
BEGIN
    SELECT count(*) INTO bad_count
      FROM classroom_route_point
     WHERE map_px IS NULL OR map_py IS NULL;
    IF bad_count > 0 THEN
        RAISE EXCEPTION
            'classroom_route_point 有 % 行缺少像素坐标；本次迁移不兼容旧数据，'
            '请先回填 map_px/map_py 后重跑。',
            bad_count;
    END IF;
END $$;

ALTER TABLE classroom_route_point
    ALTER COLUMN map_px SET NOT NULL,
    ALTER COLUMN map_py SET NOT NULL;

ALTER TABLE classroom_route_point
    DROP CONSTRAINT IF EXISTS ck_classroom_route_point_px_nonnegative;
ALTER TABLE classroom_route_point
    ADD CONSTRAINT ck_classroom_route_point_px_nonnegative CHECK (
        map_px >= 0 AND map_py >= 0
    );

COMMIT;
