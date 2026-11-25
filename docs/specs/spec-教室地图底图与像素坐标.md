# 规格：教室地图底图与像素坐标

> 维护者需求（2026-11-25）：「添加一个传地图图片接口，地图图片放到 minio 中，然后也需要鉴权……将传位置的接口改为同时传位置的像素位置和地理位置，还有属于哪张图（该点是什么校区的）。」
>
> 后续确认（2026-11-25）：「换算公式放到数据库中不写死，都要像素，但是允许无像素（给其他未来接口兼容）」「管理员只用像素点位置，给用户的是两个都要，所有都要传 mapId」「用 campus 枚举」「认可 `GET /classrooms/maps/:campus` + `POST /admin/classrooms/maps`」。

本规格是 `internal/classroom/` 域的地图扩展。该域此前由迁移 `0010_classroom_geo.sql` 的注释直接承载需求，没有独立规格文件；按 `README.md:102`「新增业务接口前须先补……规格」，本次补齐并作为后续改动依据。

---

## 一、要解决的问题

教室导航现在只有「经纬度」一种坐标表达。这有两个具体缺陷：

1. **没有人能凭经纬度判断位置对不对**。管理员录入一栋楼时，只有一串数字（`29.978648, 102.995025`），他无法确认这个点在教学楼的哪一侧、是不是落在了湖里。校方提供的拼接底图才是他实际看着填的东西。
2. **客户端无法把点画在底图上**。底图是像素空间，业务数据是经纬度空间，两者之间缺一层可复用的换算。

同时，业务已经在向「多校区」演进：雅安、成都、都江堰各有自己的底图。**同一组像素坐标在不同校区图上含义完全不同**，不标校区就会串数据。

## 二、解决方案

**引入「底图」与「像素坐标」两个概念，并把像素作为管理员录入的一等输入。**

- **底图**（`classroom_map`）：每个校区一张，存 MinIO 对象键 + 图片尺寸 + **像素↔经纬度的相似变换参数**。
- **像素坐标**（`px`/`py`）：图像坐标，`px` 向右、`py` 向下，原点在图片左上角。
- **经纬度**（`lat`/`lng`）：GCJ-02（高德），由像素 + 该校区的换算参数**派生并随行存下**。

管理员录入传像素，后端算出经纬度一并存；读者侧两个都给。

**换算参数存数据库，不写死代码**。这是维护者的明确要求，理由也成立：

- 只有雅安做过人工标定（6 控制点，平均误差 1.51 m）。成都与都江堰**没有**参数。
- 硬编码会逼实现者在那两个校区伪造公式或复用雅安参数，两者都会静默产出错误坐标。
- 参数存在表里，未标定的校区就是「没有这一行」，行为可判定、可解释。
- 将来重新标定只改数据，不改代码、不发版。

## 三、接口

### 3.1 新增

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/classrooms/maps/:campus` | 需登录 | 下发底图图片字节（后端流式代理 MinIO） |
| `GET` | `/classrooms/maps/:campus/meta` | 需登录 | 底图元信息：尺寸、换算参数、四角经纬度 |
| `POST` | `/admin/classrooms/maps` | 管理端 | 管理员上传底图（multipart），替换该校区既有底图 |
| `GET` | `/admin/classrooms/maps` | 管理端 | 列出已上传的底图（含是否已标定换算参数） |

**为什么读图走后端流式代理而不是 `PresignGet`**：

与皮肤包域同一判断（`internal/platform/objectstore.go` 的注释已写明）。底图是校内地理数据，不把 MinIO 暴露给公网、不做 CORS、没有预签名过期窗口；也让端到端测试不需要起 MinIO。代价是流量过本服务——底图约 2.8 MB、按校区缓存，代价可接受。

**为什么需要 `/meta`**：客户端要把点画到底图上、还要做经纬度↔像素双向换算。只有图片字节不够——它需要图片尺寸和换算参数。给了 `meta`，客户端可以在本地互转，不必每次请求后端。

### 3.2 改造

`Building` 与 `RoutePoint` 增加像素坐标与校区归属。详见第六节数据契约。

## 四、坐标语义（必须逐条落实，写错则所有点位置错误）

1. **原点与方向**：图像坐标，原点左上，`px` 向右递增，`py` 向下递增。这与部分地图 SDK 的「左下原点」相反，API 文档与字段注释都要写明。
2. **基准坐标系是 GCJ-02**（高德）。雅安参数的拟合基准即 GCJ-02。**不是 WGS84**：若客户端用 WGS84 底图叠加，会有数十到数百米偏移。API 响应里显式给 `coordSystem: "GCJ-02"`，不靠文档口头约定。
3. **像素只在所属校区内有效**。`px/py` 的含义由 `campus` 决定，离开 `campus` 讨论 `px/py` 无意义。
4. **换算参数结构**（相似变换 / Procrustes，6 控制点拟合）：

   ```text
   x  = ar*px + ai*py + tx0
   y  = ai*px - ar*py + ty0
   lng = lng0 + x / kx
   lat = lat0 + y / ky
   ```

   其中 `ar`/`ai` 是旋转+尺度（复数形式的分量），`tx0`/`ty0` 是平移，`lng0`/`lat0` 是基准点，`kx`/`ky` 是该纬度下 1 经度/1 纬度对应的**米数**。

   逆变换：

   ```text
   x = (lng - lng0) * kx
   y = (lat - lat0) * ky
   det = -(ar*ar + ai*ai)
   px = (-ar*(x-tx0) - ai*(y-ty0)) / det
   py = (-ai*(x-tx0) + ar*(y-ty0)) / det
   ```

## 五、写入门槛：强制传像素，不兼容旧数据

维护者最终口径（2026-11-25）：「**不兼容旧数据，强制要传**」。

早期曾考虑「列可空、仅写入门槛要求像素」以兼容 `0010` 时代只录经纬度的存量数据。**该方案已作废**，理由是不兼容旧数据换来的一致性更重要：

- 列可空会出现「有的行能用像素、有的不能」，上游必须处处判断，最终一定漏判；
- 「有没有像素」本该是一个确定不变的事实，而不是每次读取都要分支处理的状态。

落地规则：

1. **`classroom_building.map_px/map_py` 与 `classroom_route_point.map_px/map_py` 均为 `NOT NULL`**（迁移 `0013`）。
2. **新建/更新楼栋、路线点必须传 `campus` + `mapPx` + `mapPy`**；只传 `lat`/`lng` → `400`。
3. 即使同时传了 `lat`/`lng`，也**一律以像素派生值覆盖**：同一行存在两套互相矛盾的坐标时，读者无法判断该信哪个。
4. **未标定换算参数的校区**（成都、都江堰）可以上传底图，但**传像素会被拒**：`400`，错误「该校区尚未标定像素-地理换算参数」。绝不退回雅安参数、绝不猜一个。
5. **升级时存量数据必须已回填**：迁移 `0013` 会检查既有无像素的行，有则**整个迁移失败**并报出行数。宁可升级失败，也不静默把不合规数据留在库里。

第 4、5 条让「没有参数」「数据不合规」成为**显式可判定的失败**，而不是静默的错误坐标。

## 六、数据契约

### 6.1 新表 `classroom_map`

| 列 | 类型 | 说明 |
| --- | --- | --- |
| `id` | BIGSERIAL | 主键 |
| `campus` | TEXT NOT NULL UNIQUE | 校区枚举值（见 6.2） |
| `object_key` | TEXT NOT NULL | MinIO 对象键 |
| `content_type` | TEXT NOT NULL | `image/gif` / `image/png` / `image/jpeg` |
| `size_bytes` | BIGINT NOT NULL | 字节数 |
| `etag` | TEXT NOT NULL DEFAULT '' | 对象存储返回的 etag |
| `width` | INTEGER NOT NULL | 图片宽（像素） |
| `height` | INTEGER NOT NULL | 图片高（像素） |
| `coord_system` | TEXT NOT NULL DEFAULT 'GCJ-02' | 坐标系标识 |
| `ar`/`ai`/`tx0`/`ty0`/`lng0`/`lat0`/`kx`/`ky` | DOUBLE PRECISION，**全部可空** | 换算参数；全空 = 未标定 |
| `calibration_note` | TEXT NOT NULL DEFAULT '' | 标定说明（控制点来源、误差） |
| `enabled` | BOOLEAN NOT NULL DEFAULT TRUE | |
| `created_at`/`updated_at` | TIMESTAMPTZ | |

**约束**：

- `campus` 唯一：一个校区只能有一张底图（换图 = 覆盖）。
- **换算参数必须全有或全无**：`CHECK` 约束保证 8 个参数要么都为 `NULL`，要么都非 `NULL`。半套参数是无意义的，且会让逆变换静默出错。
- `width > 0 AND height > 0`。
- `size_bytes > 0`。

### 6.2 校区枚举

受控词表，**值固定为**：

| 值 | 校区 | 底图 |
| --- | --- | --- |
| `yaan` | 雅安 | 已有（12488×5586，已标定） |
| `chengdu` | 成都 | 无 |
| `dujiangyan` | 都江堰 | 无 |

维护者提出过用数字 `0/1/2` 表达。最终选**字符串枚举**，理由：数字在数据库、日志、API 里都不可读，排错时无法一眼确认；数字一旦写进客户端就永久成为协议的一部分，改不动。字符串枚举的可读性收益大过「省几个字节」。

**兼容**：`classroom_building.campus` 现为自由文本（默认空串）。本次不强制回填，但**新增写入必须**是上述三值之一；空串在读取时视为「未归属校区」，此时 `px/py` 必须也为空。

### 6.3 `classroom_building` 增列

| 列 | 类型 | 说明 |
| --- | --- | --- |
| `map_px` | DOUBLE PRECISION NULL | 图像 x（向右） |
| `map_py` | DOUBLE PRECISION NULL | 图像 y（向下） |

`campus` 列已存在，语义收紧为上述枚举。

**约束**：`map_px`/`map_py` 成对出现；有像素时 `campus` 必须非空且 `lat`/`lng` 必须非空（经纬度是派生值，写入时一并算出）。

### 6.4 `classroom_route_point` 增列

同上加 `map_px`、`map_py`。路线点所属校区由**所属楼栋**决定，不单独存 `campus`——路线属于楼栋，两者校区不一致是无意义状态，用外键关系表达比冗余一列更稳。

约束：`map_px`/`map_py` 成对出现；有像素时 `latitude`/`longitude` 仍为 `NOT NULL`（该表现有约束，保持不变）。

## 七、API 契约

### 7.1 `GET /classrooms/maps/:campus`

- 成功：`200`，`Content-Type` 为存储时的类型，`Cache-Control: public, max-age=86400`（底图不变，可强缓存；换图时 `object_key` 变化，URL 带内容版本见下）。
- 未上传该校区底图：`404`，`{"code":"not_found","message":"该校区暂无地图"}`
- 非法 campus：`400`
- 需登录；不进 `publicPaths`。

**缓存与换图**：`meta` 里给 `objectKey`（含内容指纹），客户端取图 URL 用 `?v=<objectKey>`。换图后 `objectKey` 变，客户端缓存自然失效，无需后端主动清缓存。

### 7.2 `GET /classrooms/maps/:campus/meta`

```json
{
  "data": {
    "campus": "yaan",
    "campusLabel": "雅安",
    "width": 12488,
    "height": 5586,
    "coordSystem": "GCJ-02",
    "contentType": "image/gif",
    "sizeBytes": 2772820,
    "objectKey": "classroom/maps/yaan-<sha256前12位>.gif",
    "calibrated": true,
    "calibration": {
      "ar": 0.19050900468989643,
      "ai": 0.0002290787340230358,
      "tx0": -1509.247805327019,
      "ty0": 525.6390234504005,
      "lng0": 102.995025,
      "lat0": 29.978648,
      "kx": 96426.68366048264,
      "ky": 110540.0
    },
    "calibrationNote": "6 控制点相似变换，平均误差 1.51 m，最大 1.94 m",
    "corners": {
      "leftTop":     {"px": 0,     "py": 0,     "lat": 29.983403, "lng": 102.979373},
      "rightTop":    {"px": 12488, "py": 0,     "lat": 29.983429, "lng": 103.004046},
      "rightBottom": {"px": 12488, "py": 5586,  "lat": 29.973802, "lng": 103.004059},
      "leftBottom":  {"px": 0,     "py": 5586,  "lat": 29.973776, "lng": 102.979387}
    }
  }
}
```

- `calibrated: false` 时 `calibration` 为 `null`、`corners` 为 `null`。
- `corners` 由后端算，避免每个客户端各写一遍换算。上表数值由 `yaan_px2ll.py` 实算得出（保留 6 位小数），作为 Go 测试的回归基准。

### 7.3 `POST /admin/classrooms/maps`

- `multipart/form-data`：`campus`（必填）、`file`（必填）、`calibrationNote`（可选）。
- 允许类型：`image/gif`、`image/png`、`image/jpeg`。**按魔数嗅探**，不信 `Content-Type`（客户端可伪造）。
- 大小上限 16 MiB（底图是静态资产，给足余量；仍设上限防误传大文件）。
- **尺寸从图片字节真实解码得出，不信客户端声明**。GIF/PNG/JPEG 均读头部。
- 成功：`200` + 与 `meta` 相同结构。`calibrated` 取决于该校区是否已有参数行；上传新图**不覆盖**既有换算参数（换图不等于重新标定）。
- 覆盖语义：同校区重复上传 → 替换 `object_key`/尺寸/类型；旧对象**删除**（避免存储泄漏）。

### 7.4 读取侧新增字段

`buildingView` 与 `pointView` 增加：

```go
Campus string   `json:"campus,omitempty"`
MapPx  *float64 `json:"mapPx,omitempty"`
MapPy  *float64 `json:"mapPy,omitempty"`
```

原有 `lat`/`lng` 保留。三者同时出现时含义为「该点在 `campus` 这张图的 `(mapPx, mapPy)` 像素处，对应 GCJ-02 `(lat, lng)`」。

### 7.5 管理端写入请求体

`buildingRequest` 与 `pointRequest` 增加 `campus`/`mapPx`/`mapPy`，按第五节规则校验。

## 八、错误码

沿用现有 `apperr`：

| 场景 | code | HTTP |
| --- | --- | --- |
| 非法 campus / 缺像素 / 像素越界 | `validation_failed` | 400 |
| 该校区未标定换算参数却传了像素 | `validation_failed`，消息含校区名与「尚未标定」 | 400 |
| 底图不存在 | `not_found` | 404 |
| 上传类型不支持 / 超过上限 / 图片解码失败 | `validation_failed` | 400 |
| 未登录 | 由全局会话中间件给 `401` | 401 |

## 九、测试

**层一 · 纯函数单测**（坐标换算是本规格最容易错的地方，必须单测）：

1. `yaan_px2ll` 的 8 个常量与 `yaan_px2ll.py` 逐值一致（回归锚，防止抄错小数点）。
2. 往返一致性：对一组采样像素 `px2ll` 再 `ll2px`，误差 < 1e-6 像素。
3. 已知控制点：用 py 脚本算出的四角与中心经纬度作为期望值。
4. 未标定参数时：换算返回明确的「未标定」错误，不返回零值坐标。
5. 像素越界（负数、超出 `width`/`height`）→ 校验错误。

**层二 · HTTP 集成测**（内存 Repo + 内存 ObjectStore）：

6. 上传底图 → 取 `meta` 尺寸正确、`calibrated` 正确。
7. 未上传校区的 `GET /classrooms/maps/:campus` → 404。
8. 非法 campus → 400。
9. 未登录 → 401。
10. 上传非图片（伪装成 png 的文本）→ 400（魔数嗅探生效）。
11. 重复上传同校区 → 旧对象被删除、新对象生效。
12. 未标定校区传像素 → 400，消息含校区名。

**层三 · 迁移**：`0013` 迁移在空库与既有库（已有 `0010` 数据）上都能执行；参数全有或全无的 `CHECK` 生效。

## 十、不做（Out of Scope）

1. **成都/都江堰的换算参数**：需要人工标定控制点，本次没有数据。它们可以传图，传像素会被明确拒绝。
2. **底图切片（瓦片金字塔）**：单张 2.8 MB 一次性下发足够，切片是独立工程。
3. **多张图/一个校区多底图**：一校区一图，换图即覆盖。
4. **管理员在 Web 端标定参数的 UI**：参数由运维直接写库或由后续接口提供；本规格只提供读取与使用。
5. **WGS84/GCJ-02 互转**：只声明坐标系，不做纠偏。需要时另开。
6. **历史数据回填像素**：现有楼栋保持「无像素」状态合法。

## 十一、不动清单

1. 不改 `0010`/`0011`/`0012` 迁移文件，只新增 `0013`。
2. 不改 `platform.ObjectStore` 接口方法集（`Put`/`Get`/`PresignGet`/`Delete`/`Stat` 已够用）。
3. 不改 `/classrooms/lookup`、`/classrooms/buildings`、`/classrooms/buildings/:id` 的既有响应字段语义（只增不改）。
4. 不把地图端点加进 `publicPaths`。
5. 不引入新第三方依赖：GIF/PNG/JPEG 头部解析用标准库 `image/gif`、`image/png`、`image/jpeg` 的 `DecodeConfig`。
6. 不在业务域里 import MinIO/S3 SDK——只经 `platform.ObjectStore`。
7. 高德 key 池（`amap_key`）不动。

## 十二、遗留与未验证

| 项 | 状态 | 保守设计兜住什么 |
| --- | --- | --- |
| 成都/都江堰换算参数 | **未标定** | 传像素被显式拒绝；不会用雅安参数静默产出错误坐标 |
| 雅安参数长期精度 | 6 控制点、平均 1.51 m | 对「找到哪栋楼」够用；对「定位到哪个门」偏紧，参数可随标定更新 |
| GCJ-02 与客户端底图是否一致 | **未验证** | 响应显式给 `coordSystem`，客户端可判断并自行纠偏 |
| 底图版权与可分发范围 | **未知** | 校内使用；若要对外分发需另行确认授权 |
