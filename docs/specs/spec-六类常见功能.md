# Spec：六类常见功能（公告 / 帖子 / 站内消息 / 用户行为埋点 / 管理台 API / 文件存储）

> 状态：**待实施**（2026-11-12）。本文是 SDD 流程 `PRD → Spec` 的 Spec 层，**待实施计划**。
>
> **依据**：[PRD-六类常见功能.md](prd-六类常见功能.md)（`fefced6`）／[架构落点草图-六类常见功能.md](../tech/架构落点草图-六类常见功能.md) @ `2be2297`（prototype，已获维护者逐项点头）／[决策-Spec收口方式.md](../tech/决策-Spec收口方式.md)（`be04dee`，定本文骨架与边界）／八份域决策笔记（本文引用不重写）。
>
> **跟踪器**：本文对应的 tracker issue 为 [#57](https://github.com/NongyuSystem/nongyu-backend-plus/issues/57)（标签 `ready-for-agent`）。**仓库文件是权威版本**，issue 只放索引与摘要，避免两处并行漂移。
>
> **术语**一律遵循 [GLOSSARY.md](../../GLOSSARY.md)（六功能词条见 `:67-119`）：**站内消息**（总称）／**通知**（系统或他人动作派生）／**内部信**（用户主动发）／**帖子**／**公告**／**用户行为埋点**／**运行日志**／**审计流水**／**管理台（B 端管理）**。行文不用「消息中心」「广场」等非正名。
>
> **本文与 PRD 的分界**：PRD 只回答「用户能看见什么算成」；本文回答「**怎么做、接口长什么样、测哪些行为**」。字段、索引、端点形状、状态机取值是本文的职责。

## Problem Statement

六类功能（公告、帖子、站内消息、用户行为埋点、管理台后端 API、帖子附件文件存储）在旧农屿里都有实现，但都散落在旧 monorepo 中，且各有结构性缺陷：

1. **公告**在旧仓只是帖子的一个类型取值，没有独立的读者范围、有效期与撤回语义。
2. **帖子**缺少统一的回复/点赞/收藏与编辑窗口约束；附件上限只存在于 PRD 提案，没有后端强制的落点。
3. **通知**是「前台 30 秒轮询 + `post_replies` 行上的布尔位」，收件人恒为帖主，无法表达多收件人与 per-user 已读；**内部信**在旧仓根本不存在。
4. **用户行为埋点**依赖一个已下线的旧 Go 服务，且那套服务用与业务 API 不同的响应信封、队列满时返回 503（会把重试风暴放大成自伤）。
5. **管理台**后端能力散在各处，鉴权口径与身份域不一致（旧 Admin JWT 永不过期）。
6. **对象存储**没有任何接缝；若直接在业务域里 import S3 SDK，会破坏「业务域只依赖 platform 接口」的架构约束。

同时，本仓现行 PRD 明文把「鉴权、会话、内容、广场、埋点服务、Web 管理端」列为**不包含**——这六类功能是一个与 WebVPN 迁移**并行**的 effort，需要自己的 PRD 与 Spec（PRD 已产出）。

## Solution

**四个顶层业务域 + 一个装配层薄单元 + 一个 platform 接缝**，全部在本仓内，不新增独立服务、不新增数据库：

- `content/`（父包）下两个子域：`post/`（帖子）、`announcement/`（公告）——共享可见性判定与受众过滤放在父包，两子域互不 import。
- `message/`：通知与内部信合并为**单表 per-user 投递行**，`read_at` / `deleted_at` 一表承载已读与软删。
- `track/`：用户行为埋点，独立 PG 表 + 有界缓冲旁路。
- `app/notify/`：跨域派生编排（装配层，**不是业务域**），异步旁路、可丢、不拖垮主业务。
- `platform/objectstore/`：唯一新增的 platform 接缝 `ObjectStore`，S3 SDK 只允许它 import。
- **管理台不设域**：`/admin/*` 只是路由前缀 + `AdminOnly` 中间件。

用户可感知的结果：公告有真实的有效期与定向；帖子的匿名、附件上限与编辑窗口由服务端强制；通知是 per-user 已读且同源多赞合并成一条；内部信可一对一发送；埋点批量上报部分成功、缓冲满也不影响任何业务端点；管理动作有门槛且留痕；图片经预签名短链访问。

## 横切口径（六域共享，下文各节引用本文）

### 鉴权分层

| 类别 | 端点 | 要求 |
| --- | --- | --- |
| **公开读（免登录）** | `GET /announcements`、`GET /announcements/:id`、`GET /announcements/latest`、`GET /posts`、`GET /posts/:id` | 无会话；进 `publicReadablePaths`，**与 `publicPaths` 并列但共用同一 `IsPublicPath` 判定入口** |
| **需农屿会话** | 其余所有客户端端点（回复/点赞/收藏/编辑/删除、消息、埋点上报） | 会话校验，fail-closed |
| **管理端** | 全部 `/admin/*` | `scope=admin` 会话 + `role>=1`；**改角色、切换集成开关、读取敏感字段三类要求 `role>=2`** |

- 角色判定**复用既有集中式 `RequireRole`**（对非法角色、非法最低要求、`UserID=0` 一律拒绝），**不在 handler 里散写**；role 以 DB 为准，令牌内 role 只作提示。
- 中间件顺序固定：`RequestID → NY1（适用时）→ 鉴权 → AdminOnly（仅 /admin）→ 限流`。

### 请求签名（NY1）分层

- **App 侧业务端点与埋点上报**：继续要求 NY1 签名。
- **`/admin/*`**：**不签**——浏览器拿不到 App 内密钥；管理端安全由 admin 会话 + role 三档 + fail-closed + 审计承担。
- **埋点上报**按客户端自报的 `platform`（`ios`/`android`/`web`）区分适用性。

### 响应信封

所有新端点统一 `{code: string, message: string}` + **camelCase** 字段名。**不照抄旧 Track 服务的 `{ok,data}` + snake_case**（语义对齐，不承诺字面一致）。

### 审计命名与粒度

- `audit_log.action` 用 **`<域>.<动作>`** 形式：`post.delete`、`post.restore`、`announcement.publish`、`announcement.revoke`、`user.role_change`、`integration.github_toggle`、`message.read_all`、`notification.emit`、`notification.dropped`、`user.read_sensitive`。
- **一次管理动作一条**，不做「一请求多动作合并」；审计对象记在既有 `target` 列（`<域>:<id>`）。
- **只记标识性字段**：绝不写票据、密码、身份证号、验证码、上游响应体，也**不写消息正文与收件人集合**。
- **管理员读取敏感字段也算一次审计**。

### 脱敏铁律（ADR-0001/0002）

票据、密码、身份证号、验证码、上游响应体**一律不得**进日志、审计、埋点、错误信息。埋点域另加落库前的字段级过滤（见埋点节）。

### 失败隔离（旁路取向）

- **通知派生**、**埋点写入**、**附件对象清理**三处都是**旁路**：失败只记日志与计数，**绝不回滚或阻塞主业务**。
- 与既有审计旁路同源：`docs/tech/持久层结构.md` 的「宁可丢审计，也不能让业务失败」。

## User Stories

### 公告

1. 作为学生，我想在首页看到最新一条仍在有效期内的公告，这样我不会错过临时停课、考试调整这类有时效的通知。
2. 作为学生，我想看到只针对我所在学院/年级/角色的公告，这样我不用在全校通知里翻找我关心的部分。
3. 作为学生，我想按发布时间浏览公告列表并看到置顶公告在最前，这样重要的长期公告不会被我划过去。
4. 作为学生，我不想看到草稿或已撤回的公告，这样我不会读到还没定稿或已经作废的内容。
5. 作为学生，我想在公告过期后仍能按 id 打开它看历史内容，这样我能核对之前看过的那条通知。
6. 作为管理员，我想把公告先存成草稿再发布，这样没写完的内容不会提前露出。
7. 作为管理员，我想在发布后撤回公告，这样写错的内容不会一直挂在学生面前。
8. 作为管理员，我想给公告设定生效与失效时间，这样过期公告自动从列表消失而不需要我手动清理。
9. 作为管理员，我想把公告置顶，这样长期有效的规则不会被新公告挤下去。
10. 作为管理员，我想在编辑后重新发布时让学生再收到一次通知，这样更正过的内容不会被当成已读过。
11. 作为学生，我不想在公告下点赞或回复，这样公告区只有一个明确的权威声音。
12. 作为未登录访客，我想能打开公告列表和公告详情，这样我不必先注册才能看学校通知。

### 帖子

13. 作为学生，我想在反馈板块发布一条带图片的帖子，这样我能把问题现场拍下来。
14. 作为学生，我想在帖子下留言，也想看到管理员给我的正式回复，这样我知道问题被谁处理到哪一步。
15. 作为学生，我想给帖子或留言点赞、收藏，这样我能标记有用的内容。
16. 作为学生，我想在发布后 24 小时内改正我的错别字或补充内容，这样我不会因为小错被误解。
17. 作为学生，我想超过 24 小时后不能再改，这样我不会被误以为可以事后篡改。
18. 作为学生，我不想在列表与详情里看到别的作者是谁，这样我敢说真话。
19. 作为其他学生，我想在留言里也看不到留言者身份，这样留言区同样安全。
20. 作为作者本人，我想能认出「这条帖子是我的」，这样我能在列表里找到自己的内容去编辑。
21. 作为学生，我想一次给一条帖子最多带 9 张图、单张不超过 5MB、只允许 jpg/png/webp/gif，这样我不必先自己压缩或转格式。
22. 作为学生，当我的图片超数量、超大小或格式不对时，我想被明确告知是哪一种原因，这样我知道该改哪一项。
23. 作为学生，我想能一直翻到更早的帖子且不出现重复或漏项，这样我找旧内容时不会迷路。
24. 作为学生，我想按板块筛选帖子，这样反馈区与留言区不会混在一起。
25. 作为学生，我想对同一条内容只算一次点赞，重复点与取消点都能正确反映，这样计数不会被刷。
26. 作为学生，我想能对留言点赞而不仅是对帖子点赞，这样有价值的回复也能被标记。
27. 作为学生，我不想看到帖子的阅读量或某个人的点赞数，这样我不会因为数字而不敢发。
28. 作为学生，我想在回复时不会被要求一层层嵌套，这样讨论不会变成看不懂的树。

### 站内消息（通知 / 内部信）

29. 作为帖主，我想在帖子被回复或被点赞时收到一条站内消息，这样我不必反复刷新页面找反馈。
30. 作为留言作者，我想在留言被点赞或被回复时收到消息，这样我知道有人回应了我。
31. 作为学生，我想看到自己的未读消息数，这样我一眼知道有没有新消息。
32. 作为学生，我想把单条消息标为已读，也想一次把当前筛选下的全部标为已读，这样我能快速清空红点。
33. 作为学生，我想删掉不想要的消息，删掉后未读数随之减少，这样我的收件箱不会越积越乱。
34. 作为学生，我想在一台设备上读完，另一台设备上也是已读，这样我的已读状态不会打架。
35. 作为学生，当同一条来源在短时间内被多次触发时，我想看到一条被合并的消息而不是刷屏，这样我的收件箱不会被同一件事塞满。
36. 作为学生，我想给另一名已知用户发一条内部信，这样我能就具体问题私下一对一沟通。
37. 作为学生，我不想收到自己给自己的消息，这样我的收件箱里只有别人真正发给我的内容。
38. 作为发送者，我想在把内部信发给一个不存在的用户时立刻被拒绝，这样我不会以为消息已经送达。
39. 作为收件人，我想知道内部信是谁发的真名，这样一对一的沟通能建立信任。
40. 作为学生，我想按类型（通知／内部信）和「仅未读」筛选消息列表，这样我能只看自己关心的那类。
41. 作为学生，我不能通过任何接口凭空创建一条通知，这样我不会收到伪造的系统消息。

### 用户行为埋点

42. 作为客户端，我想把一批用户行为事件一次性上报，这样弱网下不必为每条事件发一个请求。
43. 作为客户端，我想在部分条目不合格时其余条目仍被接收，这样一条坏数据不会连累整批。
44. 作为客户端，我想在返回里分别看出「已接收 / 重复 / 被拒」的数量，这样我能决定是否重传。
45. 作为客户端，我想在同一个事件重复上报时不产生两条记录、也不算失败，这样网络重试不会污染数据。
46. 作为客户端，我想在超过请求体积上限时被明确拒绝，而不是被静默截断后当成成功，这样我能及时修正。
47. 作为产品维护者，我想按事件类型与时间窗看到计数汇总，这样我能判断某个功能有没有人用。
48. 作为产品维护者，我不想通过埋点看到任何个人明细或导出个人行为，这样用户在功能里的行为不会被逐条围观。
49. 作为用户，我不想我的密码、证件号、票据之类信息被当作埋点上报或落库，这样即使我点了什么也不泄露敏感数据。
50. 作为用户，我不想客户端自报的身份覆盖服务端已认证的身份，这样我的行为不会被算到别人头上。
51. 作为运维，我希望埋点写不进去时业务请求照常成功，这样埋点故障不会让学生用不了别的功能。
52. 作为运维，我想能看到有多少事件因为缓冲满被丢弃，这样我知道要不要调容量。

### B 端管理（管理台后端 API）

53. 作为管理员，我想在管理台登录后就能发布公告、处理帖子，这样我不需要再单独要一套账号。
54. 作为管理员，我想在管理台看到被匿名保护的作者真实身份，但只在处理具体问题时，这样匿名不成为追责的障碍。
55. 作为管理员，我想我查看匿名作者真实身份这件事本身会被记录，这样这个能力不会被悄悄滥用。
56. 作为超级管理员，我想让「改角色」「打开/关闭某个集成开关」这类敏感动作只有我能做，这样普通管理员误操作不会扩大权限面。
57. 作为管理员，我想我做的每一个管理动作（删除、撤回、改角色、查看敏感字段）都会被记一条流水，这样事后能追溯是谁做的。
58. 作为安全负责人，我希望管理台会话会过期、也能被吊销，这样一台没退出的电脑不会长期敞着。
59. 作为安全负责人，我希望普通 App 会话、过期会话、被吊销的会话都进不了管理端点，且拒绝是默认行为而不是靠某个开关打开。
60. 作为安全负责人，我希望管理端不需要请求签名（浏览器拿不到 App 密钥），而 App 侧业务端点仍按既有要求签名，两侧口径不混用。
61. 作为审计者，我希望审计流水里只有标识性字段，不含票据、密码、验证码、上游响应体，这样审计本身不会变成泄露源。

### 文件存储（帖子附件）

62. 作为学生，我想我的图片在后续访问时仍能正常显示，而不是过一段时间就变成坏链接，这样我的帖子不会烂掉。
63. 作为学生，我想在帖子被删除后，别人手里的图片链接也很快就取不到内容，这样我的内容不会被长期留在外面。
64. 作为客户端开发者，我不想为图片存储单独接一套认证或密钥，这样客户端不因为存储而多一份凭据。
65. 作为后端维护者，我希望业务代码只说「存这个附件、取这个附件」，不直接依赖某个 S3 实现，这样将来换存储不牵动业务。
66. 作为后端维护者，我希望附件流量有体积硬限，且这个限制真的在后端执行，而不是靠客户端自律或存储侧策略。

### 横切

67. 作为学生，我想通知写入失败时我的发帖/回复仍然成功，这样我不会因为一个提醒挂了而丢掉自己的内容。
68. 作为学生，我想埋点写入失败时我用的任何功能都不受影响，这样埋点故障不会变成我的故障。
69. 作为运维，我想所有新端点都在同一条请求链路上（标识 → 签名（适用时）→ 鉴权 → 管理端门槛 → 限流），这样排查时不用记多套顺序。
70. 作为运维，我想新端点复用既有的限流实现而不是再长一套，这样配额口径只有一处。

## Implementation Decisions

### 域与依赖形状

- **四个顶层业务域**：`content/`（父包 + `post/`、`announcement/` 两个子域）、`message/`、`track/`。
- `content/` 父包**只放跨子域共享契约**：错误码、`Board` 枚举（`feedback`/`courtyard`）、**可见性判定入口**（两子域都要回答「这条内容对当前用户可见吗」）。
- **子域之间不互相 import**；`post` 与 `announcement` **都不 import `message`**。
- **装配层薄单元** `app/notify/`：算收件人 → 调消息域写入口。它是**编排**，不是业务域，无独立存储。
- **管理台不设域**：`/admin/*` 是路由前缀 + `AdminOnly` 中间件；管理端点写在各域自己的 handler 里。
- 每个域自带 `handler.go` / `service.go` / `repo.go`（**repo 是接口**）；PG 实现放 `platform/pgstore/`。这与仓库既有做法一致。

### platform 接缝增量（**只 1 个**）

新增 `ObjectStore`，方法集最小化为四个：

```go
// 来自 prototype（架构落点草图 @ 2be2297）第三节，此处照抄形状，不含实现
type ObjectStore interface {
    Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (ObjectInfo, error)
    PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
    Delete(ctx context.Context, key string) error
    Stat(ctx context.Context, key string) (ObjectInfo, error)
}

type ObjectInfo struct {
    Key         string
    Size        int64
    ContentType string
    ETag        string
}
```

- **不含 `PresignPut`**：上传走后端中转，不需要直传签名。
- **埋点写入与消息写入不进 platform**：`track_event` 与消息表是**本仓自有 PG 表**，不是外部能力，走各域自己的 `repo` 接口。
- **复用既有、不新增**：`platform.Clock`（TTL 计算与测试注入）、`platform.RedisCache`（帖子列表/详情缓存）、`platform.AuditStore`（审计）、`platform.StudentBriefReader`（公告受众过滤）。`platform.Store` **不用于本 effort**。
- **S3 SDK 只允许 `platform/objectstore/` import**；业务域只见 `ObjectStore` 接口。该约束可用 `go list -deps ./...` 断言。

### 各能力的实现决策（引决策笔记，不重写域模型）

| 能力 | 关键实现决策 | 出处 |
| --- | --- | --- |
| 公告 | 独立子域；状态机 **草稿 → 已发布 → 已撤回**；受众条件存公告自身（全员 / 按角色 / 按学院+年级），读列表时逐人按 `StudentBrief` 过滤；**不自建已读表** | [决策-公告域模型与作用域.md](../tech/决策-公告域模型与作用域.md) |
| 帖子 | `board` 区分 `feedback`/`courtyard`；**对读者匿名**（不下发作者标识，作者本人靠 `isMine` 类语义识别）；回复/留言无嵌套（深度 ≤ 2）；24h 编辑窗口由服务端判定；点赞/收藏靶子支持 `post`/`comment`；**读缓存只加在列表与详情** | [决策-帖子域模型与契约.md](../tech/决策-帖子域模型与契约.md) |
| 站内消息 | 单表 per-user 投递行；内部信 = 任何登录用户 → 任何已存在用户（不能发自己、fail-closed、限频、实名）；**通知对 `kind=通知` 建部分唯一索引 + upsert 累加计数**；未读数直查 PG，不加 Redis 计数器 | [决策-消息域模型.md](../tech/决策-消息域模型.md) |
| 用户行为埋点 | 独立域；**有界缓冲 + 后台批量写 + 满则丢弃计数**；批量 1~100、`event_id` 幂等、部分成功；**缓冲满不返回 503** | [决策-用户行为埋点.md](../tech/决策-用户行为埋点.md) |
| 横切鉴权 | 见本文「横切口径」节 | [决策-横切鉴权口径.md](../tech/决策-横切鉴权口径.md) |
| 通知派生 | 5 个触发点；编排放装配层；**异步旁路、可丢、不拖垮主业务**；同 source 合并而非按时间窗切分；发给自己双层过滤 | [决策-通知派生规则.md](../tech/决策-通知派生规则.md) |
| 对象存储 | **上传 = 后端中转**（唯一能让体积与格式上限成为后端真硬约束的形状）；**读 = 预签名 GET + 短 TTL**；单桶 + 按域分前缀；不配桶生命周期自动删除 | [决策-MinIO接入落点.md](../tech/决策-MinIO接入落点.md) |
| 范围 | 一份 PRD + 一份 Spec；不重开前三张已关 map | [决策-六功能范围切分.md](../tech/决策-六功能范围切分.md) |

## 数据契约

> 本节的字段名、端点形状、状态机取值属 **pending 第①层「阻塞开工」**——实施时不再需要回来问。

### 公告

**实体字段**：`id`、`title`、`body`、`status`（`draft`|`published`|`revoked`）、`publishedAt`、`effectiveFrom`、`expiresAt`、`pinned`、`publishRevision`（每次发布/重发递增）、受众条件三件：`audienceScope`（`all`|`role`|`college_grade`）、`audienceRoles`（int 数组，`audienceScope=role` 时用）、`audienceCollege`+`audienceGrade`（`audienceScope=college_grade` 时用）。

**状态机**：`draft → published`（发布，`publishRevision` 自增，记 `publishedAt`）；`published → revoked`（撤回）；`revoked → published`（重发，`publishRevision` 再自增，**允许再次通知**）。`draft → revoked` 不允许。

| 端点 | 鉴权 | 说明 |
| --- | --- | --- |
| `GET /announcements` | 公开读 | 只含 `status=published` 且在有效期内、且对当前用户可见（未登录 = 只看 `audienceScope=all`）；`pinned` 优先，同级按 `publishedAt` 倒序；游标分页 |
| `GET /announcements/:id` | 公开读 | 已发布的可查；**已撤回/草稿对非管理端不可见**；过期仍可查详情 |
| `GET /announcements/latest` | 公开读 | 已发布 + 有效期内 + 可见 + `pinned` 优先 + `publishedAt` 倒序取 1 条；无则 `data: null` |
| `POST /admin/announcements` | admin, `role>=1` | 创建（草稿或直接发布） |
| `PATCH /admin/announcements/:id` | admin, `role>=1` | 编辑 / 发布 / 撤回 / 置顶 |
| `DELETE /admin/announcements/:id` | admin, `role>=1` | 软删 |
| `GET /admin/announcements` | admin, `role>=1` | 含草稿与已撤回 |

### 帖子

**实体字段**：`id`、`board`（`feedback`|`courtyard`）、`title`、`body`、`attachments`（对象 key 数组，**只存 key 不存 URL**）、`publishedAt`、`editedAt`、`deletedAt`；作者标识**仅在服务端与 `isMine` 判定中使用，不下发给其他调用方**。

**回复/留言字段**：`id`、`postId`、`body`、`publishedAt`、`deletedAt`；**无 `parentReplyId`**（不做嵌套）。

**点赞/收藏**：`targetType`（`post`|`comment`）+ `targetId` + `userId`；**同一用户同一靶子唯一**（取消即删行）。

| 端点 | 鉴权 | 说明 |
| --- | --- | --- |
| `GET /posts` | 公开读 | 按 `board` 过滤 + `publishedAt` 倒序 + 游标分页；**加短 TTL 缓存（键含 board + 游标）** |
| `GET /posts/:id` | 公开读 | 详情 + 回复列表 + 附件预签名 URL；**加短 TTL 缓存（键含 id）** |
| `POST /posts` | 会话 | 发帖；body 内 `attachments` 为已上传的对象 key 数组 |
| `POST /posts/:id/replies` | 会话 | 留言（无嵌套） |
| `POST` / `DELETE` `/posts/:id/like` | 会话 | 帖子点赞/取消 |
| `POST` / `DELETE` `/posts/:id/favorite` | 会话 | 帖子收藏/取消 |
| `POST` / `DELETE` `/posts/:id/replies/:replyId/like` | 会话 | 留言点赞/取消 |
| `PATCH /posts/:id` | 会话（仅作者，24h 内） | 编辑；**超窗口返回专用错误码**，回 `editedAt` |
| `DELETE /posts/:id` | 会话（仅作者） | 软删；**异步清理附件对象**（失败只记日志+计数） |
| `GET /admin/posts` | admin, `role>=1` | 管理列表（**含真实作者，且该读取记审计**） |
| `DELETE /admin/posts/:id` | admin, `role>=1` | 管理删除（记审计 `post.delete`） |

**写操作对缓存的处理**：`POST`/`PATCH`/`DELETE` 一律删除对应 `board` 键；**per-user 列表与点赞数不缓存**。

### 附件上传（本 Spec 新定：`#53` 只定了机制未定路径）

| 端点 | 鉴权 | 说明 |
| --- | --- | --- |
| `POST /posts/attachments` | 会话 | **后端中转**：`multipart/form-data` 单文件；服务端**流式**校验并转发到 `ObjectStore`，**不落本地盘**；返回 `{key}` 供 `POST /posts` 引用 |

- **体积硬限在执行点**：单请求体经 `MaxBytesReader` 限死，**超过 5MB 直接拒绝**，不依赖存储侧策略。
- **格式白名单**：`jpg|jpeg|png|webp|gif`；按内容嗅探而非仅看扩展名。
- **对象 key 模板**：`posts/{yyyy}/{MM}/{uuid}.{ext}`；预留 `announcements/{yyyy}/{MM}/{uuid}.{ext}` 前缀但**本期不实现**。
- **数量上限 9 张**在 `POST /posts` 与 `PATCH /posts/:id` 处校验（非上传端点，因为上传是逐个的）。
- 上传端点**限流复用既有 `RateLimitMiddleware`**。
- 「MinIO 是否真的不提供身份认证」**依据 AWS S3 文档，未在 MinIO 实测**（见 Further Notes 第④层）。

### 站内消息

**实体字段**：`id`、`recipientId`、`kind`（`notification`|`internal_mail`）、`sourceDomain`、`sourceObjectId`（通知必填；内部信为空）、`title`、`body`（**触发方写入时落库的快照**，不做读时渲染）、`hits`（合并次数）、`readAt`、`deletedAt`、`createdAt`。

**唯一性**：对 `kind=notification` 建部分唯一索引 `(recipientId, kind, sourceDomain, sourceObjectId)`，同键重复触发走 **upsert 累加 `hits` + 重置 `readAt=null`**。

| 端点 | 鉴权 | 说明 |
| --- | --- | --- |
| `GET /messages` | 会话 | 只返回本人；`createdAt` 倒序；游标分页；可按 `kind` 与 `仅未读` 过滤 |
| `GET /messages/unread-count` | 会话 | `count(readAt is null and deletedAt is null)` |
| `PATCH /messages/:id/read` | 会话 | 单条已读（非本人 → 404，不泄露存在性） |
| `POST /messages/read-all` | 会话 | 按当前筛选条件批量置已读 |
| `DELETE /messages/:id` | 会话 | 软删；不计未读 |
| `POST /messages` | 会话 | **仅 `kind=internal_mail`**；**通知不接受外部直接建** |

**内部信校验**：收件人必须存在（fail-closed）、不能发自己、`body` 有长度上限、按发送者限频。

### 用户行为埋点

**实体字段**：`eventId`（幂等键）、`eventType`（7 个枚举：`screen_view`|`button_click`|`perf`|`app_open`|`heartbeat`|`crash`|`llm_proxy_fail`，最后一个仅服务端）、`eventName`、`clientTsMs`、`sessionId`、`appVersion`、`platform`（`ios`|`android`|`web`）、`props`（自由 JSON，**4KB 截断**）、`requestId`、`userId`（**服务端注入，忽略客户端自报**）、`receivedAt`。

| 端点 | 鉴权 | 说明 |
| --- | --- | --- |
| `POST /track/events` | 会话 + NY1（按 `platform`） | 批量 1~100；Body ≤1MB（`MaxBytesReader`）；`eventId` 幂等；**单条失败不整批拒**；返回 `{accepted, duplicated, rejected}` |
| `GET /track/events/summary` | admin, `role>=1` | 只给**按事件类型 + 时间窗的计数**；无 props 明细、无导出 |

**脱敏（写入侧，顺序不可颠倒）**：
1. **顶层白名单**：未知顶层字段**丢弃**（不报错）。
2. **`props` 敏感键名剔除**：`password`/`passwd`/`pwd`/`idCard`/`id_card`/`token`/`ticket`/`cookie`/`authorization`/`code` 等，剔除后计数。
3. **再做 4KB 截断**（截断在剔除之后）。

**缓冲满**：**不返回 503**，正常返回并计入 `dropped`；关停前 flush。

### 错误码（新端点统一使用）

除既有 `unauthorized` / `rate_limited` / `schema_rejected` 外，本 effort 新增并固定以下取值：

`not_found`、`forbidden`、`validation_failed`、`locked`（编辑窗口已过）、`too_many_attachments`、`attachment_too_large`、`unsupported_media_type`、`recipient_not_found`、`self_send_forbidden`、`invalid_cursor`、`payload_too_large`。

**错误响应不携带内部细节**（不回显 SQL、上游响应体、堆栈）。

## Testing Decisions

**什么算好的测试**：只断言**外部可观察行为**——HTTP 状态码、响应体字段、是否产生了外部调用（用计数的 fake 断言）；不断言内部函数调用顺序或私有字段。

**两层接缝**（沿用 #51 Q4 与架构草图第七节，不新增第三层）：

1. **纯函数单测（无 IO）**——本 effort 逻辑最密的一层：
   - 公告**可见性判定**（全员/角色/学院年级三档 + 未登录）
   - 公告**受众过滤**（`audienceScope` 与 `StudentBrief` 的匹配）
   - 消息**幂等键组装**（`sourceDomain` + `sourceObjectId` 含动作与靶子，如 `like:post:88`）
   - 埋点**脱敏规则**（白名单丢弃 → 敏感键剔除 → 4KB 截断，**顺序**要有用例）
   - 埋点 `props` **形状校验**与事件枚举校验
   - 编辑窗口判定（`publishedAt` + 24h 边界）
2. **HTTP 集成测**——`httptest` 打路由，repo 用 fake：
   - 公开读 5 个端点在**无会话**下可读，其余端点无会话返回 401
   - `/admin/*` 在 `role=0` / 过期会话 / 已吊销会话下被拒；`role>=2` 专属动作在 `role=1` 下被拒
   - 发帖/回复/点赞/收藏/编辑/删除的完整读写链与错误码
   - 消息列表/未读数/单条已读/全部已读/软删的 per-user 隔离（A 的消息 B 看不到）
   - 埋点批量部分成功（1 条坏 + 2 条好 → `{accepted:2, duplicated:0, rejected:1}`）、重复 `eventId` 记 `duplicated` 不算失败
   - 附件上传超 5MB / 格式不在白名单 → 对应错误码
   - 通知派生：主业务成功但队列满 → **主业务仍 200**，`dropped` 递增

**只有两处用真实外部依赖**（其余一律 fake）：

- **PG 的部分唯一索引 upsert 行为**：`(recipientId, kind, sourceDomain, sourceObjectId)` 同键第二次写入必须**合并成一行并累加 `hits`**，这是 fake repo 覆盖不到的真实约束。沿用仓内既有的「未设置 `*_TEST_*` 环境变量则跳过」做法。
- **MinIO 预签名读写**：`PresignGet` 产出的 URL 能被 `PUT`/`GET` 使用。**注意：MinIO 侧具体行为未实测**（见 Further Notes 第④层），该用例的期望值以 AWS S3 语义为准，首次运行需人工确认。

**先例**（照这些写，不要新发明风格）：

- `internal/school/jiaowu/handler_test.go` —— `httptest` + gin 的行为断言。
- `internal/platform/cache_test.go` —— 缓存层语义与「未配置时降级」。
- `internal/identity/session/*_test.go` —— fail-closed 的逐例覆盖（非法角色、`UserID=0` 等）。

**客户端契约不进本 Spec**：端点与字段由「数据契约」节给出；客户端 SDK 不在本仓。

## Out of Scope

（以 map #43 的 Out of Scope 段为准——比 PRD 摘录的 13 条更全；PRD 的 13 条是其子集。）

- 管理端 UI（在 `nongyu-web-admin`）。
- 新建完整 RBAC（角色／权限点／菜单）模型；只复用 `role` 三档。
- 运行日志与审计流水的重定、应用直连 ES、日志检索出口。
- MinIO 选型重开（对比 OSS／本地盘）。
- 埋点查询分析平台／看板／导出／明细查询；只留计数汇总口。
- 客户端埋点 SDK、消息推送 SDK 的**代码实现**。
- **把埋点做成独立服务/独立端口/独立库**。
- **照抄旧 Track 的 `{ok,data}` + snake_case 信封与「队列满 503」**。
- **在埋点域做 `presence/offline` 在线态回写**。
- **消息推送通道（厂商推送／APNs／邮件）**；本期只做站内拉取。
- **内部信的用户间社交能力**（好友关系／拉黑／举报）；本期只做频率限制。
- **要求 `/admin/*` 也签 NY1 请求签名**。
- **新增 `RequestIDMiddleware` / 限流实现**。
- **把业务公开读端点塞进 `publicPaths`**。
- **让「B 端管理操作」（封禁／审核结果）触发通知**——无来源可挂（无审核态、无封禁能力）。
- **让身份域「登录异常」触发通知**、**让埋点数据触发通知**。
- **为通知派生新建 `internal/notification` 业务域**。
- **通知与主业务同步同事务**。
- **点赞通知按时间窗合并**（本期只做同 source 合并）。
- **公开永久可读的附件 URL**。
- **客户端预签名直传附件**。
- **按域分桶**、**S3 桶生命周期自动删除规则**。
- **把埋点写入或消息写入做成 `platform` 接口**。
- **`admin/` 单独成业务域**。
- **按「六个能力 = 六个顶层域」切分**。
- **把 PRD 与 Spec 合并成一份**。
- **「用户故事按角色分组」**。
- **三层测试接缝（含真实 PG+Redis+MinIO 全链）**。
- 旧后端代码／表结构／部署链路的复用、数据与表结构迁移、部署链路切换。
- 把旧仓 Admin JWT「永不过期」口径搬进新仓。
- 把旧仓 `notified_author` 行级一次性投递位照搬成 per-user 已读模型。
- 把旧仓 Track 服务／`nongyu-node-server` 的代码、表结构、部署链路当作本仓依赖来源。
- 把旧仓「公告 = `post_type='announcement'` 取值」的形状照搬进新仓。
- 把旧仓公告与帖子的 HTTP 路径逐字复刻进新仓。
- **帖子域不做富文本**、**不做审核态**、**不做「仅自己可见」**。
- **站内消息不做 Redis 未读计数器**、**不做读时按 source 实时渲染**（快照语义）。
- **修改现行 PRD 的范围段**。
- 产出可运行 Go 业务代码以外的东西（本 Spec 之后是实施计划，再之后才是编码）。
- 前三张已关 map 的 effort（WebVPN 能力重写、身份域／平台底座、教务查询与缓存），以及学校业务线本身。

## Further Notes

### 待定项四层（本 Spec 只定第①层，②③④在此登记）

**① 阻塞开工 —— 已在本文定死**：字段名、表结构形状、索引、端点路径与请求响应形状、状态机取值、对象 key 模板、错误码。实施时**不再回来问**。

**② 施工期可定 —— 实施时定，标注依据**

| 项 | 判据／依据 |
| --- | --- |
| 预签名 GET 的 TTL 值 | 建 5~15 分钟区间；越短则「软删后已签发 URL 仍可访问」的窗口越小 |
| 帖子读缓存 TTL 与 `board` 键命名 | 沿用「新鲜度窗口与物理 TTL 分开」的既有做法 |
| 单帖附件总大小上限 | 除「9 张 × 5MB」外是否再设总限，按实际流量定 |
| 站内消息保留期 N 与清理作业 | 只留「超过 N 天且已读的软删消息可清理」的运维口 |
| 埋点保留期 N、缓冲容量、flush 周期、批量大小 | 按事件量压测定；`dropped` 计数是调参依据 |
| 通知队列容量、flush 周期、批量 | 同上；容量需覆盖「全校公告 fan-out」峰值 |
| 公告 fan-out 到数万收件人的分批写入 | 与「单表 per-user 投递行」的规模代价相关；分批 + 限速 |
| `publishRevision` 的生成规则 | 每次「发布/重发」自增；撤回不增 |
| `Delete` 失败的重试策略 | 旁路取向：重试有限次后放弃并计数，不阻塞软删 |
| `audit_log.action` 完整取值表 | 命名规则与示例集已在本文固定，实施时补齐剩余动作 |
| 超管/管理员完整权限矩阵 | 已定三类 `role>=2` 动作；其余动作的门槛实施时列表 |

**③ 留后续 effort / 本期不做**：推送通道、内部信防骚扰与社交能力、阅读量与覆盖率统计、图片服务端二次处理（压缩/缩略图/EXIF 剥离）、管理端 CSRF/会话固定防护、管理台「强制下线他人会话」、公开读端点的独立限流档位、埋点 `platform` 自报可伪造的更强证明、SDK 事件名白名单、六功能的实施顺序与分批。

**④ 需实测 —— 不许当已知**

- **MinIO 预签名行为**：本文的预签名语义**依据 AWS S3 公开文档**，**未在 MinIO 实例上实测**。首次做 MinIO 集成测时必须人工确认（含 TTL 上限、`x-amz-*` 参数、`Put`/`PresignGet`/`Delete`/`Stat` 的实际可用性）。若要拿一手结论，另开一张 HITL task 起 MinIO 实测，**不阻塞本 Spec**。

### 依赖策略

新增 Go 依赖 `github.com/minio/minio-go/v7`（Apache-2.0、纯 Go、无 CGO、原生 presigned）。按根 `AGENTS.md` 的「停用即删」双确认判据：① `go mod tidy` 后 `go.mod` 不再出现该 `require`；② `go list -deps ./...` 不再含该包路径。**不手改 `go.sum`**。引入理由须写进 commit 说明。该 SDK **只允许 `internal/platform/objectstore/` import**。

### 已知边界（不假装没有）

1. **「帖子回复成功但通知没投递」可能发生**——异步旁路、可丢；靠 `dropped` 计数观测。这是与「审计可丢」同源的取舍，Spec 与实施计划都不得写成「通知必达」。
2. **附件软删后已签发 URL 在 TTL 内仍可访问**——预签名 URL 无法回收；只能靠短 TTL 缩小窗口。
3. **埋点 `platform` 字段由客户端自报，可伪造**——本期接受，不为此引入更强的客户端证明。
4. **广播型公告的 fan-out 行数随收件人线性增长**——全校全员公告会产生数万行；本期接受，若成瓶颈再评估广播型水位。
5. **内部信实名、帖子匿名**是**刻意相反**的取向——不是不一致。

### 与既有设施的关系（不动清单）

本 effort **明确选择不动**以下既有设施。将来若有人想改这些，先回来改本文，不要在实施中顺手改：

1. **`/status` 保持公开且限流豁免**；限流仍**只豁免 `/healthz`**。限流豁免与鉴权豁免是两件事，不合并。
2. **不新增** `RequestIDMiddleware`、**不新增**限流实现——复用既有。
3. **响应信封统一 `{code: string, message: string}` + camelCase**；不引入 `{ok, data}` 或 snake_case。
4. **中间件顺序固定** `RequestID → NY1（适用时）→ 鉴权 → AdminOnly（仅 /admin）→ 限流`。
5. **`publicPaths` 不被业务端点污染**：公开读另立 `publicReadablePaths`，**两者共用同一 `IsPublicPath` 判定入口**（仍只有一处判定）。
6. **既有会话、`role` 三档、`RequireRole` 的 fail-closed 语义**不改；role 仍以 DB 为准。
7. **既有 `RedisCache` / `AuditStore` / `StudentBriefReader` / `RateLimitMiddleware` / `MaxBytesReader` 的用法**不重写，只复用。
8. **前三张已关 map 的任何结论**不改（上游 effort）；六域需求与上游冲突时，改六域侧设计。

### 移交

- 下一步是**实施计划**（`docs/plans/`），不是直接编码；SDD 顺序见 `README.md:79-80`。
- 实施时对照的形状依据：[架构落点草图](../tech/架构落点草图-六类常见功能.md) @ `2be2297` 的第二～五节；**什么算还原**与**复核人**已写在该票正文。
- 本文的「四五层待定项」若在实施中需要变更第①层（字段/端点/状态机），**先改本文再改代码**，不在代码里悄悄改口径。
