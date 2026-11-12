# 决议：Spec 的章节结构与验收边界（本 effort 收口方式）

> 来源：[决议：Spec 的章节结构与验收边界（收口方式）](https://github.com/NongyuSystem/nongyu-backend-plus/issues/51)（wayfinder map #43 **收口票**，本图最后一张），2026-11-12 收口（HITL grilling，Q1~Q5 全 A）。
> 性质：**决策记录**，不是 Spec 本体。本票定的是「Spec 怎么组织」，Spec 文件由 `/to-spec` 产出。

## 前置（已锁，不在本票重开）

- **PRD 已存在**：`README.md:80` 要求 PRD 先于 Spec；#47 Q1 判「另立新 PRD」，#56 已产出 [PRD-六类常见功能.md](../specs/prd-六类常见功能.md)（270 行九节，`fefced6`）。→ **本票不重开「要不要补 PRD」**。
- **八份决策笔记**：[公告](决策-公告域模型与作用域.md)、[帖子](决策-帖子域模型与契约.md)、[消息](决策-消息域模型.md)、[埋点](决策-用户行为埋点.md)、[横切鉴权](决策-横切鉴权口径.md)、[范围切分](决策-六功能范围切分.md)、[通知派生](决策-通知派生规则.md)、[MinIO 落点](决策-MinIO接入落点.md)。
- **架构形状**：[架构落点草图-六类常见功能.md](架构落点草图-六类常见功能.md) @ `2be2297`（四域 `content/`(post+announcement)、`message/`、`track/`；`admin/` 不设域；`platform` 只加 `ObjectStore`）。
- **仓内 Spec 惯例**：`docs/specs/` 现有 2 份，**一个 effort 一份**（`spec-身份域与平台底座.md`、`spec-教务查询与缓存.md` 240 行），**无拆多份先例**。
- **既有设施已定不动**（#46 Q5）：`/status` 公开且限流豁免、不新增 `RequestIDMiddleware`/限流实现、信封 `{code:string, message}`+camelCase、中间件链 `RequestID→NY1(适用时)→鉴权→AdminOnly(仅 /admin)→限流`。

## 决议（Q1~Q5 全 A）

**Q1 一份 Spec**：`docs/specs/spec-六类常见功能.md`，六个能力各一节，**另设「横切口径」节**供各节引用。
- 理由：① 仓内先例是「一个 effort 一份」；② 鉴权／信封／审计／脱敏四域共享，拆分要重复三遍、改一次改三处；③ **帖子→通知→消息的派生链路必须在一份里才看得见**，拆开后端到端验收无处落。
- 已记录代价：文档会长（预估 400~500 行），用「横切节 + 各节引用」控制重复。
- 否决：按域拆三份；PRD 与 Spec 合并（破坏 SDD 分层）。

**Q2 章节骨架**（沿用 `spec-教务查询与缓存.md` 范本 + 一节增补）：
1. Problem Statement（六能力各自问题，引 PRD 不重写）
2. Solution（四域形状，引架构草图 @ `2be2297`）
3. **横切口径**（鉴权／信封／审计／脱敏／NY1 分层——供下文引用）
4. User Stories **按能力分组**（公告／帖子／消息／埋点／管理台／文件存储，各 3~5 条）
5. 各能力的 Decisions（引八份决策笔记，不重写）
6. **数据契约**（端点、字段、状态机、对象 key——**这是 Spec 与 PRD 的真正分界**）
7. Testing Decisions（两层接缝，见 Q4）
8. Out of Scope
9. Further Notes（pending 四层 + 依赖策略 + 已知边界）
10. **与既有设施的关系（不动清单）**（见 Q5）
- 否决：按域分章（`admin/` 不设域，`/admin/*` 无处安放）；只写决策索引（与 PRD 无差别）。

**Q3 pending 分四层**（本票最实用的一处）：
- **① 阻塞开工**（Spec 必须定死，不定不能写代码）：字段名／表结构／索引／端点路径与请求响应形状／状态机取值／对象 key 模板。→ **写进 Spec 正文，不留 TBD**。
- **② 施工期可定**（Spec 给区间与判据，数值实施时定）：TTL 值、保留期 N、通知队列容量／flush 周期／批量、`publish_revision` 生成规则、`Delete` 重试策略、`audit_log.action` 完整取值表、超管/管理员权限矩阵。→ **列为 pending，标注「实施时定 + 依据」**。
- **③ 留后续 effort / 本期不做**：推送通道、内部信防骚扰、阅读量与覆盖率统计、图片服务端二次处理、管理端 CSRF/会话固定、强制下线他人会话。→ **写进 Out of Scope，不进 pending**。
- **④ 需实测**：**MinIO 预签名行为**（#53 Q6 已明说未验证）——标「**依据 AWS S3 文档，未在 MinIO 实测**」，**不许写成已知**。
- 否决：全列一个列表（无法区分阻塞与可并行）；只写阻塞项（Spec 出现 TBD 空洞）。

**Q4 测试两层接缝**：
- **① 纯函数单测**（无 IO）：可见性判定、受众过滤、幂等键组装、脱敏规则（顶层白名单 + props 敏感键剔除）、`props` 形状校验。
- **② HTTP 集成测**：`httptest` 打路由断言状态码与响应体，repo 用 fake。
- **只有两处用真实外部依赖**：**PG 的 `(recipient_id,kind,source_domain,source_object_id)` 部分唯一索引 upsert 行为**、**MinIO 预签名读写**（后者即 Q3 的「需实测」项）。
- **客户端契约不进 Spec**：由「数据契约」节给出端点与字段；客户端 SDK 不在本仓。
- 否决：三层含真实全链（CI 要起 PG+Redis+MinIO，贵且不稳）；只做 HTTP 层（纯逻辑失去快速回归）。

**Q5 专设「不动清单」一节**：逐条声明**本 effort 明确选择不动**——
- `/status` 保持公开且限流豁免（限流仍只豁免 `/healthz`）；
- **不新增** `RequestIDMiddleware`／限流实现；
- 响应信封统一 `{code:string, message}` + camelCase（**不照抄旧 Track 的 `{ok,data}`**）；
- 中间件链顺序 `RequestID → NY1(适用时) → 鉴权 → AdminOnly(仅 /admin) → 限流`；
- `publicPaths` **不被业务端点污染**（公开读另立 `publicReadablePaths`，**共用同一 `IsPublicPath`**）。
- 理由：用「不动清单」而非散写，将来有人想改这些时能一眼看到「这是本 effort 明确选择不动的」。
- 否决：散写在各节（无人能一眼看出）；不写（实施者可能「顺手统一」信封或加限流，破坏 #46）。

## 交 `/to-spec` 的清单（本票的最直接产物）

`/to-spec` 应能据此直接产出 `docs/specs/spec-六类常见功能.md`，无需再问决策：

1. **文件**：一份 `docs/specs/spec-六类常见功能.md`。
2. **节序**：按 Q2 的十节（第 10 节「不动清单」在 Further Notes 之后）。
3. **素材来源**（全部已存在，无需再查）：
   - PRD：[prd-六类常见功能.md](../specs/prd-六类常见功能.md)（范围、用户故事、验收标准）
   - 架构：[架构落点草图-六类常见功能.md](架构落点草图-六类常见功能.md) @ `2be2297`
   - 八份决策笔记（见「前置」）
   - map #43 的 Not yet specified（30 条，按 Q3 分四层归位）
4. **硬约束**：Q3 的①必须写完、④必须标注未实测；Q5 的不动清单必须完整；Out of Scope 以 map #43 的 Out of Scope 段为准（30+ 条，**比 PRD 摘录的 13 条更全**，Spec 用 map 版）。

## 本票未裁定 / 留 fog

- **Spec 内六能力的章节顺序**：按 PRD 顺序还是按依赖顺序（先内容域再消息域）——交 `/to-spec` 时定，不影响骨架。
- **fog 分层对照表是否附在 Spec 末尾**：倾向附（便于实施者查），但不强求。
- **Spec 的 Out of Scope 与 PRD 的 13 条是否逐字一致**：**不一致**——PRD 摘录 13 条，map 有 30+ 条；**Spec 以 map 版为准**（更全），PRD 的 13 条是其子集。
- **六功能的实施顺序与分批**：本票不排期，交 `/to-spec` 后的实施计划。

## 本票不做的事

- **不写 Spec 文件本身**（那是 `/to-spec`）——本票只定骨架与边界，与前三张已关 map 的收口票同例。
- 不重开任何已关决议的语义。
- 不产代码、不建表、不改路由。

## 证据

`docs/specs/` 目录清单（2 份 Spec，均为一 effort 一份）；[spec-教务查询与缓存.md](../specs/spec-教务查询与缓存.md) 的章节骨架（240 行，Problem Statement → … → Testing Decisions → Out of Scope → Further Notes）；[prd-六类常见功能.md](../specs/prd-六类常见功能.md)（`fefced6`）；[架构落点草图-六类常见功能.md](架构落点草图-六类常见功能.md) @ `2be2297`；八份决策笔记；`README.md:79-80`（SDD 顺序）；票 #51 的票评与 resolution comment；map #43 的 Not yet specified（30 条）与 Out of Scope（30+ 条）。
