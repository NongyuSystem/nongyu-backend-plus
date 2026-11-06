# 旧农屿 WebVPN 迁移调研

> 本文是 `/research` 产物：**只列一手事实与可选解释，不做迁移决策、不拍板**。维护者原话需求为「迁移旧农屿，然后主要先迁移 webvpn 这个大壳，这样：先看看如何搞这个」。「webvpn 这个大壳」是口语，本笔记只负责查清它在旧仓库里可能对应哪些真实实体。
>
> **选址理由**：本仓库（新农屿 Go 后端）暂无独立 research 目录；`README.md:34` 约定 `docs/tech/` 放「技术方案（需要时）」，且该目录当前只有 `.gitkeep`（`docs/tech/.gitkeep`，`glob docs/tech/*` 实测）。本调研是迁移前的技术事实核查，按现有约定就近放 `docs/tech/`，并在开头说明。

## 调研范围与来源

本次只读两处磁盘位置，未访问网络、未读任何二手转述：

- **新仓库**（下称 `@NEW/`）：`D:\IT\program\web-program\nongyu-system\apps\nongyu-backend-plus\`
- **旧农屿 monorepo**（下称 `@OLD/`）：`D:\IT\program\web-program\nongyu-system\`

> **包含关系更正（2026-11-06，R3 核查后）**：`@NEW/` 物理上就是 `@OLD/apps/nongyu-backend-plus/`，是**同一目录**，不是两个磁盘实体。它是嵌套在 monorepo `apps/` 下的**独立 Git 仓库**（自带 `.git/`，remote = `NongyuSystem/nongyu-backend-plus.git`）而父库 remote = `NongyuSystem/nongyu-system.git`；父库 `.gitignore:95-96` 写明「新农屿后端：独立 Git 仓库（嵌套在 apps/ 下，由自身仓库管理，父仓库不跟踪）」并忽略该目录，`git -C @OLD ls-files apps/nongyu-backend-plus` 返回空，`docs/common/项目结构总览.md:52,55-56` 亦印证。故下文「两处目录」应理解为「同一路径、两套 Git 仓库上下文」。详见 [同名backend-plus与旧迁移先例核查.md](同名backend-plus与旧迁移先例核查.md)。[来源: @OLD/.gitignore:95-96；@OLD/docs/common/项目结构总览.md:52,55-56；@NEW/.git/config:8-10；@OLD/.git/config:11-13；本会话 `git rev-parse --show-toplevel` 与 `Resolve-Path` 实测]

为让每条结论可回溯，引用统一采用 `[来源: @OLD/路径:行号]` 或 `[来源: @NEW/路径:行号]` 格式；路径相对各自根目录，行号为该文件实际行号（由带行号的读取工具或 grep 给出）。

检索方法（均为仓内一手文件）：

- `glob` / `ls` 列目录：`@OLD/apps`、`@OLD/packages`、`@OLD/docs`、`@NEW/**`。[来源: @OLD/apps 目录列举；@OLD/packages 目录列举；@NEW/glob `**/*`]
- `grep` 关键词：`WebVPN|webvpn|WEBVPN`、`大壳|壳|Shell|网关|统一入口|容器`、`CAS|SSO`，限定 `*.md` 与 `*.ts`。[来源: 本会话 grep 结果]
- `read` / `ops_read_files` 逐文件带行号读取（见下文各处 `[来源]`）。

被排除的数据：`@OLD/node_modules/**`（第三方依赖，非一手项目文档）、`@OLD/**/dist/**`（构建产物）。

---

## 一手事实

### 问题 1：旧农屿里 WebVPN 相关的一手实体清单

结论先行：**旧农屿里所有 WebVPN 实现都落在「客户端 RN + 一个纯 TS 工具包」这一层，没有 Node 后端 / 独立部署的 WebVPN 服务，也没有服务端代理入口。** 依据：

- `@OLD/apps/nongyu-node-server/src` 全量 grep `webvpn|WebVPN|WebVpn|WEBVPN` = **0 命中**。[来源: 本会话 grep `@OLD/apps/nongyu-node-server/src` 无匹配]
- `@NEW/**` 全量 grep `WebVPN|CAS|SSO|迁移|壳` = **0 命中**（新仓库完全没有 WebVPN/迁移痕迹）。[来源: 本会话 grep `@NEW` 无匹配]

#### 1.1 文档层（规格 / 探索）

| 实体 | 路径 | 职责 | 状态 |
| --- | --- | --- | --- |
| 探索笔记 | `@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md` | 实测川农 WebVPN（网瑞达 wengine）产品形态、编码算法、两套 CAS appid、Cookie/SSO 行为、三套方案 A/B/C 与风险 | 标题「川农 WebVPN 探索」[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:1]；产品 = 网瑞达 wengine 无客户端 HTTPS 反向代理（实测 2026-10-30）[来源: 同文件:13] |
| 规格 | `@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md` | 把探索结论定为「传输层 + 系统 Cookie 罐」实施规格：直连优先、失败切 WebVPN、只改写两个 host、票据存 SecureStore | 状态「已按 2026-10-31 选型实施」[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:3-5] |

#### 1.2 源码层：`packages/nongyu-tool-jiaowu/core/webvpn/`（独立 TS 包，运行时在客户端）

- 包身份：`name = nongyu-tool-jiaowu`，`type: module`，`main = ./dist/index.js`，依赖 `axios / buffer / crypto-js / iconv-lite`，RN 加载 `dist/`。[来源: @OLD/packages/nongyu-tool-jiaowu/package.json:2,13-14,30-35；@OLD/packages/nongyu-tool-jiaowu/TECH.md:46]
- `core/webvpn/encode.ts`：**WebVPN URL 改写核心**。`WEBVPN_ORIGIN = https://webvpn.sicau.edu.cn` [来源: 同文件:11]；`WEBVPN_KEY = "wrdvpnisthebest!"`（key=iv）[来源: 同文件:14]；`WEBVPN_KEY_HEX` 固定前缀 [来源: 同文件:17]；`encodeVpnHost` 用 AES-128-CFB（`segment_size=128`）加密主机名、只取主机名长度 [来源: 同文件:71-112]；`encodeWebVpnUrl` 把 http(s) 绝对地址改写成 `https://webvpn.sicau.edu.cn/{scheme}/{key_hex}{host_cipher}{path}{query}`，已是 webvpn 域的原样返回 [来源: 同文件:118-127]。注释明确「只加密 host，path 与 query 保持明文」[来源: 同文件:4]。
- `core/webvpn/transport.ts`：**传输模式开关**。`JiaowuTransportMode = "direct" | "webvpn"`，默认 `direct` [来源: 同文件:10,15]；`REWRITE_HOSTS` 只含 `jiaowu.sicau.edu.cn`、`auth.sicau.edu.cn` 两个 [来源: 同文件:12-13]；`rewriteOutboundUrl` 仅 webvpn 模式且 host 命中才改写 [来源: 同文件:43-47]；注释「直连是默认，只有登录页探测到教务不可达，或冷启动恢复了 WebVPN 票据时，才切到 webvpn」[来源: 同文件:4]。
- `core/webvpn/encode.selfcheck.ts`：本地自检，写死两个 host 的期望密文，不发网络请求。[来源: 同文件:2-9]

> 说明：用户背景里猜的 `packages/nongyu-tool-jiaowu/core/webvpn/` 路径**确实存在**（glob 首次因路径写法未命中，后用 `find` 确认）。[来源: 本会话 `find packages/nongyu-tool-jiaowu` 输出]

#### 1.3 源码层：CAS / e 认证登录换票（与 WebVPN 是两件事，但耦合在同一包）

- `core/login/cas.ts`：**统一身份认证（川农 e 认证 / CAS）登录模块**，标题与背景写明「川农 e 认证 / CAS」，链路 `captcha → /api/v1/cas/login → callbackUrl?ticket=`；教务 appid `b54a2f27-…` 来自 `caslogin.asp` 的 302 `Location`，`CAS_SERVICE_URL = https://jiaowu.sicau.edu.cn/jiaoshi/aspsso/caslogin.asp`。[来源: @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:1-13,29-41]
- 同一文件里**也有 WebVPN 相关能力，但只是「换票/探测」而非「URL 改写」**：`WEBVPN_PORTAL_APPID = "aea49a21-…"`（门户 appid，注释「不能和教务 appid 混用」）[来源: 同文件:679-680]；`WEBVPN_PORTAL_SERVICE = https://webvpn.sicau.edu.cn/login?cas_login=true` [来源: 同文件:682-683]；`probeDirectJiaowu()` 短超时探测教务首页 [来源: 同文件:690-703]；`prepareJiaowuTransport()` 可达则 direct 否则 webvpn [来源: 同文件:705-713]；`loginWebVpnPortal()` 用门户 appid 换 wengine 票据 [来源: 同文件:738-804]。
- 门户与教务是**两套 CAS app，不可混用**：门户 `aea49a21-…` / service `webvpn…/login?cas_login=true`，教务 `b54a2f27-…` / service 真站 `caslogin.asp`。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:15,150-151]
- 包导出面 `index.ts` 把 `prepareJiaowuTransport / loginWebVpnPortal / WEBVPN_PORTAL_APPID` 与 CAS 登录函数一起对外导出，注明「prepareJiaowuTransport 直连可达则直连，否则切 WebVPN」「loginWebVpnPortal 校外：先登 WebVPN 门户」。[来源: @OLD/packages/nongyu-tool-jiaowu/index.ts:19-35]

#### 1.4 源码层：RN App 侧（客户端壳层调用方）

- `apps/nongyu-rn-app/src/modules/jiaowu/auth/webvpnTicket.ts`：**HttpOnly 票据的持久化/恢复**。`captureWebVpnTicket()` 登录成功后从系统 Cookie 罐抄进 `expo-secure-store`（key `webvpn_ticket`）[来源: 同文件:18-35]；`restoreWebVpnTicket()` 冷启动写回 Cookie 罐并 `setJiaowuTransport("webvpn")` [来源: 同文件:37-53]；`clearWebVpnTicket()` 登出清 SecureStore、系统罐、切回 direct [来源: 同文件:55-70]。
- `apps/nongyu-rn-app/modules/nongyu-webvpn-cookie/android/.../NongyuWebVpnCookieModule.kt`：**Android 原生 Expo 模块**，通过 `java.net.CookieHandler` 读写 RN XHR 用的那只 Cookie 罐（不用 WebView 的 `CookieManager`），常量 `ORIGIN = https://webvpn.sicau.edu.cn/`、`TICKET_NAME = wengine_vpn_ticketwebvpn_sicau_edu_cn`，导出 `readTicket/writeTicket/clearTicket`。[来源: 同文件:8-15,73-76]
- 调用/依赖点：`src/api/handleAuthInvalid.ts` 登出时 `await clearWebVpnTicket()` [来源: 同文件:7,39]；`src/modules/jiaowu/hooks/useJiaowuBootstrap.ts` 冷启动 `await restoreWebVpnTicket()` [来源: 同文件:9,70]；`src/modules/jiaowu/auth/jiaowuSession.ts` 桥接 `bridgeGetWebVpnPortalCaptcha / bridgePrepareJiaowuTransport / bridgeLoginWebVpnPortal`，注释「WebVPN 门户验证码 appid 与教务不同，不能复用教务那张图」「登录页探测：校园网通则直连，否则切 WebVPN」[来源: 同文件:42-60]；`performJiaowuLogin.ts` 调 `captureWebVpnTicket / clearWebVpnTicket / bridgeLoginWebVpnPortal` [来源: 本会话 grep 命中:23,29,92,100,106,120,348]。

#### 1.5 三件事的分层结论（关键区分）

| 事项 | 落点 | 运行时位置 | 是否同一个东西 |
| --- | --- | --- | --- |
| **WebVPN URL 改写 / 传输适配** | `packages/nongyu-tool-jiaowu/core/webvpn/{encode,transport}.ts` | 客户端（RN，加载 `dist/`） | 否 |
| **CAS / e 认证登录换票（含门户 WebVPN 换票）** | `packages/nongyu-tool-jiaowu/core/login/cas.ts` + RN `jiaowuSession.ts` / `performJiaowuLogin.ts` | 客户端为主；另有一份**服务端托管自动登录** | 与 URL 改写不同层 |
| **教务内网可达性探测** | `cas.ts` 的 `probeDirectJiaowu()` / `prepareJiaowuTransport()` | 客户端（RN 登录页） | 是触发「切 WebVPN」的判断，不是代理 |

- 规格明确「**不做服务端代请求，不把 WebVPN 编成任意 URL 代理**」。[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:20]
- 探索笔记把「禁止把 WebVPN 编码 URL 做成服务端任意代理（SSRF）」列为风险，并写「服务端接受用户传入并请求任意 WebVPN 编码 URL，等于开放校内代理」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:22,329]
- 唯一与「服务端」相关的是 `apps/nongyu-node-server/src/modules/jiaowu-hosted/`：**服务端托管教务凭据 + OCR 过验证码 + CAS 自动登录**，但链路是「取图 → OCR → `POST /api/v1/cas/login` → 回投 `caslogin.asp` → ASPSESSIONID」，**没有 WebVPN、没有 URL 编码、没有代理**。[来源: @OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/autoLogin.ts:1-14,73-124；@OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/service.ts:1-10]（另：该目录 grep `webvpn` 0 命中，见上文）
- 会话载体本身也不同：WebVPN 登录态是 `webvpn.sicau.edu.cn` 上 HttpOnly 的 `wengine_vpn_ticket*`，不是与 auth 共享的 Cookie，也不是 MMKV 里的 `ASPSESSIONID*`；网关按票据在服务端补上游 Cookie。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:9,295-300,377；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:9]

#### 1.6 被谁依赖 / 对外接口

- `nongyu-tool-jiaowu` 的对外接口集中在 `index.ts`：导出 CAS 登录（`getJiaowuCaptcha / jiaowuLoginByCas / jiaowuLoginByPhone / sendJiaowuSmsCode`）、WebVPN（`prepareJiaowuTransport / loginWebVpnPortal / WEBVPN_PORTAL_APPID`）、Cookie 桥接（`getCookie / setCookie / getJiaowuTransport / setJiaowuTransport`）、错误类型、以及 13 个业务查询函数。[来源: @OLD/packages/nongyu-tool-jiaowu/index.ts:12-68]
- 已知消费方：`nongyu-rn-app`（教务模块、Agent SDK）与 `nongyu-node-server` 的 `jiaowu-hosted`（`autoLogin.ts` 直接 `import { getJiaowuCaptcha, jiaowuLoginByCas } from "nongyu-tool-jiaowu"`）。[来源: @OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/autoLogin.ts:14；@OLD/docs/forhuman/records/全局技术选型总览.md:34]；`@OLD/packages/nongyu-agent-sdk/dist/jiaowu.js`、`src/jiaowu.ts` 亦存在，但本次未逐行读取其内容，仅记录路径存在。[来源: 本会话 `find packages -iname "*jiaowu*"` 输出]

---

### 问题 2：「大壳」可能指什么

**事实：仓内不存在一个叫「webvpn 的大壳」的实体。** 全仓（含 `*.md`、`*.ts`）grep `大壳` = **0 命中**；grep `webvpn…壳` / `壳…webvpn` = **0 命中**。[来源: 本会话 grep `大壳`（@OLD 全仓、`*.md`、`*.ts` 三遍）均无匹配]

检索 `壳|Shell|网关|统一入口|容器` 得到大量命中，但语义全部指向别处。候选解释及一手证据如下：

#### 候选 A：UI「壳层」（App Shell / 页面壳）——命中最多，但与 WebVPN 无关

- `docs/forhuman/rawprds/nongyu-rn-app/Shell/` 是一个**目录名**，专门放 RN App 的 UI 壳 / Agent 对话壳 PRD。目录内 16 个文件（列举）：`底部导航PRD.md`、`全局ToastPRD.md`、`全局下雨特效预览PRD.md`、`农屿AI入口气泡提示PRD.md`、`农屿Agent*`（会话管理/交互优化/多轮上下文/工具UI/模型通道/流式Markdown/生成保活/空态/WebDetail/聊天页加载骨架屏/上下文管理 等）。[来源: 本会话 `ls @OLD/docs/forhuman/rawprds/nongyu-rn-app/Shell/`]
- 该目录代表性内容：`底部导航PRD.md` 标题「底部导航 / App Shell PRD（草稿）」，「农屿学生端需要统一的底部主导航壳，承载首页、课表、广场、我的四个主入口」[来源: @OLD/docs/forhuman/rawprds/nongyu-rn-app/Shell/底部导航PRD.md:1,7]；`农屿Agent会话管理PRD.md` 是「农屿 AI 页对话本地保存」[来源: 同目录/农屿Agent会话管理PRD.md:1,8]。
- **与 WebVPN 的关系：无。** 该目录 16 个文件 grep `WebVPN|webvpn|CAS|SSO|教务` 只有 1 条命中，且是「下雨特效」PRD 里「用教务档案中的学院/校区映射」一句，与 WebVPN 无关。[来源: 本会话 grep `@OLD/docs/forhuman/rawprds/nongyu-rn-app/Shell` 命中 `全局下雨特效预览PRD.md:5`]
- 其余「壳」命中全是 UI/原生壳：`技术选型.md:45`「Expo + Dev Client（prebuild）… 自建带原生模块的开发/生产壳」[来源: @OLD/docs/nongyu-rn-app/技术选型.md:45]；`Pushy版本管理手册.md:47-48` 区分「原生壳（APK/IPA）」与「热更包」[来源: 同文件:47-48]；Web 管理端 `AdminShell` / `PageFrame` / `JiaowuPageShell` 等。[来源: @OLD/docs/nongyu-web-admin/specs/管理端壳层与智慧助手UI对齐.md:250；@OLD/docs/nongyu-rn-app/specs/教务子页交互与UI优化.md:16-22]

#### 候选 B：WebVPN 自身是「反向代理 / 网关 / 统一入口」——有证据，但仓内不叫「壳」

- 探索笔记把川农 WebVPN 定义为「**网瑞达 wengine 无客户端 HTTPS 反向代理**」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:13]
- 多处出现「网关」一词描述 wengine：如「网关自身下发 `wengine_vpn_ticket…`」[来源: 同文件:99]、「网关按 host 保存 ASPSESSIONID」[来源: 同文件:254]、「网关用 vpn ticket 在服务端补上游 Cookie」[来源: 同文件:300]。改写行为里 `Location` 被改成 `webvpn…/https/<segment>/…` [来源: 同文件:19]。
- 但：仓内**没有**把 WebVPN 称作「壳 / 大壳 / 统一入口 / 容器」的语句；相反规格明令「不做服务端代请求，不把 WebVPN 编成任意 URL 代理」。[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:20] 这些「网关」指学校侧产品，不是农屿仓内模块。

#### 候选 C：「容器」——命中为 CSS/布局容器，与本题无关

- `@OLD/docs/forhuman/records/全局技术选型总览.md:60` 的「跨平台二进制容器（buffer）」指 npm `buffer` 包 [来源: 同文件:60]；其余「容器」命中多为「图表随容器宽度自适应」「媒体区固定宽高比容器」「页面容器」等 UI 用语。[来源: @OLD/docs/nongyu-web-admin/specs/数据大屏.md:24,66；@OLD/docs/nongyu-web-site/specs/品牌官网落地页.md:88]

#### 候选 D：把「WebVPN 传输适配」整体俗称成「壳」——无仓内证据

- 规格把实现命名为「**传输适配**」（「传输层 + 系统 Cookie 罐」），探索笔记称「传输适配层 方案 A」。[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:1,5；@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:21,202]
- 仓内**未找到**任何把整套 WebVPN 传输适配称作「壳 / 大壳 / 统一入口」的一手语句。

**小结（事实，不拍板）**：「大壳」在仓内 0 命中；若按词面最接近的候选，是 (A) RN App 的 UI/原生壳层（与 WebVPN 无关）或 (B) WebVPN 作为学校侧反向代理网关（但仓内不叫壳、且明令禁止做成服务端代理）。二者与「WebVPN」字面捆绑都缺乏一手证据，需维护者澄清（见「尚未查清」）。

---

### 问题 3：旧农屿整体可迁移单元

#### 3.1 目录清单（一手列举）

- `@OLD/apps`（8 个）：`nongyu-app-plus`、`nongyu-backend-plus`、`nongyu-go-track-server`、`nongyu-node-server`、`nongyu-node-track-server`、`nongyu-rn-app`、`nongyu-web-admin`、`nongyu-web-site`。[来源: 本会话 `ls @OLD/apps`]
- `@OLD/packages`（5 个）：`nongyu-agent-cli`、`nongyu-agent-sdk`、`nongyu-tool-jiaowu`、`nongyu-tool-second`、`nongyu-track-contract`。[来源: 本会话 `ls @OLD/packages`]
- `@OLD/docs` 顶层：`agents`、`common`、`forhuman`、`nongyu-go-track-server`、`nongyu-node-server`、`nongyu-node-track-server`、`nongyu-rn-app`、`nongyu-web-admin`、`nongyu-web-site`。[来源: 本会话 `ls @OLD/docs`]
- **更正（2026-11-06，R3 核查后）**：`@OLD/apps/nongyu-backend-plus` 与 `@NEW/` 是**同一物理目录**，不是「两个不同磁盘实体」；它同时是嵌套在 monorepo 下的**独立 Git 仓库**（两个不同 remote、父库 `.gitignore:95-96` 显式忽略、`ls-files` 为空、`docs/common/项目结构总览.md:52,55-56` 印证）。`README.md:3` 的「独立」指 Git 仓库独立，与「不复用旧后端代码」相互印证。详见 [同名backend-plus与旧迁移先例核查.md](同名backend-plus与旧迁移先例核查.md)。[来源: @OLD/.gitignore:95-96；@OLD/docs/common/项目结构总览.md:52,55-56；@NEW/go.mod:1,3,5；本会话 `git rev-parse` / `Resolve-Path` 实测]

#### 3.2 技术栈归属（一手）

- 系统组成：「RN客户端 / Web管理端 / Node业务后端（express） / Node埋点后端（由原 Go 埋点经压测迁移，Fastify + SQLite） / 一系列 sdk 和工具库」。[来源: @OLD/docs/common/农屿系统技术文档概览.md:5-9]
- 逐项目：`nongyu-node-server` = MySQL + Express + TS + pm2 + winston [来源: @OLD/docs/forhuman/records/全局技术选型总览.md:34-41]；`nongyu-web-admin` = React18 + antd + Vite8 [来源: 同文件:22-32]；`nongyu-rn-app` = React Native + TS + Expo [来源: 同文件:43-47]；`nongyu-tool-jiaowu` 依赖 iconv-lite/axios/buffer（+ crypto-js，见其 package.json）[来源: 同文件:51-60；@OLD/packages/nongyu-tool-jiaowu/package.json:30-35]。

#### 3.3 「后端能力」 vs 「客户端壳层」

- **后端能力（潜在「迁移」对象）**：`apps/nongyu-node-server`（业务后端，Express + MySQL）[来源: @OLD/docs/forhuman/records/全局技术选型总览.md:34-41]；`apps/nongyu-node-track-server`（现行埋点后端）[来源: @OLD/apps/nongyu-go-track-server/DEPRECATED.md:5；@OLD/docs/common/农屿系统技术文档概览.md:8]；`apps/nongyu-go-track-server`（已下线 Go 埋点，见 3.4）；`apps/nongyu-node-server/src/modules/jiaowu-hosted/`（服务端托管自动登录，是 Node 后端里唯一「替用户跑教务 CAS」的能力）[来源: @OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/service.ts:1-10]。
- **客户端壳层 / 工具库（非后端进程）**：`apps/nongyu-rn-app`（RN 客户端，含 App Shell 与 WebVPN 票据原生模块）、`apps/nongyu-web-admin`、`apps/nongyu-web-site`、`packages/nongyu-tool-jiaowu`（教务抓取 + WebVPN 传输适配，加载于 RN `dist/`）、`packages/nongyu-tool-second`、`packages/nongyu-agent-sdk`、`packages/nongyu-agent-cli`、`packages/nongyu-track-contract`。[来源: 本会话 `ls @OLD/apps`、`ls @OLD/packages`；@OLD/docs/common/农屿系统技术文档概览.md:5-9；@OLD/packages/nongyu-tool-jiaowu/TECH.md:46]

#### 3.4 已标记 DEPRECATED / 已迁移的历史

- `apps/nongyu-go-track-server/DEPRECATED.md`：标题「⚠️ DEPRECATED — 历史 Go 埋点实现」，「已下线」「生产 CD 不再发布」，现行实现是 `apps/nongyu-node-track-server`（Fastify + better-sqlite3），本目录「仓库保留、不删除」，「禁止把本目录当作现行源码修改」。[来源: @OLD/apps/nongyu-go-track-server/DEPRECATED.md:1-7]
- `docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md`：状态「已确认；实施中」，目标「同一职责、同一机器、同一 HTTP 契约，把实现语言从 Go 换为 Node」「永不与业务 Node 同机合并」，CD 只发 Node Track、Go 目录不再触发发布、Go 代码与文档保留。[来源: @OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:12,20,29,40,106-115]
- 新仓库 README 也点名：`apps/nongyu-go-track-server` 是「旧农屿已下线的 Go 埋点服务，仅作历史对照，不是本仓库的现行源码或依赖来源」。[来源: @NEW/README.md:84]

#### 3.5 「从旧农屿迁到新 Go 后端时，可能被称作『迁移』的对象」清单（只列名 + 依据，不选型）

1. 旧 `apps/nongyu-node-server` 的业务能力（鉴权/会话/内容/广场/教务托管等）→ 新 Go 后端重写。[来源: @OLD/docs/forhuman/records/全局技术选型总览.md:34-41；@OLD/docs/nongyu-node-server/specs/App与Admin鉴权会话策略.md:5]
2. 旧 `jiaowu-hosted` 服务端托管自动登录（服务端跑 CAS + OCR）→ 是否要在 Go 后端重建。[来源: @OLD/apps/nongyu-node-server/src/modules/jiaowu-hosted/service.ts:1-10]
3. 旧 `nongyu-tool-jiaowu` 的 WebVPN 传输适配 + CAS 登录（现为客户端 TS 包）→ 是否 / 如何进 Go 后端。**注意规格明令「不做服务端代请求，不把 WebVPN 编成任意 URL 代理」**。[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:20]
4. 旧 `nongyu-go-track-server` → 已下线、已迁到 `nongyu-node-track-server`，仅历史对照。[来源: @OLD/apps/nongyu-go-track-server/DEPRECATED.md:1-7；@NEW/README.md:84]
5. 旧 monorepo 的数据 / 表结构 / 部署链路 → 新仓库 README 明确「不复用旧后端代码、表结构与部署链路」。[来源: @NEW/README.md:3]
6. 旧 RN / Web 端的「壳层」→ 属客户端，不是后端进程。[来源: @OLD/docs/forhuman/rawprds/nongyu-rn-app/Shell/底部导航PRD.md:1,7]

---

### 问题 4：新仓库现状与硬约束

- 身份与关系：「**独立于旧农屿（`nongyu-system` monorepo）的全新仓库，不复用旧后端代码、表结构与部署链路**」。[来源: @NEW/README.md:3]；「新农屿基本不复用以前的后端实现」。[来源: @NEW/README.md:84]
- 状态：「骨架阶段。仅提供可编译、可启动、可优雅关停的最小工程结构 + `GET /healthz`，**不含任何业务逻辑、不连接数据库**」。[来源: @NEW/README.md:7]
- 技术选型：Go 1.26 [来源: @NEW/go.mod:3]；Gin v1.11.0 [来源: @NEW/go.mod:5]；模块化单体（一个仓库/module/进程）[来源: @NEW/README.md:15]；配置走环境变量 [来源: @NEW/README.md:16；@NEW/.env.example:1-10]；骨架期存储用 `internal/platform.MockStore` [来源: @NEW/README.md:17；@NEW/internal/app/app.go:32]。
- 唯一对外路由：`GET /healthz`，返回 `{"status":"ok"}`；注释「骨架阶段只挂 /healthz」「就绪探针等接入数据库后再单独添加」。[来源: @NEW/internal/server/server.go:13-28；@NEW/internal/server/server_test.go:11-23]
- 装配链：`cmd/server/main.go`「读配置 → 装配 App → 启动并优雅关停」，业务逻辑不写入口 [来源: @NEW/cmd/server/main.go:1-4,20-63]；`internal/app/app.go` 是唯一 composition root，注入 `MockStore` + `server.New()` [来源: @NEW/internal/app/app.go:1-6,29-47]；`internal/config/config.go` 仅 3 个环境变量（`ADDR`/`APP_ENV`/`SHUTDOWN_TIMEOUT_SEC`）且各有默认值 [来源: @NEW/internal/config/config.go:13-34]。
- `Store` 接口只有 `Get(ctx, key)` 一个方法，注释「骨架期故意只定义一个方法」「不是任何具体数据库的形状」。[来源: @NEW/internal/platform/store.go:13-20]
- **架构约束 5 条（长期不可破）**：①业务域之间不互相 import，协作走 `internal/app`；②业务域只依赖 `internal/platform` 接口，不直接 import 数据库驱动/S3 SDK；③进程无状态，状态外置（DB/缓存/对象存储）；④`internal/<业务域>` 自带 handler/service/repo 接口，存储实现放 `internal/platform` 或子包；⑤不提前建 `pkg/utils`、`common` 垃圾场包。[来源: @NEW/README.md:42-48]
- **SDD 流程**：「PRD → Spec → 技术方案（按需）→ 实施计划 → 编码 → 人工 Review」「骨架期只有 README 作为依据；业务接口开发前须先补 PRD 与 `docs/specs/` 下的规格，**禁止无规格直接写业务代码**」。[来源: @NEW/README.md:77-80]
- 目录约定：`docs/specs/` 规格、`docs/tech/` 技术方案（需要时）、`docs/plans/` 实施计划（需要时）；`migrations/` 单向有序、`deploy/` 部署示例，当前均为空占位（`.gitkeep`）。[来源: @NEW/README.md:30-39；@NEW/glob `docs/specs/.gitkeep`、`docs/plans/.gitkeep`、`migrations/.gitkeep`、`deploy/.gitkeep`]
- Agent 约定：issue/规格走 GitHub Issues（`gh` CLI）[来源: @NEW/docs/agents/issue-tracker.md:1-3]；五种 triage 标签 [来源: @NEW/docs/agents/triage-labels.md:5-11]；single-context 领域文档（根 `GLOSSARY.md` + `docs/adr/`），若不存在则静默跳过 [来源: @NEW/docs/agents/domain.md:5-15]。本调研按 task 要求**未创建 issue**。

> 对「迁移」有直接约束的事实：新仓库 (a) 明确不复用旧后端代码/表结构/部署链路，(b) 处于骨架期且禁止无规格写业务代码，(c) 进程无状态、业务域只依赖 `platform` 接口，(d) 目前没有任何 WebVPN/CAS/教务相关代码或依赖（`go.mod` 仅 Gin）。[来源: 上述各条]

---

### 问题 5：术语冲突点——「迁移」在本次语境下的可能含义

以下只列**含义 + 所需前提 + 与仓内硬约束的关系**，不做选择：

| 含义 | 具体所指 | 需要的前提 | 与一手事实的冲突/注意点 |
| --- | --- | --- | --- |
| ① 代码移植 | 把旧 TS/Node 代码（如 `nongyu-tool-jiaowu`、`jiaowu-hosted`）改写成 Go 落到 `@NEW/internal/<业务域>` | 需先定业务域边界；`go.mod` 现无 axios/crypto-js 对应 Go 依赖 | 新仓库「不复用旧后端代码」且语言不同（Go 1.26 vs TS）；SDD 要求先补 PRD/Spec [来源: @NEW/README.md:3,80] |
| ② 功能重写 | 在新 Go 后端按旧功能语义重新实现（WebVPN 传输适配 / CAS 登录 / 教务托管） | 需 PRD → Spec → 技术方案 → 实施计划；遵守架构约束 5 条 | 规格明令「不做服务端代请求，不把 WebVPN 编成任意 URL 代理」「SSRF」[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:20；@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:329] |
| ③ 数据迁移 | 把旧库的业务数据/表结构搬到新库 | 需旧表结构定义、目标库就绪 | 新仓库「不复用旧后端…表结构」、骨架期 `MockStore` 无 DB、`migrations/` 为空 [来源: @NEW/README.md:3,7,17；@NEW/migrations/.gitkeep] |
| ④ 部署切换 | 把流量从旧服务切到新 Go 服务 | 需部署链路、入口、健康检查契约 | 新仓库「不复用旧…部署链路」、`deploy/` 为空、仅 `/healthz` [来源: @NEW/README.md:3,7；@NEW/deploy/.gitkeep；@NEW/internal/server/server.go:18] |
| ⑤ 把旧壳整体包一层 | 用新 Go 后端做统一入口/代理，把旧 WebVPN/教务能力包在后面 | 需明确「壳」是哪个实体、是否允许服务端代请求 | 仓内**不存在**叫「webvpn 大壳」的实体；且规格明令「不做服务端代请求，不把 WebVPN 编成任意 URL 代理」[来源: 本会话 grep `大壳` 0 命中；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:20] |
| ⑥ 历史 Go→Node 那种「同契约换实现」 | 类比 `埋点服务迁移至Node.md`：同一 HTTP 契约换语言 | 需一份「HTTP 契约真相」文档 + 切换/回滚窗口 | 那份迁移的对象是**已下线**的 `nongyu-go-track-server`，且决议「仓库保留、不删除」；新仓库 README 声明它不是现行源码/依赖来源 [来源: @OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:9-12,20；@OLD/apps/nongyu-go-track-server/DEPRECATED.md:1-7；@NEW/README.md:84] |

---

## 术语与歧义

- **WebVPN**：在旧仓内有两个不同侧面。(a) 学校侧产品「网瑞达 wengine 无客户端 HTTPS 反向代理」[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:13]；(b) 农屿客户端侧「传输适配层」（URL 编码 + 单域 Cookie + 门户换票）[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:21,202；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:1,5]。二者不是同一个东西，前者不属于农屿代码。
- **CAS / e 认证**：川农统一身份认证。门户 appid `aea49a21-…`（WebVPN）与教务 appid `b54a2f27-…` 两套不可混用。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:15,150-151；@OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:35,680]
- **票据（ticket）**：两类不同。WebVPN 网关会话 `wengine_vpn_ticketwebvpn_sicau_edu_cn`（HttpOnly，webvpn 域）[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:18,295]；教务业务会话 `ASPSESSIONID*` [来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:296]。CAS 的 `ticket=ST:…` 又属统一认证回调参数。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:135,142]
- **SSO**：探索笔记指「同一套川农 e 认证的 SSO」，明确「不是 Cookie 头跨域共享」。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:20,283]
- **壳 / Shell**：仓内一致指 UI 壳（App Shell、页面壳、管理端壳、Agent 对话壳）或 RN 原生安装包壳，不指 WebVPN。[来源: @OLD/docs/forhuman/rawprds/nongyu-rn-app/Shell/底部导航PRD.md:1,7；@OLD/docs/nongyu-rn-app/技术选型.md:45；@OLD/docs/nongyu-rn-app/Pushy版本管理手册.md:47-48]
- **网关**：仓内指学校 wengine 反向代理，不是农屿自建服务。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:99,254,300]
- **迁移**：旧仓里已发生过一次有文档的「迁移」（Go 埋点 → Node 埋点，同契约换实现），与本次「迁到新 Go 后端」方向相反且对象不同。[来源: @OLD/docs/nongyu-go-track-server/specs/埋点服务迁移至Node.md:1,20；@NEW/README.md:84]
- **`nongyu-backend-plus` 同名**：`@OLD/apps/nongyu-backend-plus` 与 `@NEW/` 是同一物理目录、嵌套独立 Git 仓库；「不同实体」只成立于「两套 Git 仓库上下文」，不成立於「两个磁盘目录」。[来源: @OLD/.gitignore:95-96；@OLD/docs/common/项目结构总览.md:52,55-56；同名backend-plus与旧迁移先例核查.md]

---

## 尚未查清 / 需要维护者拍板

以下均为**仓内查不到或需要人确认**，不推测填充：

1. **「webvpn 这个大壳」到底指什么**：全仓 `大壳` 0 命中，无法从一手材料确定。候选见问题 2（UI 壳 / wengine 反向代理网关 / 整套传输适配俗称），但都缺「大壳」字面证据，需维护者指认具体对象。[来源: 本会话 grep `大壳` 0 命中]
2. **「迁移旧农屿」的范围**：是整体重写旧 `nongyu-node-server` 业务，还是只迁某一块能力（如 WebVPN / 教务托管），还是数据/部署切换，本笔记只列 6 种含义（问题 5），未查到任何仓内文档定义本次「迁移」的范围。[来源: @NEW/README.md:3,7,80（仅声明「不复用旧后端」）；@NEW 全仓 grep `迁移|WebVPN|CAS|SSO|壳` 0 命中]
3. **WebVPN 是否要进新 Go 后端、以什么形态进**：旧规格明令「不做服务端代请求，不把 WebVPN 编成任意 URL 代理」[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:20]，而新仓库是后端进程；若要在后端承载，与这条既有边界如何协调，仓内无结论，需维护者拍板。
4. ~~`@OLD/apps/nongyu-backend-plus` 的内容与关系~~ **已查清（2026-11-06，R3）**：与本仓是同一物理目录、嵌套独立 Git 仓库，与 `README.md:3` 无冲突；详见 [同名backend-plus与旧迁移先例核查.md](同名backend-plus与旧迁移先例核查.md)。剩余未查清的是父库历史是否曾跟踪过另一同名目录、远端服务器状态、实际切换时间线。[来源: 同名backend-plus与旧迁移先例核查.md「尚未查清」]
5. **`packages/nongyu-agent-sdk/src/jiaowu.ts` 等消费方细节**：仅确认路径存在，未逐行读取其对 `nongyu-tool-jiaowu` 的依赖内容。[来源: 本会话 `find packages -iname "*jiaowu*"`]
6. **SDD 前置产物**：新仓库 `docs/specs/`、`docs/plans/` 当前均为 `.gitkeep` 空占位，若要做任何业务「迁移」，按 README 须先补 PRD/Spec。[来源: @NEW/README.md:77-80；@NEW/glob `docs/specs/.gitkeep`、`docs/plans/.gitkeep`]
7. **真账号链路未验收项**（探索笔记自列，可能影响任何重写）：WebVPN ticket TTL/空闲超时未公布、SSO 优化未验证、RN 不跑 `main.js` 时 `ASPSESSIONID*` 是否仅靠 `Set-Cookie` 进系统罐未拆响应头验证。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:22,219,295,324-330]

---

## 不适用说明

- 本文**不是**迁移方案、不是技术选型、不是实施计划，不含任何「应该迁 / 不该迁 / 怎么迁」的结论；按 `/research` 与 task 约束，只做事实核查与歧义陈列。[来源: 本任务约束]
- 未创建 issue、未建分支、未修改除本笔记外的任何文件、未做迁移决策。[来源: 本任务约束；`@NEW` 工作区仅新增 `docs/tech/旧农屿WebVPN迁移调研.md`]
- 未引用任何二手转述（博客、问答、外部网页）；所有结论均来自 `@OLD/` 与 `@NEW/` 仓内一手文件（文档、源码、规格、配置、目录列举）。[来源: 本会话全部工具调用]
- 探索笔记中带「实测 2026-10-30 / 2026-10-31」的记录，是维护者当时的**一手实测记录**，本文按一手来源引用；其中「未测 / 未验证 / 环境相关」的条目已如实标注，未被当作已证实结论。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:13,154,173,219,320-330]
- 全局 `~/.dsh/AGENTS.md` 要求「任务完成后提交 git」等，但本任务明确要求「不要修改除上述笔记外的任何文件、不要创建 issue、不要建分支」，且本文为 research 子代理产物；是否提交由父代理/维护者决定，本子代理未执行 git 操作。[来源: 本任务约束；全局 AGENTS.md]
