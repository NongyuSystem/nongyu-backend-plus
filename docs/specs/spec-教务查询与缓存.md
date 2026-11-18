# 教务查询与缓存：前端解析、后端缓存

> 状态：**已实现**（2026-11-11；2026-11-16 修订 cache_miss recipe）。决策来源见 [地图 #30](https://github.com/NongyuSystem/nongyu-backend-plus/issues/30) 及其 12 张子票；
> 逐条决策的完整依据住在各 decision ticket 里，本文只做汇总与落地说明。
>
> **2026-11-16 修订**：未命中不再只下发 WebVPN 编码地址。`cache_miss` 下发取数 recipe（地址 + 方法 + 解析器名 + 会话 Cookie）。tgt 不下发。解析仍在客户端，POST 仍收解析后的 JSON。见文末「2026-11-16 推翻了什么」。
>
> **2026-11-17 修订**：地址形态改由客户端声明。请求头 `X-Nongyu-Campus-Network: direct` 表示「客户端能直连校园网」，此时下发直连地址并换出 `ASPSESSIONID`；该头缺失或为 `vpn`/`0`/`false` 时下发 WebVPN 编码地址，且**不**出站换票（网关按 vpn ticket 自己补上游 Cookie）。判定权在客户端：教务站只在校内可达，后端出口位置推不出学生位置。
>
> 术语遵循仓库 `GLOSSARY.md`：**农屿会话**（自有 token）/ **票据失效**（WebVPN 网关 302→/login）/
> **教务会话失效**（教务 ASPSESSIONID，正文「登录超时」）。三者不可混用。

## Problem Statement

学生要在校外查课表、成绩、考试安排、学业进度、排名、培养方案、学籍、通知等教务数据，但：

1. 教务站只在校内网可达，校外必须经 WebVPN 网关；
2. 教务站需要登录，且会话失效时返回的仍是 HTTP 200（正文 `alert('登录超时')`），状态码不可信；
3. 现有的旧实现把「取数 + 解析 HTML」都放在后端，受 ADR-0001 的客户端直连边界限制，也让后端的观测与限流失去了着力点。

## Solution

**解析在客户端、缓存在服务端。** 分三段：

1. **无缓存**：前端调 `GET /school/address?target=jiaowu&path=<教务路径>` 拿到经网关编码的地址 →
   自己直连教务取 HTML → 自己解析成结构化 JSON → `POST /school/jiaowu/cache/:dataType` 回传；
2. **后端校验**：只做 schema/形状校验（JSON Schema 2020-12）。不合格回 `400 schema_rejected`，
   且**一个字节都不写**，保证坏数据永远顶不掉能读的旧数据；
3. **有缓存**：前端直接 `GET /school/jiaowu/cache/:dataType` 命中返回，不再打教务；未命中回
   `200 + code=cache_miss`，并下发该类型的取数 recipe：地址形态、方法、解析器名、会话 Cookie。
   地址形态由请求头 `X-Nongyu-Campus-Network` 声明——
   `direct` 给直连教务地址 + `ASPSESSIONID`；缺省/`vpn` 给 WebVPN 编码地址且不换票。

后端不解析 HTML，也不代取业务页。它认识已写进契约的固定 path（例如课表的 `title_id1=4`），
用来拼地址。需要用户输入才能确定的 path（教室名、教师编号）不编。

## User Stories

**取数**

1. 作为学生，我想查本学期课表，这样我知道这门课在哪上、什么时候上。
2. 作为学生，我想查成绩与绩点，这样我能确认自己的学业状况。
3. 作为学生，我想查考试安排（含考场与座位），这样我能提前安排复习与出行。
4. 作为学生，我想查加权排名，这样我知道自己在专业里的位置。
5. 作为学生，我想查学业进度，这样我知道还差多少学分毕业。
6. 作为学生，我想查培养方案，这样我知道后面还有哪些课要修。
7. 作为学生，我想查学籍信息，这样我能确认专业、班级、年级等是否准确。
8. 作为学生，我想查教学通知与竞赛通知，这样我不会错过报名与截止日期。
9. 作为学生，我想按教室或教师查课表，这样我知道某间教室或某位老师这学期的安排。
10. 作为学生，我想查开课目录，这样我知道这学期开了哪些课。

**回传与校验**

11. 作为学生，我的客户端解析完数据回传时，如果格式不对，我想被明确告知「这次数据没被接受」，而不是让坏数据静默覆盖我还看得到的旧数据。
12. 作为学生，我不想让客户端自报的「成功标志」决定数据能不能进缓存——历史实现里它把页面上的「特别提示」当成过课程名。
13. 作为学生，我传的数据如果超过大小上限，我想收到明确拒绝，而不是等到服务端撑不住。
14. 作为学生，我不想让别人的数据出现在我的查询结果里，也不想我的数据被别人看到。

**缓存与 stale**

15. 作为学生，我想在教务改版导致解析失败时仍能看到上一次的数据，并明确知道「这可能不是最新的」。
16. 作为学生，我不想看到去年甚至更早的课表被当成当前数据——超过七天的旧数据应当报错而不是骗我。
17. 作为学生，我这学期真的一门课都没有时，我想看到「无数据」的空列表，而不是一个错误。
18. 作为学生，我重复查同样的东西时，我希望直接拿到缓存，不必每次都等教务响应。

**刷新与配额**

19. 作为学生，我想主动让缓存失效以取最新数据，这样我刚考完试就能看到新成绩。
20. 作为学生，我连续点刷新时，我想被合理节流，而不是让我的账号把教务站打挂。
21. 作为学生，我刷新被拒时，我想知道还要等多少秒。

**运维与安全**

22. 作为开发者，我想在不配 Redis 的本地环境里也能启动服务，并让缓存端点明确回「未启用」而不是崩溃。
23. 作为开发者，如果某份 schema 写错了，我想在启动时就知道，而不是等到线上第一批脏数据进缓存。
24. 作为开发者，我想让缓存键天然按用户隔离，这样我不必在每处都记得加用户维度。

## Implementation Decisions

### 端点集

只保留两类通用端点，**不为 14 个功能各建语义端点**（decision ticket #37）：

| 端点 | 方法 | 说明 |
| --- | --- | --- |
| `/school/address` | GET | 下发经 WebVPN 编码的地址，支持任意白名单 `path` + query |
| `/school/jiaowu/cache/:dataType` | GET | 命中直返；未命中 `200 + code=cache_miss`，附直连 recipe |
| `/school/jiaowu/cache/:dataType` | POST | 回传解析后的 JSON，schema 校验后写入 |
| `/school/jiaowu/cache/:dataType` | DELETE | 缓存失效，60 秒间隔 |

不做语义端点的理由：`Service.Address` 已支持任意白名单路径，`/school/jiaowu/cache` 承载所有数据类型，
再包一层 `/school/scores` 之类只是把 path 拼装挪个地方，还让教务改路径变成后端改动。

### dataType 受控词表

15 个名字（不是早期文档说的 14 个 —— `kaike` 是第 15 个）：

`course` `score` `exam` `progress` `rank` `plan` `personal` `notice` `teachingNotice`
`competition` `classroomInfo` `teacherInfo` `teacherCourse` `classroomCourse` `kaike`

任何来自 URL 的字符串都必须过白名单校验；未知名返回 `400 target_denied`，
绝不把用户输入当缓存键的一段拼进去。

### 缓存语义表

| 项 | 取值 |
| --- | --- |
| 键 | `nongyu:cache:<userID>:<dataType>`（只一种键型、**不带学期**） |
| TTL（新鲜度窗口） | 通知/竞赛 **10 分钟**；考试 **30 分钟**；其余 11 类 **1 小时** |
| stale 触发方 | **客户端上报失败**后带 `fallback=1` 再取 |
| stale 硬上限 | **7 天**，超期一律按未命中 |
| 业务空值 | 当**合法 payload** 存（如 `payload: []`），不使用 sentinel |
| 校验失败 | **不写缓存、不动旧缓存** |
| 失效 | 只删本用户该类型键，60 秒间隔，**不级联** |

**一处必须显式说明的实现取舍**：TTL（10 分钟 / 1 小时）与 stale 硬上限（7 天）如果都当 Redis 过期时间，
那么数据在 10 分钟后就物理消失，「7 天上限」永远触发不到，假期间回来一定读不到旧课表。
因此把它们拆成两个语义：

- **Redis TTL** = 7 天 + 1 天缓冲（承载「再旧就不给」）；
- **新鲜度窗口** = 每类的 TTL（决定正常 GET 是否判 miss）。

于是正常 GET 超过窗口即 `cache_miss`（让客户端去取新的），而带 `fallback=1` 的请求仍能拿到
7 天内的旧数据。两个数字都真正生效，对外语义与决策一致。

### 回传信封与错误码

信封（三次决策叠加后的最终形状）：

```json
{
  "payload": [ ... ],
  "success": true,
  "schemaVersion": 1,
  "fetchedAt": "2026-11-11T03:11:07Z",
  "stale": false
}
```

- `payload` 可能是**数组、对象或 null**（15 类里三种形状都有），所以按原始 JSON 透出，不做二次编解码；
- `success` **只记审计，不作准入**：准入完全由 payload 形状决定。历史上 `success` 有实测假阳性
  （空壳页的红字「特别提示」被当成课程名且 `success: true`）；
- `fetchedAt` 由**服务端**盖章，不采信客户端时间——否则改系统时间就能绕过 7 天上限。

错误码分两张表（客户端要据此决定用户动作）：

| 场景 | HTTP | code |
| --- | --- | --- |
| 未命中 | 200 | `cache_miss`（附 recipe；`session=unavailable` 仍是这条，不是 500） |
| 回传形状不合格 | 400 | `schema_rejected` |
| dataType 不在白名单 | 400 | `target_denied` |
| 缺认证身份 | 401 | `unauthorized` |
| 回传体超 1 MiB | 413 | `payload_too_large` |
| 刷新过于频繁 | 429 | `refresh_too_soon`（带 `Retry-After`） |
| 缓存层未配置 | 503 | `cache_unavailable` |

`schema_rejected` 的响应只给统一文案，**不回**具体哪个字段不符合——那是内部实现细节。

### 依赖与校验载体

- JSON Schema 库：`github.com/santhosh-tekuri/jsonschema/v6` v6.0.3，Draft 2020-12，无 CGO；
- 15 份 schema 放在 `internal/school/jiaowu/schema/`，用 `go:embed` 打进二进制；
- **启动时一次性编译**，任何一份编译失败就整体不挂缓存路由（而不是带着坏 schema 上线）；
- 形状分三层：能用 `pattern` 表达的进 schema；跨字段/枚举类进 schema 的 `enum`/`anyOf`；
  需要业务语义的留在 Go 侧（本批暂无第三层需求）。

## Testing Decisions

**什么算好的测试**：只断言外部可观察行为——HTTP 状态码、响应体字段、是否产生了上游请求，
不断言内部函数调用顺序或私有字段。

**两层接缝**（decision ticket #36 定的）：

1. **schema 校验器纯函数单测**：把 15 类的好样例与坏样例直接喂给校验器，不启 HTTP。
   这是这道闸的核心——每类至少一个坏样例必须被拒。
2. **HTTP 层集成测**：`httptest` 走完「未命中 → 回传 → 命中 → 坏数据被拒 → 失效」全链。
   缓存用进程内假实现，测试不需要起 Redis。

**另外补了一层真实依赖验证**：`TryAcquire` 的原子性（SET NX）与到期放开用真实 Redis 验，
内存假实现覆盖不到这两点。该用例在未设置 `REDIS_TEST_ADDR` 时自动跳过。

**覆盖的关键行为**：

- 命中路径**零出站**（用计数的 mock doer 断言）；
- 坏数据被拒后**旧缓存仍在**；
- `stale=true` **只在** `fallback=1` 时出现，正常 GET 超过窗口就 `cache_miss`；
- 7 天硬上限：造一条 8 天前的缓存，`fallback=1` 也必须 `cache_miss`；
- 失效 60 秒内二次请求回 429 且带 `Retry-After`，推进 61 秒后恢复；
- 失效**不级联**到其它 dataType；
- 用户隔离：A 写的缓存 B 读不到；
- `success=false` 但形状合法 → 接受；`success=true` 但形状不对 → 拒绝（证明准入看形状）；
- 未配缓存时回 503 而非崩溃。

**先例**：`internal/school/jiaowu/handler_test.go`（httptest + gin 的行为断言）、
`internal/platform/cache_test.go`（缓存层语义）。

## 解析器契约附录（给前端）

后端不解析 HTML。本节把前端要用的契约收在一处，方便查阅。

**逐字段契约**：

- `docs/tech/课程表与成绩查询可重写契约清单.md` —— 14 模块 URL / 返回 / 失败形状 + course/score 逐字段
- `docs/tech/解析器施工级补充-4个待定解析器.md` —— plan / progress / exam / rank 施工级契约
- `docs/tech/14类回传JSON字段形状清单.md` —— 15 个名字的顶层形状分类与逐类字段表

**真实页面实证（最高证据等级）**：`docs/tech/实测-教务6页真实HTML与表头.md`

- 课表表头是 **`任课教师`**（不是「任课老师」）；
- 课表页**没有学期下拉框，`?term=` 参数不存在**，学期是纯文本「当前学期状态：2026-2027-1」；
- 成绩页**没有分页**，一次返回全部；
- 成绩表头是 **`最终成绩`**，而泛化的 `成绩` 出现次数更多 —— **关键字优先级必须把 `最终成绩` 放在 `成绩` 之前**，否则会取错列；
- 考试页真实存在 **`考场`** / **`座位`** 两列，已收进 schema（`examRoom` / `seatNumber`）。

**回归夹具**：`testdata/jiaowu/`（6 份真实页 + 3 份手工 + README）。
脱敏口径是「结构保留 + 身份占位」；只保留 UTF-8（GBK 解码路径的补偿做法见 README）。

## 2026-11-16 推翻了什么

未命中响应从「编码地址」改成 recipe：

```json
{
  "code": "cache_miss",
  "target": "jiaowu",
  "address": "https://jiaowu.sicau.edu.cn/xuesheng/gongxuan/gongxuan/kbbanji.asp?title_id1=4",
  "method": "GET",
  "cookies": [{ "name": "ASPSESSIONID…", "value": "…" }],
  "parser": "jiaowu-course-v1",
  "session": "ok"
}
```

推翻的：

- miss 只下发 WebVPN 编码地址。现在 `address` 是直连教务 URL，校外打不进。这是下发 `ASPSESSIONID` 的代价：那张 Cookie 只对 `jiaowu.sicau.edu.cn` 有效，贴到编码地址上不会登录。
- 后端不认识固定 path。已写明的 path 由后端拼进 `address`。教室/教师这类要用户输入的 path 仍不下发，避免编假地址。培养方案只给第一跳 `jjihua.asp?title_id1=1`，不编不能静态化的隐藏域。
- Cookie 只在客户端罐里。miss 时后端用已保管的 `go-admin-tgt` 换票、走教务域内三跳，把 `ASPSESSIONID*` 交给本人。tgt 不下发。没有 tgt 或建会话失败时仍是 200，`cookies` 为空，`session=unavailable`。Cookie 值不进日志、不进审计。
- ADR-0001 第 5 条「不回显非必要 Set-Cookie」在这一条路径上放宽为：可回显给本人的教务会话 Cookie。见该 ADR 的 2026-11-16 修订。

没有推翻的：

- 解析仍在客户端。`parser` 只是名字（如 `jiaowu-course-v1`），本轮不实现 HTML 解析器。
- `POST /school/jiaowu/cache/:dataType` 仍收解析后的 JSON，仍进现有缓存，不新建表。
- `GET /school/address` 仍返回编码地址、零出站，只追加 `method=GET`，不附 `ASPSESSIONID`。
- 命中路径零出站，不建会话。
- 后端不代取课表 HTML 作为主路径。

## 2026-11-17 地址形态分流

`cache_miss` 现在读请求头 `X-Nongyu-Campus-Network` 决定下发哪种地址：

| 请求头取值 | mode | address | cookies | session |
| --- | --- | --- | --- | --- |
| `direct` / `campus` / `intranet` / `1` / `true` | `direct` | `https://jiaowu.sicau.edu.cn/...` | `ASPSESSIONID*` | `ok` / `unavailable` |
| `vpn` / `webvpn` / `0` / `false`，或缺省、不可识别 | `vpn` | `https://webvpn.sicau.edu.cn/https/<KEY_HEX><密文>/...` | `wengine_vpn_ticket` | `ok` / `unavailable` |

**两种形态完全同构**：`cache_miss` 给出配给该 `address` 的 cookie，客户端/代取链路拿
`address + cookies` 去取数，自己解析回传。区别只在地址与 cookie 的内容。

### 2026-11-18 定案：vpn 形态必须下发网关票

模型：**客户端什么都没有、全在后端取**。客户端不碰网关地址、不碰任何 cookie 罐。
`wengine_vpn_ticket` 由客户端登录网关后 `POST /school/webvpn-session` 交来，后端按用户
加密保管（`webvpn:` 命名空间）；`cache_miss` 的 vpn 分支从保管库取票放进 `cookies`，
`session` 与 direct 一致用 `ok`/`unavailable`。

之前有一版把 `cookies` 恒置空、额外回 `session=client_side` 和一套 `webvpn.status`
状态字段——那是基于「客户端自己拿 WebView 罐直连」的错误前提，且后端零出站根本
验证不了网关侧票是否有效，状态恒为 `missing` 会误导弹窗。该版已删除。

**弹窗信号**：客户端自己判 302。带票请求编码地址时被 302 回 `webvpn.sicau.edu.cn/login`
即票失效，需重新登录网关并重交票。后端不造零出站看不出的状态字段。

**失效哨兵**：`/fetch` 判死票时写哨兵而非删记录，避免拿死票反复打学校站；
哨兵不下发成 cookie（无票即 `cookies=[]`、`session=unavailable`），重新交票即覆盖。

**实测边界**：网关票只够过网关（公开页 200）。课表等需登录页还要教务自己的
CAS 会话（`go-admin-tgt` 换教务 st → 回投编码后的 `caslogin.asp`），这一步由后端
代取链路完成，不是单纯「带 cookie」能解决的。后端不声称 `cookies` 里的票保证放行。

为什么后端不自造网关票：2026-11-17 实测确认，`wengine_vpn_ticket` 只有在网关**服务端已登记
该会话为已登录**时才有效。后端用 tgt 走门户 appid 换票并回投网关，只能拿到 16 位游客票
（回投链停在 `/wui/#/` 的 fragment 上，fragment 不进 HTTP，剩余会话绑定全在门户 SPA 里），
拿这张票打任何编码地址都 302 回 `/login`。因此票只能由客户端真实登录后交来。

为什么判定权在客户端：教务站只在校内网可达，而「这个学生此刻在校内还是校外」只有客户端知道。
后端出口在校内也推不出学生位置，所以不做后端探测，也不复用 `/school/presence` 的持久化上报
（那个端点仍在，用于「进入/切换网络时上报」这类更重的场景）。

不可识别/缺省回落 `vpn`，不回落 `direct`：PRD 前提是「离校后必须经网关」，按校外处理不会让
常见用户拿到一个必然失败的地址。

编码地址的拼法是「先拼完整直连 URL 再 Encode」，不能拿 `tgt.Vpn` 直接接 path——
`Encode` 对无 path 的地址会补 `/`，会拼出 `...e0//xuesheng/...` 双斜杠。

## 2026-11-17 实测：三票分工

三种票各管一段，互不可替代（全部真账号实测）：

| 票 | 来源 | 能做什么 | 不能做什么 |
| --- | --- | --- | --- |
| `go-admin-tgt`（`auth` 域） | 客户端登录统一身份认证后交后端 | 换任意 appid 的 `st`：教务 st（`code=200`）、门户 st、学工 token | 直接访问编码地址（302） |
| `wengine_vpn_ticket`（`webvpn` 域） | 客户端真实登录网关后自带 | 过网关；网关按需补出 `ASPSESSIONID` | 去 auth 换 st（`code=500 未登录`） |
| `ASPSESSIONID`（教务） | 网关按需吐出 | 直连教务（校内）或经网关访问教务（校外） | 单独过网关（302） |

两条链路的实测结论：

- **校内**：`go-admin-tgt` → 换教务 st → 回投 `jiaowu.sicau.edu.cn` → `ASPSESSIONID` → 直连。
  换 st 一步只打 `auth`（已验证 `code=200`）；回投要求后端能到 `jiaowu`。
- **校外（后端代理也不行，走客户端）**：客户端登录网关 → 自带 `wengine_vpn_ticket` →
  直接访问编码地址。若需登录页（课表/成绩/学籍），客户端还要在自己 WebView 里完成一次
  教务 CAS（网关会推动），或由客户端把 tgt 交后端另行处理。

**票长不能判断有效性**：有效票与游客票都是 16 位十六进制（如 `43d5b3947913eab2`，8 字节）。
网关判断登录与否看的是服务端会话记录，不是票里的内容。

## Out of Scope

- Agent 的课表/成绩 tool 改造。
- 教务数据入库与历史留存（纯缓存，不做趋势分析）。
- 后端代取 HTML 作为主路径（`Service.Fetch` 仅保留为逃生口）。
- 后端对回传 JSON 做真伪复算 / 防伪造。
- `success` 作为准入门槛。
- 全局回源并发上限（后端不回源，该对象不存在）。
- `session_expired` 由后端产出（它是客户端本地错误态）。
- 为 14 个功能各建语义端点。
- 用 `emptyMarker` 承载业务空值（`Get` 命中 marker 返回 `nil, nil`，与键不存在不可区分）。
- `?term=` 学期参数与历史学期课表（实测不存在；历史学期走另一页面，属新功能）。
- Go 后端消费 HTML 夹具、以及 HTML 解析器的纯函数单测（属前端仓）。

## Further Notes

**已确认，不要重复验证**：`?term=` 不存在；真实表头文字已坐实（`任课教师` / `最终成绩` / `考场` / `座位` / `加权` / `排名`）。

**架构边界**：ADR-0001:28/:43 —— 后端**观测不到**前端直连教务那一段，因此**也限不住前端打教务的频率**。
本 Spec 不声称全链路已限流；60 秒失效间隔只约束「让缓存失效」这个后端动作。

**待验证项**（施工期补）：

1. **真实凭据端到端**：用真账号走一次「登录 → 取页 → 回传 → 命中」全链（本批的 e2e 用真实 Redis
   与真实 HTTP 进程跑通了缓存链，但取数段用的是假凭据）。
2. **培养方案页 `PLAN_FIXED_PARAMS` 跨用户可重放性**：实测只采了 6 页，未含此页。
3. **教室/教师公开查询页真实结构**：目前用手工夹具覆盖，真实页未采。

**依赖策略**：见根 `AGENTS.md`。本 Spec 引入的 `jsonschema/v6` 若将来被替换或移除，须按该策略的
双确认判据（`go mod tidy` + `go list -deps`）删除并写进提交说明。
