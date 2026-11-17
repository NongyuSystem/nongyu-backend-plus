# 决议：Spec 的章节结构与验收边界（学校二课收口方式）

> 来源：[决议：Spec 章节结构与验收边界](https://github.com/NongyuSystem/nongyu-backend-plus/issues/64)（wayfinder map [#58](https://github.com/NongyuSystem/nongyu-backend-plus/issues/58) **收口票**，本图最后一张），2026-11-17。
> 性质：**决策记录**，不是 Spec 本体。本票定「Spec 怎么组织」，Spec 由 `/to-spec` 产出。
> 循先例：[决策-Spec收口方式.md](决策-Spec收口方式.md)（#51，六类功能）、#36（身份域）。

---

## 前置（已锁，不在本票重开）

- **PRD 已存在**：#66 产出 [prd-学校二课接入.md](../specs/prd-学校二课接入.md)（216 行九节，`d70515c`）。→ **不重开「要不要补 PRD」**。
- **八份决策笔记**：[凭据边界](决策-二课凭据边界与ADR-0001再修订.md)（#69）、[范围切分](决策-二课范围切分与OutOfScope.md)（#59）、[横切口径](决策-二课横切口径.md)（#63）、[记录语义](决策-二课记录语义.md)（#61）、[配方契约](决策-二课写类配方契约.md)（#62）、[术语](<../../GLOSSARY.md>)（#65）+ [ADR-0001](../adr/0001-服务端代请求边界由新后端重写.md) 二课追加节。
- **架构形状**：[架构落点草图-学校二课.md](架构落点草图-学校二课.md)（#60，HITL 六项确认）。
- **真实响应夹具**：[testdata/secondclass/](<../../testdata/secondclass/README.md>)（#70，commit `804afbb`）——**这是本 effort 唯一的一手实测数据**。
- **仓内 Spec 惯例**：`docs/specs/` 现有 3 份（194 / 327 / 455 行），**一个 effort 一份**，无拆多份先例。

---

## 〇、本票与前三个 effort 收口票的**关键不同**

前三个 effort（#1/#30/#43）收口时，**上游事实全部来自 DEX 静态分析**，没有真实响应。

**本 effort 有 #70 的实测数据，且实测推翻了三条静态结论**：

| 静态分析说 | **线上实测** | 出处 |
| --- | --- | --- |
| 信封字段 `data` | **`content`** | #70 |
| `code` 是 `int` 或 `String`（二选一） | **两者并存**：成功 `"0"`（字符串）、未登录 `6`（数字） | #70 |
| 登录路径 `login/v1.0.0/…`、`sid` 是数字 | **`/user/login/v1.0.0/…`**、`sid` 是学校 UUID | #70 |

**所以本票的 Q1 不是「照抄十节」，而是「怎么在 Spec 里表达一手实测压过静态分析」**——这是前三个先例没有的问题。

---

## 一、Q1 文件与节序

**一份** `docs/specs/spec-学校二课接入.md`，**十一节**（#51 的十节 + 一节新增）：

| # | 节 | 内容 | 为什么不省 |
| --- | --- | --- | --- |
| 1 | Problem Statement | 引 PRD §一，不重写 | — |
| 2 | Solution | 引架构草图：一张配方表 + 一个记录子域，无出站无凭据 | — |
| 3 | 横切口径 | 信封／错误码／鉴权／盲区／NY1／脱敏，引 #63 | 供下文各节引用，避免重复 |
| 4 | User Stories | 按五组（学分成绩单／活动浏览报名／凭据登录／记录可观测／失败边界），引 PRD §三 | — |
| 5 | 各能力的 Decisions | 引 #61/#62，不重写 | — |
| 6 | **数据契约** | **配方七字段 + 参数槽位 + 四端点 + `content` 信封 + dataType 词表** | **Spec 与 PRD 的真正分界** |
| 7 | **一手实测与静态分析的差异** | #70 的三条推翻 + 四条补充事实 | **新增节，见下** |
| 8 | Testing Decisions | 两层接缝（§三） | — |
| 9 | Out of Scope | **以 map #58 的 Out of Scope 为准**（比 PRD 摘录更全） | — |
| 10 | Further Notes | pending 四层（§二）+ 依赖策略 | — |
| 11 | **不动清单** | #63 的 11 条**原样收录**（§四） | — |

**第 7 节为什么必须单独成节**（本票核心判断）：把实测差异**散写进数据契约**，将来读者只看第 6 节时会把 `content` 当既定事实，**看不出它曾是 `data`、也看不出静态分析错过**；而**只放附录**又会被当脚注忽略。**单独成节 + 第 6 节显式引用它**，才能让「一手实测压过静态分析」这件事在文档结构上可见。

**否决**：照抄十节（第 7 节无处安放，实测修正会被稀释进各节）；把实测差异并进 Further Notes（Further Notes 是「未定项」，实测差异是「已定项」——性质相反）。

---

## 二、Q2 pending 分层：按「卡不卡施工」分四层

沿用 #51 的口径，但**按本 effort 的实际内容重新分**：

### ① 阻塞开工（Spec 必须写死，不定不能写代码）

1. **配方七字段结构**：`id`/`target`/`method`/`path`/`contentType`/`auth`/`params`+`fields`（#62）
2. **参数槽位 `origin` 三值**：`client_local`/`fixed`/`optional`（#62）
3. **端点路径与请求响应形状**（2026-11-17 按维护者决定改为 cache 形态）：
   - 读三类：`GET/POST/DELETE /school/secondclass/cache/:dataType`——命中直返 / 未命中回 `cache_miss` **并捎带配方**
   - 写一类：`GET /school/secondclass/recipe/:recipeId`——写不经 GET，没有 miss 时机，必须独立入口
   - **理由**：二课 61 接口里 47 读 / 14 写（学生视角 47:10，读占 82%），读类占绝大多数；合并进 `cache_miss` 可让客户端少一次往返
   - **与教务对称**：`/school/jiaowu/cache/:dataType` 与 `/school/secondclass/cache/:dataType` 形状同构，客户端可复用处理逻辑
   - 出处：#60（已按此修订）、#64 讨论
4. **信封**：本仓 `{code,message}`+camelCase、成功不带 code（#63）——**不复制二课的 `content`**
5. **dataType 词表**（受控枚举 + `ParseDataType` 白名单）（#61）
6. **缓存键格式**：`nongyu:cache:<userID>:<dataType>`，不做共享键（#61）
7. **错误码表两张**（表 A 后端产生的 / 表 B 客户端本地态），**不为 `code=5` 定义码**（#63）
8. **脱敏字段白名单**（token/GPS/扫码时间戳/响应体全禁；配方只给槽位不给值）（#63）
9. **`code` 双类型处理口径**：后端不接触上游 `code`；客户端按「字符串或数字」判（#70 新增）
10. **设备头表达方式**（§五 Q4）

### ② 施工期可定（Spec 给区间与判据，数值实施时定）

1. **TTL 具体值**（活动 10~15 分钟 / 成绩类 6~12 小时 / 诚信值 30~60 分钟 / 规则 24 小时 —— 给区间，实施时定）
2. **`staleMaxAge` 与新鲜度窗口**（jiaowu 先例 7 天 / 60 秒，二课可调）
3. **schema 粒度**（每 dataType 一个 vs 按组一个）
4. **`schemaVersion` 带不带**（建议带，否则升级无法拒收旧格式）
5. **`audit action` 完整取值表**（已定 `secondclass.recipe_issue` / `secondclass.record`，扩展值实施时定）

### ③ 留后续 / 本期不做 → **进 Out of Scope，不进 pending**

1. **配方按角色分发**（组织者 vs 学生）——见 §五 Q3
2. C/D/E 三组全部（#59）

### ④ 需实测（Spec 必须标注「未验证」，不许写成已知）

| 项 | 现状 | Spec 怎么写 |
| --- | --- | --- |
| **空态 `content` 是 `[]` 还是 `null`** | #70 未测（账号有数据） | schema 用 `oneOf: [array, null]` 兼容；标「未验证」 |
| **分页响应总数字段名** | 未测 | 分页类 dataType 的 schema 暂不写死总数字段；标「未验证」 |
| **`code=5`（token 失效）真实形态** | 未测（#70 测的是「无 token」→ `6 用户未登录`） | 标「未验证」；且**本仓不定义该码**（#63 已定），风险由客户端承担 |
| **写操作副作用与重复提交** | 未测（避免污染真实数据） | 标「未验证」；幂等责任交客户端（#62） |
| **二维码 `qrCodeTimestamp` 时效 / GPS 校验规则** | 未测 | 标「未验证」；配方只给槽位 |
| **限流按账号还是 IP** | 未测 | 标「未验证」；不得声称已限流（#63 盲区措辞） |
| **活动详情运行时是否真带 `gid`/`groupName`** | 离线已知字段存在，运行时未验 | 标「离线已知、运行时未验证」 |

**④ 的写法铁律**：每项必须写「**已知什么 + 缺什么 + 缺的那部分用什么保守设计兜住**」，不能只写「待验证」。

---

## 三、Q3 测试两层接缝（照 #60 草图口径）

**① 纯函数单测**（无 IO）：
- 配方表完整性（61 个 `recipeId` 唯一、槽位合法、`origin` 取值合法）
- dataType 词表与 TTL 分组
- `go:embed` schema 的启动期 Compile
- **`code` 双类型解析**（字符串 `"0"` 与数字 `6` 都要能判）

**② HTTP 集成测**（fake `CacheStore` + fake `AuditStore`）：
- 读三类 cache 端点：命中直返 / 未命中回 `cache_miss` **且响应体内含配方**（这是与教务同构的关键断言）
- 写类 recipe 端点：未知 recipeId 400、写操作参数槽位 `origin` 正确
- 未认证 401 / 未知 recipeId 400 / 未知 dataType 400
- 回传体超 1 MiB 413
- schema 不合格 400 **且不写入**（旧数据仍可读）
- 命中直返、stale 回落

**只有一处用真 Redis**：TTL 与新鲜度窗口拆两个语义的那段行为。

**不进本仓测试接缝**：客户端直连二课那一段（后端零出站，测不到也不该测）、以及任何依赖真实二课响应的用例——**用 `testdata/secondclass/` 的夹具替代**。

---

## 四、Q4 不动清单：原样收录 #63 的 11 条

**逐条收录，一条不漏**（全文见 [决策-二课横切口径.md](决策-二课横切口径.md) §七）：

1. `publicPaths` **现为四条**，本 effort 不增不减
2. **`/status` 不在 `publicPaths`、不免 NY1 签名**；旧「五条含 /status」**已作废，不得写回**
3. `publicReadablePaths` 不新增条目
4. 两份清单共用同一个 `IsPublicPath`，不新增第三份
5. `IsAppSignExempt` 只豁免 `/healthz` 与 `/admin/*`，不加豁免
6. 不新增限流实现（沿用 `RateLimitMiddleware` 60/分钟进程内滑动窗口）
7. 中间件链顺序不变：`RequestID → NY1（适用时）→ 鉴权 → AdminOnly（仅 /admin）→ 限流`
8. 信封不动：错误 `apperr.ErrorResponse{code,message}`，**成功不带 `code`**，camelCase
9. `CtxUserID` 契约键与「只从上下文取身份」不动
10. 审计 `AuditEntry` 六字段不动，不加字段
11. `/status` 的 5 个字段不增不减（只是现在要签名+会话才能读）

> **⚠️ 与 #51 先例的差异（必须记录）**：#51 的不动清单第 1 条写「`/status` 保持公开且限流豁免」。**那条现在已作废**——`/status` 已于 2026-11-16 前的 0-day 修复中移出 `publicPaths` 且取消免签（`status.go:9-14`「安全契约（0-day 修复）」）。**照抄 #51 会把这个已修的安全问题写回去。** 本 effort 的不动清单以 #63 版为准。

---

## 五、Q5 交 `/to-spec` 的清单

`/to-spec` 应能据此直接产出 Spec，无需再问决策：

1. **文件**：`docs/specs/spec-学校二课接入.md`（一份）
2. **节序**：§一的十一节（第 11 节「不动清单」在 Further Notes 之后）
3. **素材来源**（全部已存在）：

| 素材 | 路径 | 用途 |
| --- | --- | --- |
| PRD | [prd-学校二课接入.md](../specs/prd-学校二课接入.md) | 范围、用户故事、验收标准 |
| 架构草图 | [架构落点草图-学校二课.md](架构落点草图-学校二课.md) | 域形状、三加一端点、降级、测试两层 |
| 凭据边界 | [决策-二课凭据边界与ADR-0001再修订.md](决策-二课凭据边界与ADR-0001再修订.md) | 零出站零凭据 |
| 范围 | [决策-二课范围切分与OutOfScope.md](决策-二课范围切分与OutOfScope.md) | 61 接口 + Out of Scope |
| 横切 | [决策-二课横切口径.md](决策-二课横切口径.md) | 信封/错误码/鉴权/盲区/NY1/脱敏/**不动清单 11 条** |
| 记录 | [决策-二课记录语义.md](决策-二课记录语义.md) | 两条记录线、dataType、TTL、缓存键 |
| 配方 | [决策-二课写类配方契约.md](决策-二课写类配方契约.md) | 七字段、槽位、多跳、14 写操作 |
| 术语 | [GLOSSARY.md](../../GLOSSARY.md) 第 ④ 段 | 统一用词 |
| **实测夹具** | [testdata/secondclass/](../../testdata/secondclass/README.md) | **`content` 信封、`code` 双类型、7 设备头** |
| map fog | #58 的 Not yet specified | 按 §二 分四层归位 |

4. **硬约束**：
   - ① 阻塞项**必须写完**，Spec 里不留 TBD
   - ② 施工期项**必须标「实施时定 + 依据」**
   - ③ 不做项**必须进 Out of Scope**（以 map #58 版为准，比 PRD 摘录全）
   - ④ 需实测项**必须显式标注「未验证」+ 保守设计**
   - **不动清单必须完整（11 条）**
   - **第 7 节必须存在**（一手实测与静态分析的差异）
5. **本票不写 Spec 本体**——那是 `/to-spec`。

---

## 六、地图走完判据

逐张读回子票状态（不只信统计数字）：`deck_map_snapshot` 应回 **frontier=0、blocked=0、indeterminate=0**，12 张子票全关。

**无未决 fog 需再开票**：剩余 fog 全部归入 ④ 需实测 或 ③ Out of Scope，**不再需要新票**（它们不阻塞 Spec 产出）。

**走交棒，不写「## 探索结论」**：与本 effort 的四张已关 map 一致——地图关闭后由维护者点详情页「完成」做最终核对。

---

## 七、本票未裁定 / 留后续

- **Spec 内各能力的章节顺序**：按 PRD 顺序还是按依赖顺序——交 `/to-spec` 时定，不影响骨架。
- **④ 需实测项的保守设计怎么写**：`oneOf` 还是「字段可选」——交 `/to-spec` 时按具体 dataType 定。
- **是否把 `testdata/secondclass/` 的样例 JSON 嵌进 Spec**：倾向只引用路径不嵌全文（Spec 已够长）。

## 八、本票不做的事

- **不写 Spec 文件本体**（那是 `/to-spec`）。
- 不重开任何已关决议的语义。
- 不产代码、不建表、不改路由、不发 PR。

## 九、证据

`docs/specs/` 清单实查 3 份（194/327/455 行，一 effort 一份）；[决策-Spec收口方式.md](决策-Spec收口方式.md) 85 行逐行读（#51 先例）；[spec-教务查询与缓存.md](../specs/spec-教务查询与缓存.md) 节结构 `grep "^## "` 实查；#63 不动清单 11 条原文 `sed` 逐条读；[testdata/secondclass/README.md](../../testdata/secondclass/README.md)（#70 实测，commit `804afbb`）；map #58 的 Not yet specified 与 Out of Scope 段；`README.md:77-80`（SDD 顺序）。
