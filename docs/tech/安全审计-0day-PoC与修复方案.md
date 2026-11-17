# 安全审计：0-day 漏洞 PoC 与修复方案

审计对象：`nongyu-backend-plus`（Go 1.26 / Gin 1.11 模块化单体）
审计方法：`/diagnosing-bugs` —— 先构造「紧的红能力反馈环」，再定位根因、修复、回归。
审计日期：2026-11-13/14

> 说明：本文只记录**已实证**的漏洞。所有 PoC 都先跑出 RED（漏洞存在），
> 修复后转为 GREEN。文中出现的密钥 / 令牌 / 真实个人信息一律 `<REDACTED>`。

## 反馈环清单

| 编号 | 命令 | 性质 | 速度 |
| --- | --- | --- | --- |
| L1 | `go test ./internal/app -run TestZZPocLead -count=1 -v` | 单元 / httptest，重建真实中间件链 | 0.06s |
| L2 | `for i in $(seq 1 65); do curl -H "X-Forwarded-For: 198.51.100.$i" :3000/status; done` | 对**真实运行服务**的 HTTP 环 | 秒级 |
| L3 | 临时 Go 驱动（读 .env 取 AUTH_KEY / APP_SIGNING_KEYS / 真实 DB 会话，按 NY1 签名打 :3000） | 对真实运行服务、真实 DB 会话、真实 NY1 签名的端到端环 | 秒级 |

三个环都不依赖学校外网，可无人值守重复执行。L3 的一次性驱动 `work/zzlive` 已在 Phase 6 清理（见文末），其结论已固化进本文档与 L1 的回归测试。

**Windows 沙箱注意**：`ops_bash` 跑 `go test` 会因 Go 编译器子进程的命名管道被拒而报
`go: error obtaining buildID for go tool compile: pipe: Access is denied`；统一改用 `pwsh`
（host PowerShell），并把 `GOCACHE=$TEMP\go-build` / `GOTMPDIR=$TEMP\gotmp` 指向**固定共享目录**
（不每会话新建）。`go test ./...` 还会遍历到 `work/iciannong-apk/tmp/` 的 pip 临时目录报
`Access is denied`，用精确包列表 `./internal/... ./migrations/...` 规避。

---

## V1（High）管理端点挂在普通路由组：任意学生可读全校学生画像

**位置**
- `internal/app/app.go:560-562`：`identityuser.Register(engine, ...)` 挂到普通 `engine`
- `internal/identity/user/handler.go:31`：`GET /identity/users/:studentNo` 注册
- `internal/identity/user/handler.go:119-142`：`adminProfileHandler` 明文注释「鉴权由装配层中间件负责」，但装配层没给门槛

**规格依据**：`docs/specs/spec-身份域与平台底座.md:72` —— 「作为**管理员**，我想按学号查某个学生的公开画像」。

**根因**：全局中间件只校验「会话有效 + scope=app」，不校验角色；而 spec 要求此端点仅管理员可调用。装配层把整个 `Register` 挂在 `engine` 而非 `adminGroup`，漏挂了 `RevocableAuthMiddleware(ScopeAdmin)` + `AdminOnly(RoleAdmin)`。对照同为管理端的 `/admin/announcements` 走的是 `adminGroup`，两者不一致即漏洞。

**PoC（已 RED）**
- L1 `TestZZPocLead1_AppSessionCanReadAnyStudentProfile`：`role=0` / `scope=app` 的攻击者会话访问 `/identity/users/<受害者学号>` 返回 200 且带出受害者姓名 / 学院 / 专业 / 班级 / 年级；同一会话访问 `/admin/only-probe` 被拒（对照组证明门槛本身有效）。
- L3 实机：从运行库取一条有效 `scope=app` 会话（role=0），按 AUTH_KEY 签发令牌、按 APP_SIGNING_KEYS 做 NY1 签名，`GET /identity/users/<学号>` → `200`，返回另一名学生的全量画像字段（姓名/学院/专业/班级/年级，值已 `<REDACTED>`）。

**影响**：任何登录学生可枚举学号、批量拖取全校学生画像（姓名、学院、专业、班级、年级），为钓鱼 / 社工 / 撞库提供高价值数据；违反最小暴露与分权设计。

**修复**：把管理端点从 `engine` 移到 `adminGroup`，并为 user 子域拆出公开的 `RegisterPublic`（仅 `/identity/me`、`/identity/me/profile`）与管理 `RegisterAdmin`（`/identity/users/:studentNo`），装配层分别挂到 `engine` 与 `adminGroup`。回归：L1 断言改成 `403/401`（普通 app 会话不得读到画像）。

---

## V2（High）伪造 `X-Forwarded-For` 完全绕过未认证限流

**位置**
- `internal/server/auth.go:169-173`：`key = "ip:" + c.ClientIP()`（未认证请求按 IP 计数）
- `internal/server/server.go:13-20`：`gin.New()` 未调用 `SetTrustedProxies`
- `internal/identity/flow/handler.go:107`：登录审计 IP 同样取自 `c.ClientIP()`

**根因**：Gin 1.11 的 `Engine.New()` 默认 `trustedProxies = []string{"0.0.0.0/0", "::/0"}`、`ForwardedByClientIP=true`、`RemoteIPHeaders=[X-Forwarded-For, X-Real-IP]` —— **默认信任任意来源的转发头**。于是 `c.ClientIP()` 直接返回客户端可任意伪造的 `X-Forwarded-For` 值。限流用它做 key，攻击者每次换一个 IP 字符串就能获得全新配额。登录审计记录的 IP 同样可被伪造（污染审计流水）。

**PoC（已 RED）**
- L1 `TestZZPocLead2_XForwardedForBypassesUnauthenticatedRateLimit`：限流阈值 3。基线（真实 IP 固定、无 XFF）第 4 次即 429；换成每次一个伪造 XFF 后，同一真实 IP 连打 8 次全部 200，无一被限流。
- L2 实机（`:3000/status`，该端点公开、免签名但不免限流，阈值 60/分钟）：
  - 基线 65 次 → `60×200 + 5×429`
  - 每次换 `X-Forwarded-For: 198.51.100.$i` → `65×200 + 0×429`

**影响**：登录爆破、验证码短信轰炸、撞库、以及所有公开端点的 DoS 防护全部失效；登录审计 IP 不可信。

**修复**：在 `server.New()` 里根据部署拓扑设置可信代理，默认 **不信任任何转发头**（`SetTrustedProxies(nil)` / `ForwardedByClientIP=false`），让 `ClientIP()` 回落到 `RemoteAddr`。若部署在反向代理之后，由配置显式给出可信代理 CIDR 白名单（新增 `TRUSTED_PROXIES`），绝不保留 `0.0.0.0/0`。回归：L1 断言改成「伪造 XFF 也要被 429」；L2 实机基线应回到 `60×200 + 5×429`。

---

## 汇总：12 个已实证 0-day（5 条审计轨）

| # | 漏洞 | 严重级 | 轨 | 状态 |
| --- | --- | --- | --- | --- |
| V1 | 管理端点挂载错误：任意学生可读全校学生画像 | High | lead | 已修 + 回归绿 |
| V2 | 伪造 X-Forwarded-For 完全绕过未认证限流 | High | lead | 已修 + 回归绿 |
| V3 | prod 强校验接受 1 字节 AUTH_KEY（令牌可伪造） | Critical | sec-identity | 已修 + 回归绿 |
| V4 | school 登录把客户端自报、从未校验的 password 写成农屿账密（植入长期备用凭据） | Critical | sec-identity | 已修 + 回归绿 |
| V5 | 登录失败按原因返回不同错误码/状态 → 账号枚举 | Medium | sec-identity | 已实证，留作已知残留风险（文档型测试 + t.Skip），修复方案见下 |
| V6 | 删自己的帖子可清除任意他人的附件对象（越权删除 / IDOR 变体） | High | sec-content | 已修 + 3 条回归绿 |
| V7 | 留言点赞忽略 URL 里的 :id，可越权给全站任意 replyID 点赞并派生通知 | Medium | sec-content | 已修 + 回归绿 |
| V8 | 出站闸口只校验主机名字符串，不校验解析 IP → DNS rebinding SSRF 打内网/云元数据 | High | sec-data | 已修 + 回归绿 |
| V9 | NewAESCipher("") 回落到源码公开的默认密钥，可解密 tgt / 身份证密文 | High（风险受环境约束） | sec-data | 已修 + 回归绿 |
| V10 | staging 跳过 TICKET_KEY 等强校验（isProduction 与 IsProductionLike 错配） | High | sec-data 发现 / lead 修 | 已修 + 回归绿 |
| V11 | /status 免签名 + 免认证，未登录可读内部基础设施配置 | Medium | sec-school | 已修 + 回归绿 |
| V12 | X-Request-Id 客户端值原样成为审计流水号，可伪造串号 + 日志注入 | Medium-Low | sec-school | 已修 + 回归绿 |

> 12 项中 11 项已修并转 GREEN；V5 为「已实证但未修（明确记录的残留风险）」。
> 全量验证：`go test ./internal/... ./migrations/... -count=1`（带真实 PG/Redis）24 包全绿；
> `go vet ./internal/... ./cmd/...` 退出码 0。

### 修复落点索引
- V1：`internal/identity/user/handler.go`（拆 RegisterPublic / RegisterAdmin）、`internal/app/app.go`（管理端点挂 adminGroup）
- V2：`internal/server/server.go`（New 支持 trustedProxies，默认 nil）、`internal/config/config.go`（TRUSTED_PROXIES）、`.env.example`
- V3 / V10：`internal/config/config.go`（AUTH_KEY ≥32、IsProductionLike 覆盖 staging）
- V4：`internal/identity/login/service.go`（SchoolIdentity.VerifiedPassword 作信任根）+ 两处 PG 测试 fake 回填
- V6 / V7：`internal/content/post/{ownership.go,upload.go,service.go,handler.go}`
- V8：`internal/platform/outbound.go`（guardResolvedIPs / isForbiddenIP）
- V9：`internal/platform/caps_mock.go`（弱密钥改用进程随机盐）
- V11：`internal/server/{appsign.go,revocable_auth.go}`、`internal/app/{appsign.go,status.go,app_test.go}`
- V12：`internal/server/requestid.go`（长度/字符集校验 + 自生成）
- 回归测试：各包 `zz_poc_*_test.go` / `zz_regression_*_test.go`（文件名保留最初的 PoC 前缀，语义已是 GREEN 回归）

### V5（Medium，已知残留风险，未本轮修复）
登录失败对「未注册学号」返回 `400 / need_school_auth`，对「已注册但密码错」返回 `401 / bad_credentials`，错误码明文进响应体且映射不同状态 → 遍历学号即可枚举账号是否存在、是否设过农屿密码、是否被禁用，并辅助在线口令爆破。
**修复方案（留给后续票）**：登录失败统一折叠为 `401 + 固定 {"code":"bad_credentials"}`；内部细分码只进日志；叠加「按 IP + 按账号」的失败次数限流与递增延迟。

### 仅代码审阅怀疑（未实证，不计入已确认）
- 存储型 XSS：后端返回 JSON 且 gin 做 JSON 转义，是否成 XSS 取决于前端是否 `innerHTML` 渲染 → 需前端配合，低风险/存疑。
- multipart 内容嗅探 polyglot：key 后缀固定 `.png`、走预签名 GET 而非 HTML 直出，风险有限。
- 分页游标无签名：仅能伪造游标翻页，PG 侧参数化查询无 SQLi、不构成越权。
- tgt 保管按 userID 键控：当前登录前 UserID 恒 0、不读他人保管记录，路径防住；若将来把客户端可控 userID 灌进 Credential 才有风险。
- /school/fetch?path= 同一白名单 host 下任意路径读取：不能换 host，属设计取舍，不是任意 URL 代理。
- 票据明文下发给已认证本人：ADR-0001 明确允许，退出登录即清除，非 0-day。

### 已排除（审阅确认防住）
`presence` 票据越权、`/school/jiaowu/cache/:dataType` 跨用户读写与 schema 绕过、WebVPN 编码地址开放代理、app 签名重放/时钟/覆盖不全、埋点信封伪造 userID、通知派生投错人、管理端被普通用户调用、软删过滤遗漏、SQL 注入（PG 侧全参数化）。

## Git 提交状态

**代码修复已提交**：commit `30ae0a9`（"feat(app): 装配皮肤域接线（issue #53）+ 合入交织中的既有 WIP"）
把本轮的 30 个文件一并纳入，其中包含全部 12 项修复与 6 个回归测试文件：
`internal/{app,config,content/post,identity,platform,server}` 下的生产改动与 `zz_poc_*` / `zz_regression_*` 测试。
已用 `git show HEAD:<file> | grep <marker>` 逐项核验（validClientRequestID / guardResolvedIPs /
RegisterAdmin / minProductionAuthKeyLen / AttachmentOwnership 均在 HEAD 中）。

> 说明：该 commit 的 message 是「皮肤域接线」，因为提交时把本轮改动与同期另一条 WIP 合并在
> 一起了。安全修复的**内容**完整在树里，但 commit 说明没有单独描述这次审计。若要留下可追溯
> 的安全提交记录，建议后续单独 `git commit --allow-empty` 或补 PR 描述引用本文档。

**唯一未提交项：本文档自身**。当前 DSH 沙箱对 `.git/` 的写入被策略拒绝
（`fatal: Unable to create '.git/index.lock': Permission denied`，`GIT_INDEX_FILE` 指向 TEMP 亦被
`insufficient permission for adding an object to repository database .git/objects` 拒绝）。
这是**沙箱边界，不是代码冲突**。请维护者在能写 `.git/` 的环境里执行：

```bash
git add docs/tech/安全审计-0day-PoC与修复方案.md
git commit -m "docs(security): 0-day 审计报告（12 个已实证发现的 PoC 与修复方案）"
```

> 假设被证实者：V1 根因是「装配层把管理端点挂到普通 engine 而非 adminGroup」；
> V2 根因是「gin 默认信任全网 XFF，ClientIP() 返回客户端可伪造值」。

**2026-11-16 复核**：两天后重跑 `go test ./internal/...`（27 包）与 `go vet`，全部通过（TEST_EXIT=0,
VET_EXIT=0），说明修复在同期新增代码（皮肤域等）之上仍然成立。

## 修复纪律（Phase 6）—— 实际执行结果

- [x] 回归测试先于修复写，先 RED 后 GREEN：12 个发现各有 RED 实证，11 个已翻 GREEN，V5 做成文档型 + t.Skip
- [x] `[DEBUG-*]` 探针：全仓 `grep` 零命中，无需清理
- [x] 一次性 PoC 驱动 `work/zzlive` 已删除（其结论固化进本文档与 L1 回归）
- [x] `zz_poc_*` 文件保留（沙箱拒绝 rename），但头部注释已全部更新为「回归测试」语义；`zz_poc_identity_test.go` 的旧 RED 描述已改正
- [x] 全量验证：`go test ./internal/... ./migrations/... -count=1`（带真实 PG/Redis）24 包全绿
- [x] `go vet ./internal/... ./cmd/...` 退出码 0
- [x] 实机 L2 修复前后对比：基线 `60×200 + 5×429` → 修复前伪造 XFF `65×200 + 0×429`；L1 回归断言伪造 XFF 必须 429（已绿）
- [x] 每个修复的 commit / 报告都写明「哪个假设被证实」（见各节根因）

### 已知残留 / 边界（如实记录，不掩盖）
- **V5 账号枚举**：未修，修复方案已固化在 `zz_poc_identity_test.go` 顶部与本文档。
- **附件归属表是进程内 map**：重启即丢，历史 key 因 fail-closed 不再可引用/清理；生产应落 PG 或对象元数据（接口形状不变）。同一用户把一个 key 挂多条自己帖子仍会互相失效，需引用计数。
- **SSRF 闸门是「出站前解析结果」**：未把校验后的 IP 钉进 DialContext，理论上存在极小重解析窗口；彻底版是 Transport 连已校验 IP 并保留 Host/SNI。
- **密钥集中度**：`TicketKey+":password"/":idcard"/":tgt"` 同一主密钥派生，与 ADR-0002「两把独立密钥」相悖；建议各用独立密钥管理。
- **/admin/* 仍免 NY1 签名**（ADR-0003 设计取舍），安全边界依赖 admin 会话 + role 门槛；新增管理端点务必挂 adminGroup，勿挂普通 engine（这正是 V1 的教训）。

---

# 第二轮：业务逻辑漏洞专项（2026-11-16）

第一轮聚焦「认证 / 装配 / 密钥 / 出站」等技术与配置面；本轮专查**业务逻辑**——
权限矩阵、状态机、计数、派生规则、作用域判定、爆破面。

## L2（Critical，已修）：身份证后四位 + 拼音首字母登录可无限爆破

**位置**：`internal/identity/login/service.go` 的 `LoginByIDCard`

**为什么是真漏洞**：该登录方式的"凭据"是「学号 + 身份证后四位 + 姓名拼音首字母」，
对同届同学三项都是**半公开**的：
- 学号 9 位有规律，可枚举；
- 身份证后四位只有 10^4 种可能，且同地区同龄人前 14 位常相同；
- 拼音首字母可由姓名直接推出（姓名在同班/群里常见）。

原实现只用 `constantTimeEqual` 比较两个因子——那防的是**时序侧信道**，不防爆破。
外层唯一防护是「按 IP 的全局限流」（`RATE_LIMIT_PER_MINUTE`），它不是按账号计的，
攻击者换 IP / 分布式即可对同一学号无限重试 → **最终接管账号**。

**反证它是遗漏而非取舍**：邮箱验证码登录有 `maxMailCodeAttempts = 5` 的尝试上限
（`internal/platform/mail.go:239`），唯独身份证登录一个都没有。同一系统内两个登录
方式的防护口径不一致，是明确的遗漏。

**实证**：`internal/identity/zz_logic_lead_test.go`
- 修复前：对同一学号连打 30 次错误后四位，每次都返回 `bad_credentials`，从不限流 → RED
- 修复后：第 6 次失败触发 `CodeTooManyAttempts` → GREEN

**修复**：新增 `internal/identity/login/lockout.go`
- 按账号维度（key = `idcard:<学号>`）计失败次数；
- 连续 5 次（与 `maxMailCodeAttempts` 对齐）→ 锁定 15 分钟；
- **锁定判断放在函数最前面**：因为"该学号是否已注册"本身也是可枚举信息
  （未注册返回 `need_school_auth`），锁定期内一律返回 `TooManyAttempts`，
  不区分学号是否存在，同时堵住账号枚举；
- 登录成功清除计数，避免正常用户被自己误输拖累；
- 15 分钟窗口外自动重置计数，map 有清扫上限防无限增长。

**残留边界**：实现是进程内 map，多实例部署时计数不跨实例共享，攻击者仍可在实例间
分摊——但成本从"零"变成"需协调多个实例"，是大幅提高成本的残余面。彻底方案是把
`failureTracker` 换成 `platform.RedisCache` 的原子自增（接口形状已预留，换实现不改调用方）。

## L1（Low，不改代码）：附件对象 key 对匿名读者可见

**观察（已实证）**：`publicView.Attachments` 会把对象存储的**原始 key** 下发给读者，
而 `GET /posts` 与 `GET /posts/:id` 都是免会话的公开读。

**结论：不构成可利用漏洞**，逐条排除利用链：
1. 同一条帖子本来就公开可读，响应里已有 `attachmentUrls`（短期预签名 URL）——
   知道 key **不增加任何下载能力**；
2. 全仓没有「传任意 key 换预签名 URL」的端点（`PresignGet` 只在按帖子 id 取到
   该帖 attachments 之后调用，key 不来自客户端），拿到的 key 无法用于探测或换取其它对象；
3. 上传 key 含 `crypto/rand` UUID v4，不可预测；且第一轮已加归属校验，
   key 不能用于写入面越权；
4. 软删后对象会被清理，已知 key 通常已指向不存在的对象。

**若将来要做**：读者侧只下发 `attachmentUrls`、不下发原始 key，属纵深防御（减少内部
实现细节外泄），不改变威胁模型。记录在 `internal/content/zz_logic_lead_test.go` 里，
该测试断言当前行为并打印结论，一旦字段形状变化会提醒回看。

## 本轮已审阅、确认无逻辑漏洞的面

- **公告作用域**：`audienceMatch` 三分支全 fail-closed（未登录/画像缺失均不命中）；
  `ReadableAt` 单独处理「未生效不可读」；`validateAudience` 拒掉歧义配置。
- **公告状态机**：`draft→published`、`revoked→published`、`published→published` 拒绝（
  防止重复通知），撤回幂等且不改 revision。
- **帖子反应**：`ensureTarget` 已校验留言归属（第一轮 V7 修复）；重复点赞幂等且
  只在首次建立时发通知；收藏不产生通知。
- **消息域**：`RecipientID` 恒来自会话；`MarkRead`/`Delete` 非本人返回 404（不泄露存在性）；
  `MarkAllRead` 复用同一 `ListFilter`；内部信禁自发、收件人存在性 fail-closed。
- **通知派生**：用 `(recipientId, sourceDomain, sourceObjectId)` 幂等键 upsert，
  反复触发只让 `hits++`、不产生新消息 → **无法刷爆他人通知**；自发自收双层过滤。
- **埋点**：`ev.UserID = userID` 强制用会话身份覆盖客户端自报。
- **皮肤域**：上传唯一 key 不覆盖、失败只回滚自己写的对象、`VerifyPack` 校验清单与逐文件
  sha256；`Detail`/`Download` 不校验"已激活"是**符合契约**的（`docs/tech/皮肤包上传下载服务契约.md`
  明确把 `/skins/:packId/:version/pack` 设计成免会话公开读，且 `Pack` 模型本身没有发布状态字段）。
- **presence**：userID 只来自 `school.CtxUserID`，请求体不含 userID，无法伪造他人状态。
- **分页游标**：无签名但只参与排序比较，PG 侧全参数化；只能伪造翻页位置，不构成越权。
