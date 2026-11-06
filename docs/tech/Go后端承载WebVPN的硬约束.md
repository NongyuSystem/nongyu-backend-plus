# Go 后端承载 WebVPN 的硬约束

> **选址理由**：与已完成的 `/research` 事实底稿 `@NEW/docs/tech/旧农屿WebVPN迁移调研.md` 同目录；`@NEW/README.md:34` 约定 `docs/tech/` 放「技术方案（需要时）」，本笔记是 R2 调研的硬约束核查产物，按现有约定就近放 `docs/tech/`，并在开头说明。[来源: @NEW/docs/tech/旧农屿WebVPN迁移调研.md:5；@NEW/README.md:32-35]

## 调研范围与来源

- 本笔记**只列一手来源可核实的约束与风险**。一手来源限定为：仓内源码、规格、原始探索笔记、登录页须知摘录。[来源: 本任务约束]
- 路径前缀：`@NEW/` = `D:\IT\program\web-program\nongyu-system\apps\nongyu-backend-plus\`；`@OLD/` = `D:\IT\program\web-program\nongyu-system\`。行号由带行号读取工具给出；`本会话 grep/glob` 为本次会话对仓内文件的检索结果，可复现。[来源: @NEW/docs/tech/旧农屿WebVPN迁移调研.md:11-14]
- 已读 `@NEW/docs/tech/旧农屿WebVPN迁移调研.md`（217 行）作为定位底稿；**结论一律回溯到下列一手文件，不以该底稿作为 `[来源]`**。[来源: @NEW/docs/tech/旧农屿WebVPN迁移调研.md:1-217]
- 读取/检索的一手文件清单：
  - `@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md`（377 行，原始探索笔记，含登录页须知摘录）[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:1-7,334]
  - `@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md`（56 行，规格）[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:1-5]
  - `@OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/`（`autoLogin.ts` / `service.ts` / `routes.ts` / `ocr/client.ts`）[来源: 本会话 glob 该目录列出 7 个文件；@OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/autoLogin.ts:1；service.ts:1；routes.ts:1；ocr/client.ts:1]
  - `@OLD/packages/nongyu-tool-jiaowu/core/webvpn/encode.ts`、`transport.ts`、`core/login/cas.ts`[来源: 本会话 grep `@OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:26,500,522,535`]
  - `@NEW/README.md`、`go.mod`、`internal/config/config.go`、`internal/platform/store.go`、`internal/server/server.go`[来源: @NEW/README.md:1-84；@NEW/go.mod:1-39；@NEW/internal/config/config.go:1-56；@NEW/internal/platform/store.go:1-34；@NEW/internal/server/server.go:1-28]
  - `@NEW/docs` 目录列举（`docs/specs/.gitkeep`、`docs/plans/.gitkeep`、`docs/tech/.gitkeep`、`docs/agents/*`、事实底稿一篇）[来源: 本会话 glob `@NEW/docs/**/*`]
- **不引用二手转述**：探索笔记 `:104-105`、`:352-353` 列出的外部仓库链接（HollowMan6 / neugo）仅作为编码算法旁证出现，本文不作为任何结论的来源；登录页须知按探索笔记 `:334` 的「合规摘录（登录页须知，实测 2026-10-30）」作为维护者实测摘录引用。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:104-105,334,352-353]
- 维护者已拍板「WebVPN 能力功能重写进新 Go 后端；旧规格边界**有条件放宽**，具体条件由另一张票定」，此为**任务给定背景**；仓内未找到该放宽条件的任何规格、ADR 或文档定义。[来源: 本任务给定背景；@NEW/docs 目录列举中无相关规格]
- 「仓内未找到」的含义：在上述 `@NEW/` 与 `@OLD/` 指定一手文件与目录列举中未检索到；不表示外部世界不存在。[来源: 本任务约束]

---

## 硬约束清单

### 1. SSRF：「任意 URL 代理」为什么危险、边界禁止的到底是哪一种行为

- **规格原文**：「不做服务端代请求，不把 WebVPN 编成任意 URL 代理。」该条位于规格的「边界」小节。[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:18-20] 同小节还要求「不把票据或密码写进日志、埋点、MMKV」「不探测或公布学校未公开的空闲超时，不做保活轮询」。[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:21,24]
- **探索笔记风险条目原文**：「禁止把 WebVPN 编码 URL 做成服务端任意代理（SSRF）」；「若服务端接受用户传入并请求任意 WebVPN 编码 URL，等于开放校内代理」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:22；@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:329] 该条在「未验证项 / 风险」表中标为 ⚠。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:316-319,329]
- **边界禁止的确切行为**：由 `:329` 的措辞可精确拆出两个要件——(a) 服务端**接受用户传入**的 URL（而非固定目标）；(b) 请求的是**任意** WebVPN 编码 URL（而非固定白名单）。二者合起来即「把 WebVPN 编码能力开放成对任意目标 host 的服务端代理」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:329] 规格 `:20` 另外以「不做服务端代请求」表述了同一边界的更宽一层。[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:20]
- **为什么危险（均由一手事实推出）**：
  - 川农 WebVPN 是「网瑞达 wengine 无客户端 HTTPS 反向代理」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:13] 未登录时编码后的 jiaowu 资源一律 `302 → /login`，持有有效会话后「网关已放行」，可进入学籍页。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:17,171,266-268]
  - 编码函数接受任意 http(s) 绝对地址：`encodeWebVpnUrl` 用正则取 scheme/host/path/query，把任意 host 加密进 `https://webvpn.sicau.edu.cn/{scheme}/{key_hex}{host_cipher}{path}{query}`，path 与 query 保持明文，且仅当 host 已是 `webvpn.sicau.edu.cn` 时原样返回。[来源: @OLD/packages/nongyu-tool-jiaowu/core/webvpn/encode.ts:4,115-127；@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:30-39]
  - 因此「服务端接受用户传入并请求任意 WebVPN 编码 URL」等价于把「用已认证网关会话访问任意校内 host」的能力开放出去，即 SSRF / 开放校内代理。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:329；@OLD/packages/nongyu-tool-jiaowu/core/webvpn/encode.ts:118-127]
- **既有客户端实现是靠固定白名单避开该风险的**：`REWRITE_HOSTS` 只含 `jiaowu.sicau.edu.cn`、`auth.sicau.edu.cn` 两个 host；`rewriteOutboundUrl` 仅在模式为 `webvpn` 且 host 命中该集合时才改写，否则原样返回。[来源: @OLD/packages/nongyu-tool-jiaowu/core/webvpn/transport.ts:12-13,43-47] 规格同样写明「只改写 `jiaowu.sicau.edu.cn` 与 `auth.sicau.edu.cn`。已经是 webvpn 域的 URL 不动」「`service` / `callbackUrl` / 302 `Location` 若指向真站，再编码一次，禁止回落到校外不可达的真站」。[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:29-30]
- **对后端的直接限制**：新 Go 后端「在限定条件下代请求 WebVPN 编码 URL」本身即服务端代请求，会正面撞上 `spec:20` 与 `探索:329` 这两条既有边界。[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:20；@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:329；本任务给定背景] 仓内未找到任何关于「限定条件下可放宽」的既有定义——该条件属另一张票（票 #4），本笔记不设定。[来源: 本任务给定背景；@NEW/docs 目录列举中无相关规格]

### 2. 会话 / 票据：HttpOnly、会话级、网关在服务端补 Cookie，对「进程无状态 / 状态外置」意味着什么

一手事实（票据属性与网关行为）：

- Cookie 名 `wengine_vpn_ticketwebvpn_sicau_edu_cn`，`Domain=webvpn.sicau.edu.cn`，`HttpOnly`，常见 `Secure`，**无 Max-Age（会话级）**；另有 `show_vpn` / `show_faq` 为 `Max-Age=604800`。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:18] 表中记为「会话（响应无 Max-Age；curl jar `expires=0`）」，且「无有效值则 jiaowu 等资源 302 登录」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:179]
- 票据「HttpOnly，`cookieStore` 和原生 `document.cookie` 都看不到」；「匿名下发时无 `Max-Age`（会话级）。登录后脚本仍看不到，不能确认有没有被改成持久 Cookie」；「关浏览器可能丢；服务端空闲超时未在 `/user/info` 和门户 JS 里公布」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:295] 「ticket 是 HttpOnly，JS 读不走，也不能抄进 MMKV」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:304]
- 登录态「是 HttpOnly 的 `wengine_vpn_ticket*`，不是和 auth 共享的 Cookie，也不是 MMKV 里的 `ASPSESSIONID*`」；「网关按这张票在服务端补教务 Cookie」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:9,377；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:9] 决定性对照：门户页 fetch 编码后的学籍页、`credentials: "include"`、不带 `ASPSESSIONID*` 仍返回 200，结论是「网关用 vpn ticket 在服务端补上游 Cookie，客户端不必自己再贴 `ASPSESSIONID*`」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:300]
- `ASPSESSIONID*` 不在浏览器真实罐，存在网关的 host 罐（`/wengine-vpn/cookie?method=get&host=jiaowu.sicau.edu.cn&scheme=https&path=/`），「跟 vpn ticket 走」；IIS 会话超时「这次没等，不能写成 20 分钟」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:270,281,296] 上游 Cookie「按 **host** 存在网关罐里，由脚本同步到当前页 `document.cookie`」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:281]
- 票据失效与教务会话失效是两种不同信号：「302 到 `/login` 表示 ticket 已死；正文是「登录超时」表示 ticket 还在、教务会话已死」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:310] 注销入口 `/logout` 会拆掉票据。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:302]
- 规格侧：登录成功后「用原生模块读出票据，写入 SecureStore；冷启动写回系统 Cookie 罐」；「票据失效（302 到 `/login`）则重新登录，不定时刷学籍页」；「不把票据或密码写进日志、埋点、MMKV」；「不探测或公布学校未公开的空闲超时，不做保活轮询」。[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:16,21,24]

新仓侧一手事实：

- 架构约束第 3 条：「**进程无状态**：状态全部外置（DB / 缓存 / 对象存储），以便水平扩副本」；第 2 条：「业务域只依赖 `internal/platform` 定义的接口」。[来源: @NEW/README.md:45-46] `README:47` 要求业务域「不直接 import 数据库驱动、S3 SDK 等具体实现」。[来源: @NEW/README.md:45,47]
- 骨架期「不含任何业务逻辑、不连接数据库」；存储用 `internal/platform.MockStore`；`Store` 接口只有 `Get(ctx, key)` 一个方法，「骨架期故意只定义一个方法……不是任何具体数据库的形状」；`store.go` 注释「骨架期无数据库」。[来源: @NEW/README.md:7,17；@NEW/internal/platform/store.go:3-8,13-20]
- 配置仅 3 个环境变量（`ADDR` / `APP_ENV` / `SHUTDOWN_TIMEOUT_SEC`），且注释「后续新增配置时在此集中定义，禁止在业务包里直接读 `os.Getenv`」；`go.mod` 只有 Gin 一项直接依赖。[来源: @NEW/internal/config/config.go:2,13-20；@NEW/go.mod:3-5]

事实推论（只列推论，不给方案）：

- **推论 1**：WebVPN 登录态是有状态会话，且状态归学校网关持有：网关按票据在服务端补上游 Cookie，票据是访问该会话的索引；票据本身是会话级、无 `Max-Age`、空闲超时未公布、可能被 `/logout` 拆掉。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:18,295,300,302,377]
- **推论 2**：该票据按用户区分、且 HttpOnly，仓内一手材料记载的唯一读/持有点是 RN 原生模块与系统 Cookie 罐；规格还禁止把票据写进日志、埋点、MMKV。[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:16,21；@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:295,304,309] 仓内未找到新 Go 后端获取或持有该票据的任何接口、代码或规格定义。[来源: @NEW/README.md:7,42-48；@NEW/internal/config/config.go:13-20；@NEW/go.mod:3-5]
- **推论 3**：按 `@NEW/README.md:46`「状态全部外置」的分类，票据属于「状态」；但仓内未找到新仓对「WebVPN 票据 / 网关会话状态外置到 DB / 缓存 / 对象存储」的任何接口、规格或代码定义——`Store` 接口只有 `Get(ctx, key)`，配置里没有任何 WebVPN / 票据相关项。[来源: @NEW/README.md:46；@NEW/internal/platform/store.go:13-20；@NEW/internal/config/config.go:13-20]
- **推论 4**：`README:46` 的目的是「以便水平扩副本」，而票据的生命周期（会话级、关浏览器可能丢、空闲超时未公布、失效靠解析 302 或「登录超时」正文判别）与进程副本不绑定；仓内未找到新仓对「票据与副本 / 用户绑定关系」的任何定义。[来源: @NEW/README.md:46；@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:295,310]
- **推论 5**：后端不执行 `main.js`（探索笔记明确「原生 RN 请求不会执行 `main.js`」「接入时必须自己编码 `service` / `callbackUrl`，不能依赖 `main.js`」），而网关补上游 Cookie 的观察结论来自浏览器 `main.js` 挂钩环境；仓内未找到「后端形态下等价机制」的一手记录。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:184,269,281,285,300]

### 3. 合规与风控：登录页须知、共享出口 IP 误伤、自动化客户端

- **登录页须知摘录（一手摘录，实测 2026-10-30）**：「为校外访问图书馆电子资源、信息门户等开通 WebVPN；支持电脑/手机/PAD；**账号仅限本人**；禁止连续、系统、集中、批量下载；触发频率限制会加入黑名单。」[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:334]
- **风险表「学校对第三方客户端 ToS」**：「须知：账号仅限本人；恶意下载拉黑（1 分钟内间隔 &lt;15s 基础限制；连续 5 次 → 1 小时限制）」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:327]
- **风险表「账号锁定 / 验证码风控」**：「与直连相同；WebVPN 出口 IP 可能共享，误伤风险」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:328]
- **结论摘要风险条目**：「须知强调账号仅限本人、防恶意下载会拉黑；自动化/非浏览器客户端可能触发风控」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:22]
- **操作层既有约束**：「探测优先打门户 `GET /user/info`，不要为了续期去刷学籍页。须知禁止连续、批量下载」；「精确空闲超时仍然未知。不要发明一个定时打教务的保活」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:311-312] 规格同样「不做保活轮询」「不探测或公布学校未公开的空闲超时」。[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:24]
- **其他须知边界**：「不支持 RDP/FTP 等；与教务 HTML 无关」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:332] 方案 A 缺点列「完整链路未用真账号验收；ToS 风险」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:217]
- **对后端代请求的事实推论**：后端进程是「自动化/非浏览器客户端」形态，而探索笔记把该形态列为可能触发风控的条目；「集中、批量下载」被须知禁止，而多用户请求汇聚到同一后端进程/出口 IP 与 `探索:328`「WebVPN 出口 IP 可能共享，误伤风险」指向同一类共享出口形态。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:22,328,334；@NEW/README.md:15,46] 仓内未找到新仓对「账号仅限本人」在服务端代请求场景下如何体现的任何规格或校验定义。[来源: @NEW/docs 目录列举中无相关规格；@NEW/internal/config/config.go:13-20]
- 旧 `jiaowu-hosted` 先例自身已承认该风险：`service.ts:20` 会话缓存注释「避免高频自动登录触发风控」，路由挂 `loginRateLimit`，并有「只能托管当前登录账号本人的教务凭据」校验。[来源: @OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/service.ts:20-21,100-108；routes.ts:12,46,69] 这是针对**其自身 CAS 自动登录**的措施，不是 WebVPN 代请求的措施。[来源: @OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/service.ts:1-10；本会话 grep 该目录 `webvpn|WebVPN|wengine|encode|proxy|代理|URL|vpn|VPN` 仅命中 `ocr/client.ts:15,40` 的 `fileURLToPath`]

### 4. 先例边界：`jiaowu-hosted` 碰了什么、没碰什么，为什么不推翻 WebVPN 边界

**碰了什么（服务端托管 CAS + OCR 自动登录）**：

- 模块定位：「服务端代管凭据 → 自动换票」「用户只需提供一次学号密码，之后服务端可自行完成整个 CAS 登录拿到教务会话」。[来源: @OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/autoLogin.ts:1-8]
- 链路：「取图 → OCR → `POST /api/v1/cas/login` → 回投 `caslogin.asp`（含两跳重定向）→ ASPSESSIONID」。[来源: @OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/autoLogin.ts:7-8] 代码里循环调用 `getJiaowuCaptcha()` → `recognizeCaptcha()` → `jiaowuLoginByCas()`，成功后返回 `{ success: true, cookie }`，`cookie` 注释为「教务系统 ASP 会话 Cookie（`ASPSESSIONID*`）」。[来源: @OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/autoLogin.ts:44-45,73-124]
- 编排：开启托管「校验一次密码 → AES-256-GCM 加密入库」，取会话「解密密码 → OCR 过验证码 → CAS 登录 → 返回教务 ASP Cookie」，关闭托管「直接删行，不留副本」。[来源: @OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/service.ts:1-10,93-133,146-184]
- 它从 `nongyu-tool-jiaowu` 只导入 `getJiaowuCaptcha` 与 `jiaowuLoginByCas` 两个函数。[来源: @OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/autoLogin.ts:14]
- 对外路由路径固定为 `/hosted`（GET/POST/DELETE）、`/hosted/login`、`/hosted/invalidate`，请求体 schema 只有 `studentNo`（9 位数字）与 `password`，且都挂 `requireAppAuth`、部分挂 `loginRateLimit`；「只回 cookie，绝不回 password_cipher 或明文」。[来源: @OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/routes.ts:24-30,33-84,50]

**没碰什么**：

- **不含 WebVPN URL 编码 / 任意 URL 代理**：本会话对该目录 grep `webvpn|WebVPN|wengine|encode|proxy|代理|URL|vpn|VPN` = 仅 2 条命中，且都是 `node:url` 的 `fileURLToPath`（`ocr/client.ts:15,40`），与 WebVPN 无关。[来源: 本会话 grep `@OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/`；@OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/ocr/client.ts:14-16,40]
- **不切换 WebVPN 传输模式、不调用门户换票**：本会话 grep `Transport|rewriteOutbound|setJiaowuTransport|loginWebVpnPortal|encodeWebVpn|axios|fetch\(|http` 在该目录 = 无匹配；`transport.ts` 默认模式为 `direct`，`rewriteOutboundUrl` 仅在 `mode === "webvpn"` 且 host 命中白名单时改写。[来源: 本会话 grep `@OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/` 无匹配；@OLD/packages/nongyu-tool-jiaowu/core/webvpn/transport.ts:15,43-47]
- **不接受用户传入任意 URL**：路由与请求体均为固定结构，不含 URL 参数。[来源: @OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/routes.ts:24-30,33-84]
- **会话载体是 `ASPSESSIONID*`，不是 webvpn 域的 ticket**：探索笔记明确 WebVPN 登录态是 `webvpn.sicau.edu.cn` 上 HttpOnly 的 `wengine_vpn_ticket*`，「不是和统一认证共享的 Cookie，也不是 MMKV 里的 `ASPSESSIONID*`」；两者分属「WebVPN 门户」与「教务系统」两套 CAS 应用。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:9,148-151,377；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:9；@OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/autoLogin.ts:44-45]

**为什么不算推翻 WebVPN 边界**：

- 规格边界禁止的是「不做服务端代请求，不把 WebVPN 编成任意 URL 代理」[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:20]，探索笔记把风险精确表述为「服务端接受用户传入并请求任意 WebVPN 编码 URL」[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:329]。`jiaowu-hosted` 服务端代的是固定 CAS 登录链路、固定目标 host、固定请求体，返回的是教务业务 Cookie，既不编码 WebVPN URL、也不接受用户传入 URL、也不持有 `wengine_vpn_ticket*`。[来源: @OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/autoLogin.ts:7-8,14,73-124；routes.ts:24-30]
- 该模块既没有实现边界所禁止的行为，也没有对 WebVPN 边界作出任何放宽或例外声明；仓内未找到「因 `jiaowu-hosted` 存在而放宽 WebVPN 边界」的语句。[来源: 本会话 grep `@OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/` 无 WebVPN 命中；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:18-24；@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:316-334]
- 因此它是「服务端托管 CAS + OCR 自动登录」的先例，不是「服务端 WebVPN 代理」的先例；WebVPN 与教务 CAS 登录分属不同层。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:13,148-151；@OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/service.ts:1-10]

### 5. 真账号未验收项：未验证什么、影响后端重写的哪一处假设

> 本小节只列「未验证什么」与「该未验证项对应的后端重写假设」，不裁决、不补方案、不替维护者定条件。

| # | 未验证什么（一手原话 / 状态） | 会影响后端重写的哪一处假设 |
| --- | --- | --- |
| 5.1 | **VPN ticket TTL / 空闲超时未公布**：仅知「会话（响应无 `Max-Age`）」「服务端空闲超时未在 `/user/info` 和门户 JS 里公布」「关浏览器可能丢」；「精确空闲超时仍然未知。不要发明一个定时打教务的保活」；风险表标为「部分：仅知会话 Cookie、无 Max-Age」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:18,179,295,312,326] | 假设「后端可预先知道票据何时失效，并据此确定会话缓存时长 / 失效重试时机 / 保活策略」。仓内一手材料明确否定该假设的前提：超时未公布、禁止保活轮询、禁止探测学校未公开的空闲超时。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:295,312,326；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:24] |
| 5.2 | **SSO 优化未验证**：「若 WebVPN 登录后 auth 侧会话能被代理回传，教务 CAS 或可少一次密码；仍可能要验证码。**不可作为当前设计前提**」；「已有 CAS 会话能否免验证码进入 WebVPN：**未用真账号验证**」；「本次是先登 WebVPN 再进教务，不是先直连 auth 再进 WebVPN」；「双 CAS 是否可合并为一次验证码」仅一次浏览器观察成立。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:154,219,324-325] | 假设「后端完成门户换票后，教务 CAS 可稳定免第二次图形验证码 / 可合并为一次登录」。仓内明确把 SSO 标为「不可作为当前设计前提」，且反向顺序未验证、合并仅单次观察。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:154,219,324-325；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:23] |
| 5.3 | **`ASPSESSIONID*` 是否仅靠 `Set-Cookie` 进系统罐未拆响应头验证**：「RN 不跑 `main.js` 时，`ASPSESSIONID*` 是否仅靠响应 `Set-Cookie` 进入系统 Cookie 罐仍未拆响应头验证」；风险表「RN Cookie 罐 ⚠：HttpOnly + 跨域；已有直连坑（读不到 `Set-Cookie`）在 webvpn 单域下可能缓解也可能更复杂」；「上游 `Set-Cookie` 域名/路径改写」已否定「改写进真实罐」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:22,322,330] | 假设「后端只需转发 / 解析 `Set-Cookie` 即可维持教务业务会话」。仓内相反的一手结论是「网关用 vpn ticket 在服务端补上游 Cookie，客户端不必自己再贴 `ASPSESSIONID*`」、`ASPSESSIONID*` 存在网关 host 罐而非真实罐，且「仅靠 Set-Cookie」这一条明确未拆响应头验证。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:22,270,281,296,300,322,330] |
| 5.4 | **证书钉扎风险**：「证书钉扎 ⚠：WebVPN 证书 ≠ jiaowu/auth 直连」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:331] | 假设「后端以默认 TLS 信任链请求 `webvpn.sicau.edu.cn` 即可，无需额外证书处理」。仓内未找到新仓对证书 / 信任链 / 钉扎的任何配置项或规格（配置仅 3 个环境变量，`go.mod` 仅 Gin）。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:331；@NEW/internal/config/config.go:13-20；@NEW/go.mod:3-5] |
| 5.5 | **`/wengine-vpn/cookie` 读写语义仅部分验证**：get 必须带 `host`+`scheme`+`path`，否则 200 空体；「set 未主动调用」；缺参数的 200 空体「不能据此判断罐是空的」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:183,272-278,323] | 假设「后端可通过该端点可靠读 / 写上游 Cookie」。仓内记录是「部分」验证、set 未调用、空体不可判空。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:272-278,323] |
| 5.6 | **真账号登录后票据形态变化未定**：「登录后脚本仍看不到，不能确认有没有被改成持久 Cookie」；IIS 会话超时「这次没等，不能写成 20 分钟」；「真账号完整 WebVPN 登录 ✅」但「未记录 ticket 值，TTL 仍未知」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:295-296,320] | 假设「票据在登录后仍是同一张会话级 Cookie、且教务业务会话有已知固定时长」。两条都被仓内标为未确认 / 未等待，不能写成定值。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:295-296,320] |
| 5.7 | **链路整体未用真账号验收 / ToS 风险 / 环境相关**：方案 A 缺点「完整链路未用真账号验收；ToS 风险」；本机直连 jiaowu 200「**环境相关**；产品文档仍按『校外常不可达』设计」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:173,217] | 假设「校外直连不可达是稳定前提、且完整链路已验收」。仓内把直连结果标为环境相关、把链路标为未验收。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:173,217] |

补充说明（一手）：探索笔记 `:283` 明确「共享的是 WebVPN 网关会话加上同一套川农 e 认证的 SSO」「auth 主机罐为空；教务罐里只有 `ASPSESSIONID*`」，`:302` 说明 `main.js` 里每秒一次的 `setInterval` 是同步 `localStorage`、不是心跳，`:285` 说明「接入时必须自己编码 `service` / `callbackUrl`，不能依赖 `main.js`」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:283,285,302]

### 6. 新仓自身的硬约束：哪些会在「后端承载 WebVPN」时直接构成限制

| # | 新仓一手约束 | 对「后端承载 WebVPN」的直接限制（事实陈述） |
| --- | --- | --- |
| 6.1 | 「**独立于旧农屿 monorepo 的全新仓库**，不复用旧后端代码、表结构与部署链路」；「新农屿基本不复用以前的后端实现」。[来源: @NEW/README.md:3,84] | WebVPN 编码算法与会话行为的既有实现只在旧客户端 TS 包（`encode.ts` / `transport.ts` / `cas.ts`）与旧 Node 模块里；按此约束不能直接移植旧代码。仓内未找到新仓对 AES-128-CFB 编码、白名单、票据处理的任何实现或规格。[来源: @OLD/packages/nongyu-tool-jiaowu/core/webvpn/encode.ts:71-127；transport.ts:12-13；@NEW/README.md:3,7,84；@NEW/go.mod:3-5] |
| 6.2 | 「骨架阶段……**不含任何业务逻辑、不连接数据库**」；存储用 `internal/platform.MockStore`；`Store` 接口只有 `Get(ctx, key)`；`store.go` 注释「骨架期无数据库」；唯一对外路由 `GET /healthz`、返回 `{"status":"ok"}`，「骨架阶段只挂 /healthz」。[来源: @NEW/README.md:7,17；@NEW/internal/platform/store.go:3-8,13-20；@NEW/internal/server/server.go:12-19,24-28] | 后端承载 WebVPN 属业务逻辑与对外业务路由；票据 / 会话状态若无 DB 则只能落在当前 `MockStore`（内存、单方法 `Get`）或未定义位置。仓内未找到新仓对 WebVPN 业务域、handler / service / repo 或出站 HTTP 能力的任何定义。[来源: @NEW/README.md:7,29,42-48；@NEW/internal/platform/store.go:13-20；@NEW/internal/server/server.go:12-19] |
| 6.3 | 架构约束 5 条（长期不可破）：①业务域之间不互相 import，协作走 `internal/app`；②业务域只依赖 `internal/platform` 接口，不直接 import 数据库驱动 / S3 SDK 等具体实现；③**进程无状态，状态外置**；④`internal/<业务域>` 自带 handler / service / repo 接口，存储实现放 `internal/platform` 或子包；⑤不提前建 `pkg/utils`、`common` 垃圾场包。[来源: @NEW/README.md:42-48] | ①WebVPN 若成业务域，与 auth / 教务等域的协作必须经装配层，仓内未找到该业务域定义（`README:29` 写明 `<业务域>` 待 PRD/Spec 到位后新增如 `skin/`、`config/`、`auth/`）；②代请求所需的「出站到 `webvpn.sicau.edu.cn`」能力，`platform` 当前只定义 `Store.Get`；③见第 2 节推论 1–4；④仓内未找到 webvpn 域的 handler/service/repo 定义；⑤编码器等复用代码不能落在公共工具包，仓内未找到既定落点。[来源: @NEW/README.md:29,42-48；@NEW/internal/platform/store.go:13-20；@NEW/internal/server/server.go:12-19] |
| 6.4 | SDD：「PRD → Spec → 技术方案（按需）→ 实施计划 → 编码 → 人工 Review」「骨架期只有 README 作为依据；业务接口开发前须先补 PRD 与 `docs/specs/` 下的规格，**禁止无规格直接写业务代码**」。[来源: @NEW/README.md:77-80] | 后端承载 WebVPN 属业务接口开发；本会话 glob `@NEW/docs/**/*` 显示 `docs/specs/`、`docs/plans/` 仅为 `.gitkeep` 空占位，`docs/tech/` 仅有 `.gitkeep` 与事实底稿一篇。仓内未找到 WebVPN 相关的 PRD / Spec / 技术方案 / 实施计划。[来源: @NEW/README.md:30-39,77-80；本会话 glob `@NEW/docs/**/*`；@NEW/docs/tech/旧农屿WebVPN迁移调研.md:162,206] |
| 6.5 | 配置走环境变量，「后续新增配置时在此集中定义，禁止在业务包里直接读 `os.Getenv`」；`Config` 仅 3 个字段且各有安全默认值；`go.mod` 只有 Gin 一项直接依赖。[来源: @NEW/README.md:16；@NEW/internal/config/config.go:2,11-20；@NEW/go.mod:3-5] | WebVPN 网关地址、编码密钥、票据相关配置在仓内均无既有项；`go.mod` 中无 WebVPN / CAS / 教务相关依赖（Gin 之外无直接依赖）。仓内未找到任何出站 HTTP 客户端实现或接口定义。[来源: @NEW/internal/config/config.go:13-20；@NEW/go.mod:3-39] |
| 6.6 | 「**模块化单体**（一个仓库 / 一个 module / 一个进程，内部按业务能力强分包）」；「以便水平扩副本」。[来源: @NEW/README.md:15,46] | 代请求集中在一个进程 / 多副本，与第 3 节「出口 IP 可能共享，误伤风险」「账号仅限本人」「禁止集中、批量下载」指向同一形态。仓内未找到新仓对「按用户隔离代请求 / 出口」的任何定义。[来源: @NEW/README.md:15,46；@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:328,334] |

---

## 约束之间的张力

> 只陈述「A 与 B 互相制约」的事实，不裁决、不定「有条件放宽」的具体条件（属票 #4）、不设计架构、不做决策。

- **张力 1**：规格「不做服务端代请求，不把 WebVPN 编成任意 URL 代理」[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:20] 与「新 Go 后端在限定条件下代请求 WebVPN 编码 URL」[来源: 本任务给定背景] 互相制约：后者本身即服务端代请求。仓内未找到放宽条件的定义（票 #4 负责）。[来源: @NEW/docs 目录列举中无相关规格；本任务给定背景]
- **张力 2**：探索笔记 SSRF 风险「服务端接受用户传入并请求任意 WebVPN 编码 URL，等于开放校内代理」[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:329] 与既有客户端「`REWRITE_HOSTS` 只含两 host、仅命中才改写」[来源: @OLD/packages/nongyu-tool-jiaowu/core/webvpn/transport.ts:12-13,43-47；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:29-30] 互相制约：前者禁止的形态是「任意 / 用户传入」，后者是既有实现避开该形态的做法。仓内未找到后端形态下「可接受 URL 集合」的定义。[来源: @NEW/docs 目录列举中无相关规格]
- **张力 3**：「票据会话级、空闲超时未公布、不做保活轮询、不定时刷学籍页」[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:295,311-312,326；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:24] 与「进程无状态、状态外置、以便水平扩副本」[来源: @NEW/README.md:46] 互相制约：票据既是会话级状态又不允许用定时保活维持，而后端副本与票据生命周期不绑定。仓内未找到二者的调和定义。[来源: @NEW/internal/config/config.go:13-20；@NEW/internal/platform/store.go:13-20]
- **张力 4**：「票据 HttpOnly、JS 读不走、不把票据写进日志/埋点/MMKV、账号仅限本人」[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:295,304,334；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:21] 与「状态全部外置（DB / 缓存 / 对象存储）」[来源: @NEW/README.md:46] 互相制约：票据需被后端持有才能代请求，又受既有边界限制不得落入若干常见载体；外置位置仓内未找到。[来源: @NEW/internal/platform/store.go:13-20；@NEW/docs 目录列举中无相关规格]
- **张力 5**：「账号仅限本人、禁止连续/系统/集中/批量下载、出口 IP 可能共享误伤」[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:327-328,334] 与「一个进程 / 模块化单体、水平扩副本」[来源: @NEW/README.md:15,46] 互相制约：多账号请求汇聚与须知禁止的「集中、批量」及共享出口形态指向同一处。仓内未找到新仓对代请求的账号归属 / 频率约束定义。[来源: @NEW/README.md:42-48；@NEW/internal/config/config.go:13-20]
- **张力 6**：「网关按 vpn ticket 在服务端补上游 Cookie、客户端不必贴 `ASPSESSIONID*`」[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:300,377] 与「RN / 后端不执行 `main.js`、必须自己编码 `service` / `callbackUrl`」[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:184,285；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:9] 互相制约：前者是浏览器 `main.js` 挂钩环境下观察到的一手结论，后者是非浏览器客户端的既有约束；仓内未找到后端形态下的等价机制记录。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:269-281,300]
- **张力 7**：「不复用旧后端代码 / 表结构 / 部署链路」+「禁止无规格直接写业务代码」[来源: @NEW/README.md:3,77-80] 与「WebVPN 编码 / 会话行为的既有实现只在旧客户端 TS 包，且真账号链路仍有未验收项」[来源: @OLD/packages/nongyu-tool-jiaowu/core/webvpn/encode.ts:71-127；@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:219,320-331] 互相制约：功能重写需重新验证算法与会话行为，而 `docs/specs/`、`docs/plans/` 为空占位。[来源: 本会话 glob `@NEW/docs/**/*`]
- **张力 8**：「业务域只依赖 `internal/platform` 接口，不直接 import 具体实现」[来源: @NEW/README.md:45,47] 与「代请求需要出站到 `webvpn.sicau.edu.cn` 的具体传输能力」互相制约：`platform` 当前只定义 `Store.Get(ctx, key)`，`go.mod` 仅有 Gin 一项直接依赖。[来源: @NEW/internal/platform/store.go:13-20；@NEW/go.mod:3-5；@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:13]

---

## 未验证项

本节汇总第 5 节表格中与「后端重写」直接相关的未验证项；每条的「影响哪一处假设」见第 5 节表格右列，此处不重复。

1. **VPN ticket TTL / 空闲超时精确值**：仓内仅知会话 Cookie、无 `Max-Age`；`/user/info` 与门户 JS 未公布空闲超时；「精确空闲超时仍然未知」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:18,179,295,312,326]
2. **登录后票据是否变成持久 Cookie**：「登录后脚本仍看不到，不能确认有没有被改成持久 Cookie」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:295]
3. **SSO 优化 / 双 CAS 合并 / 反向顺序**：「SSO 优化（未验证）……不可作为当前设计前提」；「已登录 CAS 是否可无验证码进 WebVPN 未用真账号验证」；双 CAS 合并仅一次浏览器观察。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:154,219,324-325]
4. **`ASPSESSIONID*` 是否仅靠 `Set-Cookie` 进系统罐**：「RN 不跑 `main.js` 时……仍未拆响应头验证」；「RN Cookie 罐 ⚠ HttpOnly + 跨域……可能缓解也可能更复杂」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:22,330]
5. **`/wengine-vpn/cookie` 读写语义**：get 需 `host`+`scheme`+`path`、set 未主动调用、空体不可判空。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:183,272-278,323]
6. **证书钉扎**：「WebVPN 证书 ≠ jiaowu/auth 直连」；仓内未找到新仓对证书 / 信任链的配置或规格。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:331；@NEW/internal/config/config.go:13-20；@NEW/go.mod:3-5]
7. **完整链路真账号验收 / ToS / 直连环境相关性**：方案 A 缺点「完整链路未用真账号验收；ToS 风险」；直连 jiaowu 200「环境相关，不能当作稳定校外可达」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:173,217]
8. **教务业务会话时长**：「IIS 自己的会话超时这次没等，不能写成 20 分钟」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:296]
9. **后端形态的等价机制**：仓内未找到「Go 后端不执行 `main.js` 时，如何完成 `service` / `callbackUrl` 编码与上游 Cookie 维持」的任何一手记录或规格；探索笔记只要求「接入时必须自己编码 `service` / `callbackUrl`，不能依赖 `main.js`」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:184,285,300；@NEW/docs 目录列举中无相关规格]
10. **放宽条件**：维护者已拍板「有条件放宽」，仓内未找到该条件的任何规格、ADR 或文档定义；本笔记不替维护者设定。[来源: 本任务给定背景；@NEW/docs 目录列举中无相关规格]

---

## 不适用说明

- 本文**不是**架构设计、不是迁移方案、不是技术选型、不是实施计划，**不定「有条件放宽」的具体条件**（属票 #4）、不做决策、不做裁决；只列一手来源可核实的约束与风险，以及「A 与 B 互相制约」的事实。[来源: 本任务约束]
- 本文**不引用二手转述**：外部仓库链接（HollowMan6 / neugo）只出现在探索笔记 `:104-105`、`:352-353`，未作为结论来源；事实底稿 `@NEW/docs/tech/旧农屿WebVPN迁移调研.md` 仅用于定位，未作为 `[来源]`。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:104-105,352-353；本会话检索结果]
- 「仓内未找到」一律指在「调研范围与来源」列出的一手文件与目录列举中未检索到，不表示外部世界不存在；`本会话 grep/glob` 为可复现的仓内检索。[来源: 本任务约束]
- 维护者拍板内容（功能重写进新 Go 后端、边界有条件放宽）仅作为任务给定背景引用，仓内无对应规格；凡涉及该背景处均已标注，不与仓内一手事实混淆。[来源: 本任务给定背景]
- 未修改除本文件外的任何文件；未调用任何 `deck_*` 工具、未创建 issue、未建分支、未执行 `git commit`、未访问网络、未使用共享浏览器、未读取真账号口令 / 票据值（探索笔记本身也声明「不记录口令与 Cookie 值」）。[来源: 本任务约束；@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:6-7]
- 全局 `~/.dsh/AGENTS.md` 要求「任务完成后提交 git」，但本任务明确禁止 git 操作；是否提交由父代理 / 维护者决定，本子代理未执行。[来源: 本任务约束]
