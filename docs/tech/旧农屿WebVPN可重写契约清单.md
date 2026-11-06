# 旧农屿 WebVPN / CAS 可重写契约清单

> 本文是 `/research` 产物（票 #2 研究子任务）：**只把 `@OLD/packages/nongyu-tool-jiaowu` 里 WebVPN 与 CAS 相关的对外契约逐项列成可被 Go 重写对照的清单，不做设计、不拍板范围、不给 Go 接口建议**。
>
> **选址理由**：与事实底稿 `@NEW/docs/tech/旧农屿WebVPN迁移调研.md:5` 同目录；`@NEW/README.md:34` 约定 `docs/tech/` 放「技术方案（需要时）」，该目录现有 `.gitkeep` 与上述事实底稿两份（`glob docs/tech/*` 实测），本清单按仓内习惯就近放 `docs/tech/`。
>
> 引用格式 `[来源: @OLD/路径:行号]`（路径相对 `@OLD/` 根，行号为实际文件行号）；每条结论紧跟来源；查不到写「仓内未找到」，不推测。本清单只依赖仓内一手来源（源码、规格、探索/技术文档、原生模块、`dist` 类型声明），不引用二手转述（`@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:104-105` 的外部 GitHub 链接仅在该文档内被提及，本清单不采信其内容）。

## 调研范围与来源

磁盘上只读两处目录，未访问网络：

- `@NEW/` = `D:\IT\program\web-program\nongyu-system\apps\nongyu-backend-plus\`（新 Go 后端，本清单仅作为产物落点）
- `@OLD/` = `D:\IT\program\web-program\nongyu-system\`（旧农屿 monorepo）

本清单实际逐行读取的一手文件（`read` / `grep` 带行号）：

| 类别 | 文件 |
| --- | --- |
| 包导出面 | `@OLD/packages/nongyu-tool-jiaowu/index.ts`、`core/login/index.ts`、`core/utils/index.ts`、`core/jiaowu/index.ts`、`dist/index.d.ts` |
| WebVPN 编码 / 传输 | `core/webvpn/encode.ts`、`core/webvpn/transport.ts`、`core/webvpn/encode.selfcheck.ts` |
| CAS 登录 | `core/login/cas.ts`（805 行全读） |
| 错误 / 请求 / Cookie | `core/utils/errors.ts`、`core/utils/request.ts`、`core/utils/cookie.ts` |
| 技术 / 规格 / 探索 | `@OLD/packages/nongyu-tool-jiaowu/TECH.md`、`package.json`、`@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md`、`@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md` |
| RN 侧调用方 | `apps/nongyu-rn-app/src/modules/jiaowu/auth/{jiaowuSession.ts,performJiaowuLogin.ts,webvpnTicket.ts}`、`apps/nongyu-rn-app/src/modules/jiaowu/components/JiaowuLoginForm.tsx`、`apps/nongyu-rn-app/src/api/handleAuthInvalid.ts`、`apps/nongyu-rn-app/src/modules/jiaowu/hooks/useJiaowuBootstrap.ts` |
| Android 原生模块 | `apps/nongyu-rn-app/modules/nongyu-webvpn-cookie/android/src/main/java/expo/modules/nongyuwebvpncookie/NongyuWebVpnCookieModule.kt` |

说明：`@OLD/node_modules/**` 与 `@OLD/**/dist/**`（除 `dist/index.d.ts` 这一类型声明外）按底稿约定不作为一手项目来源。[来源: @OLD/docs/tech/旧农屿WebVPN迁移调研.md 对应 `@NEW/docs/tech/旧农屿WebVPN迁移调研.md:22`] 新仓侧本清单未读取任何业务源码（骨架期无 WebVPN/CAS 代码，见 `@NEW/docs/tech/旧农屿WebVPN迁移调研.md:156-165`）。

---

## 对外契约清单

### 1. `index.ts` 导出面

包身份：`name = nongyu-tool-jiaowu`，`type: module`，`main/module = ./dist/index.js`，`types = ./dist/index.d.ts`，`exports["."]` 只暴露 `dist`；依赖 `axios / buffer / crypto-js / iconv-lite`。[来源: @OLD/packages/nongyu-tool-jiaowu/package.json:2,13-20,30-35]

`index.ts` 共 6 组导出（行 12-68）；`dist/index.d.ts:1424` 是构建产物的实际导出清单。[来源: @OLD/packages/nongyu-tool-jiaowu/index.ts:12-68；@OLD/packages/nongyu-tool-jiaowu/dist/index.d.ts:1424]

| 分类 | 导出符号 | 签名 / 形状 | 职责 | 来源 |
| --- | --- | --- | --- | --- |
| 旧登录（已废弃） | `setLoginData` | `(user: string, pwd: string) => void` | 写内存 `LOGIN_DATA`，"开局调此函数便于其他鉴权函数自动重登录" | @OLD/packages/nongyu-tool-jiaowu/index.ts:13-17；dist/index.d.ts:129 |
| 旧登录（已废弃） | `jiaowuLogin` | `(user?: string, pwd?: string) => Promise<{success;message?;cookie?}>` | 【已废弃】口令登录，2026-10-23 起教务取消该入口 | @OLD/packages/nongyu-tool-jiaowu/index.ts:15；core/login/index.ts:251-253 |
| 旧登录（已废弃） | `clearLoginData` | `() => void` | 登出清空 `LOGIN_DATA` 与内存 Cookie | @OLD/packages/nongyu-tool-jiaowu/index.ts:16；core/login/index.ts:43-47 |
| **CAS 登录** | `getJiaowuCaptcha` | `(appid?: string) => Promise<JiaowuCaptchaResult>` | 取图形验证码（默认教务 appid）；返回 `{success:true;result:{uuid,image}}` 或 `{success:false;message}` | @OLD/packages/nongyu-tool-jiaowu/index.ts:21；core/login/cas.ts:236；dist/index.d.ts:65 |
| **CAS 登录** | `jiaowuLoginByCas` | `(input: JiaowuCasLoginInput) => Promise<JiaowuCasLoginResult>` | 账密 + 图形验证码登录，成功 `{success:true;cookie}` | @OLD/packages/nongyu-tool-jiaowu/index.ts:22；core/login/cas.ts:500；dist/index.d.ts:66 |
| **CAS 登录** | `jiaowuLoginByPhone` | `(input: JiaowuPhoneLoginInput) => Promise<JiaowuCasLoginResult>` | 手机短信验证码登录 | @OLD/packages/nongyu-tool-jiaowu/index.ts:23；core/login/cas.ts:634；dist/index.d.ts:100 |
| **CAS 登录** | `sendJiaowuSmsCode` | `(input: JiaowuSendSmsInput) => Promise<{success:true} \| {success:false;message;needRefreshCaptcha?}>` | 发送登录短信验证码 | @OLD/packages/nongyu-tool-jiaowu/index.ts:24；core/login/cas.ts:602-604 |
| **WebVPN** | `prepareJiaowuTransport` | `() => Promise<"direct" \| "webvpn">` | 直连可达则直连，否则切 WebVPN | @OLD/packages/nongyu-tool-jiaowu/index.ts:25；core/login/cas.ts:708；dist/index.d.ts:115 |
| **WebVPN** | `loginWebVpnPortal` | `(input: JiaowuCasLoginInput) => Promise<WebVpnPortalLoginResult>` | 校外：先登 WebVPN 门户 | @OLD/packages/nongyu-tool-jiaowu/index.ts:26；core/login/cas.ts:742；dist/index.d.ts:120 |
| **WebVPN（常量）** | `WEBVPN_PORTAL_APPID` | `const = "aea49a21-6b26-4e8f-bcfc-80e3de591c93"` | 门户 appid | @OLD/packages/nongyu-tool-jiaowu/index.ts:27；core/login/cas.ts:680；dist/index.d.ts:102 |
| **CAS 类型** | `JiaowuCaptcha` / `JiaowuCaptchaResult` | `{uuid:string;image:string}` / 成功失败联合 | `image` 是 `data:image/png;base64,...` | @OLD/packages/nongyu-tool-jiaowu/index.ts:28-29；core/login/cas.ts:80-85,113-115 |
| **CAS 类型** | `JiaowuCasLoginInput` | `{studentId;password;captchaCode;captchaUuid}` | 账密 + 验证码入参 | @OLD/packages/nongyu-tool-jiaowu/index.ts:30；core/login/cas.ts:88-97 |
| **CAS 类型** | `JiaowuCasLoginResult` | `{success:true;cookie:string} \| {success:false;message;needRefreshCaptcha?}` | 成功回 ASP Cookie 串 | @OLD/packages/nongyu-tool-jiaowu/index.ts:31；core/login/cas.ts:100-110 |
| **CAS 类型** | `JiaowuPhoneLoginInput` / `JiaowuSendSmsInput` | `{phone;smsCode}` / `{phone;captchaCode;captchaUuid}` | 短信登录 / 发短信入参 | @OLD/packages/nongyu-tool-jiaowu/index.ts:32-33；core/login/cas.ts:581-594 |
| **WebVPN 类型** | `WebVpnPortalLoginResult` | `{success:true;needJiaowuCaptcha:boolean} \| {success:false;message;needRefreshCaptcha?}` | `needJiaowuCaptcha` 表示票据已建立但教务会话还要再验一次 | @OLD/packages/nongyu-tool-jiaowu/index.ts:34；core/login/cas.ts:686-688 |
| **Cookie / 传输桥接** | `getCookie` / `setCookie` | `() => string` / `(cookie:string)=>void` | 读写包内全局内存 Cookie（非系统罐） | @OLD/packages/nongyu-tool-jiaowu/index.ts:38；core/utils/cookie.ts:18-29；dist/index.d.ts:255-261 |
| **Cookie / 传输桥接** | `getJiaowuTransport` / `setJiaowuTransport` | `() => JiaowuTransportMode` / `(next)=>void` | 读写模块级传输模式 | @OLD/packages/nongyu-tool-jiaowu/index.ts:38；core/webvpn/transport.ts:20-29 |
| 其他 | `resolveJiaowuUserAgent` / `setJiaowuUserAgent` | `() => string` / `(ua:string)=>void` | 解析/覆盖 UA | @OLD/packages/nongyu-tool-jiaowu/index.ts:40；dist/index.d.ts:272-276 |
| 其他 | `attachJiaowuHttpLogger` / `JiaowuHttpLogEvent` | `(cb)=>void` / 结算事件类型 | 开发态 HTTP 日志挂载点 | @OLD/packages/nongyu-tool-jiaowu/index.ts:43；core/utils/request.ts:96-107,179 |
| **错误** | `JIAOWU_CAMPUS_NETWORK_HINT` | `const = "教务网迁入内网，可尝试连接校园网使用农屿"` | 超时固定提示 | @OLD/packages/nongyu-tool-jiaowu/index.ts:47；core/utils/errors.ts:6 |
| **错误** | `JiaowuSessionExpiredError` / `isJiaowuSessionExpiredError` | `Error` 子类（name 同名） / 类型守卫 | 会话过期需用户重新输验证码 | @OLD/packages/nongyu-tool-jiaowu/index.ts:48-49；core/utils/errors.ts:15-28 |
| **错误** | `isJiaowuTimeoutError` / `resolveJiaowuErrorMessage` | `(error)=>boolean` / `(error,fallback?) => string` | 超时判定 / 统一 message | @OLD/packages/nongyu-tool-jiaowu/index.ts:50-51；core/utils/errors.ts:33-67 |
| **业务查询（13 个）** | `getRankInfo` / `getCourseInfo` / `getPlanInfo` / `getTeacherCourseInfoByName` / `getCompetitionInfo` / `getClassroomCourseInfoByName` / `getPersonalInfo` / `getScoreInfo` / `getExamInfo` / `getProgressInfo` / `getNoticeInfo` / `getTeachingNoticeInfo` | 见 `dist/index.d.ts` | 成绩/课表/培养方案/教师课表/竞赛/教室课表/个人信息/考试/进度/通知等 | @OLD/packages/nongyu-tool-jiaowu/index.ts:55-67；core/jiaowu/index.ts:6-32；dist/index.d.ts:702-1424 |

注：`index.ts:55-67` 注释写「13 个业务查询函数」，实际该 `export {}` 块列出 12 个符号（12 行注释）；`core/jiaowu/index.ts` 另导出 `getClassroomInfo` / `getTeacherInfo` 但未进根 `index.ts`。事实底稿第 79 行「13 个业务查询函数」是沿注释的说法，逐行核对为根导出面 12 个。[来源: @OLD/packages/nongyu-tool-jiaowu/index.ts:55-67；core/jiaowu/index.ts:6-32；@NEW/docs/tech/旧农屿WebVPN迁移调研.md:79]

**关键事实**：`index.ts` 不导出 `encodeWebVpnUrl` / `encodeVpnHost` / `WEBVPN_ORIGIN` / `WEBVPN_KEY` / `WEBVPN_KEY_HEX` / `aesCfb128Encrypt` / `REWRITE_HOSTS` / `rewriteOutboundUrl` / `probeDirectJiaowu` / `WEBVPN_PORTAL_SERVICE` / `decryptCasData` 等——WebVPN 编码与传输是包内实现细节，只有 `prepareJiaowuTransport` / `loginWebVpnPortal` / `WEBVPN_PORTAL_APPID` / `getJiaowuTransport` / `setJiaowuTransport` 从根导出面暴露。[来源: @OLD/packages/nongyu-tool-jiaowu/index.ts:12-68；dist/index.d.ts:1424；core/webvpn/encode.ts:8-17（无对根 re-export）；core/webvpn/transport.ts:8]

---

### 2. `core/webvpn/encode.ts`：AES-128-CFB 主机名编码

| 契约 | 精确值 / 行为 | 来源 |
| --- | --- | --- |
| 文件职责 | "只加密 host。path 与 query 保持明文。缺 key 的 hex 前缀时网关会 302 到登录页" | @OLD/packages/nongyu-tool-jiaowu/core/webvpn/encode.ts:1-5 |
| `WEBVPN_ORIGIN` | `"https://webvpn.sicau.edu.cn"`（网关根地址） | 同文件:10-11 |
| `WEBVPN_KEY` | `"wrdvpnisthebest!"`（key = iv，16 字节） | 同文件:13-14 |
| `WEBVPN_KEY_HEX` | `"77726476706e69737468656265737421"`（key 的 ASCII hex，路径段固定前缀） | 同文件:16-17 |
| `BLOCK` | `16`（内部常量） | 同文件:19 |
| `aesCfb128Encrypt(plain, keyText, ivText)` | 导出函数；`plain.length % 16 !== 0` 抛 `"CFB-128 明文必须按 16 字节对齐"`；`key = CryptoJS.enc.Utf8.parse(keyText)`，`feedback = asciiBytes(ivText)`；逐 16 字节块 `aesEncryptBlock(feedback, key)`（AES ECB + NoPadding）产生 keystream，异或得 cipherBlock，`feedback = cipherBlock` | 同文件:61-91 |
| `encodeVpnHost(host)` | 导出函数；`paddedLength = ceil(host.length/16)*16`；ASCII 字节写入 `padded`，从 `raw.length` 起用 `0x30`（字符 `'0'`）右填充；调用 `aesCfb128Encrypt(padded, WEBVPN_KEY, WEBVPN_KEY)`；返回 `WEBVPN_KEY_HEX + toHex(cipher.slice(0, raw.length))`（**只取 host 长度**，小写 hex） | 同文件:100-113 |
| `encodeWebVpnUrl(rawUrl)` | 导出函数；正则 `^(https?):\/\/([^/?#]+)([^?#]*)(\?[^#]*)?` 匹配，非 http(s) 原样返回；`scheme` 小写、`host = hostPort.replace(/:\d+$/, "")`（**去端口**）、无 path 补 `/`、保留 query；若 `host === "webvpn.sicau.edu.cn"` **原样返回**；否则 `${WEBVPN_ORIGIN}/${scheme}/${encodeVpnHost(host)}${path}${query}` | 同文件:115-128 |
| 已是 webvpn 域不动 | 判断仅按 `host === "webvpn.sicau.edu.cn"`（去端口后） | 同文件:126 |
| `asciiBytes` / `wordArrayToBytes` / `bytesToWordArray` / `toHex` | 内部辅助，避免 RN 无 `TextEncoder` / Node `Buffer` | 同文件:24-58,93-98 |

`encode.selfcheck.ts` 写死两个 host 的期望密文（**不含 `WEBVPN_KEY_HEX` 前缀**，因为 `got = segment.slice(WEBVPN_KEY_HEX.length)`）：[来源: @OLD/packages/nongyu-tool-jiaowu/core/webvpn/encode.selfcheck.ts:6-14]

| host | 期望 host 密文 hex | 来源 |
| --- | --- | --- |
| `auth.sicau.edu.cn` | `f1e25594692361537f1dc7a99c406d36ef` | 同文件:7 |
| `jiaowu.sicau.edu.cn` | `fafe409330252643770b88b9d65027203418e0` | 同文件:8 |

对照探索笔记的完整形态与端口形态（**端口 `-443` 只在探索笔记/规格，源码 `encodeWebVpnUrl` 不含**）：路径 `https://webvpn.sicau.edu.cn/{scheme}[ -{port} ]/{key_hex}{host_ciphertext_hex}{original_path}?{query}`；`scheme` 可为 `https` / `http`，带端口时为 `https-443`；`/https-443/{key_hex}{cipher}/…` 对 captcha 返回 200（实测）；缺 `key_hex` 前缀会 `302 → /login`。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:31-42,64-68,73-80] 编码算法 `KEY=IV=b"wrdvpnisthebest!"`、AES-128-CFB `segment_size=128`、`pad_right_0`、只取 `len(host)` 字节 hex。[来源: 同文件:44-52；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:28]

---

### 3. `core/webvpn/transport.ts`：传输模式与改写触发

| 契约 | 精确值 / 行为 | 来源 |
| --- | --- | --- |
| `JiaowuTransportMode` | `type = "direct" \| "webvpn"`（导出类型） | @OLD/packages/nongyu-tool-jiaowu/core/webvpn/transport.ts:10 |
| `REWRITE_HOSTS` | `new Set(["jiaowu.sicau.edu.cn", "auth.sicau.edu.cn"])`（**模块内私有常量，根 `index.ts` 未导出**） | 同文件:12-13 |
| 默认 `direct` | `let mode: JiaowuTransportMode = "direct"`（模块级可变状态） | 同文件:15 |
| `getJiaowuTransport()` | `() => JiaowuTransportMode`，返回当前 mode | 同文件:20-22 |
| `setJiaowuTransport(next)` | `(next) => void`，整体赋值 | 同文件:27-29 |
| `rewriteOutboundUrl(url)` | 导出函数；`if (mode !== "webvpn" \|\| !url) return url`；`host = hostOf(url)`（`^https?://([^/?#]+)` 去掉端口并小写）；`if (!REWRITE_HOSTS.has(host)) return url`；否则 `return encodeWebVpnUrl(url)` | 同文件:31-47 |
| 触发条件归纳 | 仅当 (a) mode 为 `webvpn` 且 (b) URL 是 http(s) 绝对地址且 (c) 主机名（去端口、小写）命中两个白名单之一才改写；其余（含相对路径、已是 webvpn 域、其它 host）原样返回 | 同文件:43-47；encode.ts:126 |
| 谁调用 | `core/utils/request.ts` 请求拦截器对 `config.url` 无条件调 `rewriteOutboundUrl`；`core/login/cas.ts` 对 captcha / cas/login / cas/loginByPhone / sms/send / CAS_SERVICE_URL / SESSION_PROBE_URL 共 11 处显式调 | @OLD/packages/nongyu-tool-jiaowu/core/utils/request.ts:61-64；core/login/cas.ts:238,278,280,395,522,535,615,646,658,719,759 |
| 注释口径 | "直连是默认。只有登录页探测到教务不可达，或冷启动恢复了 WebVPN 票据时，才切到 webvpn。业务请求与 CAS 请求都走 rewriteOutboundUrl，避免漏改 service / Location" | 同文件:1-6 |

---

### 4. `core/login/cas.ts`：两套 appid / service / CAS 链路 / WebVPN 换票

#### 4.1 常量（对外契约的硬值）

| 常量 | 值 | 是否导出 | 来源 |
| --- | --- | --- | --- |
| `CAS_BASE_URL` | `https://auth.sicau.edu.cn` | 否 | @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:28-29 |
| `CAS_APPID`（教务） | `b54a2f27-f7ba-4b91-bbf0-3b2b12244d14` | 否（注释"来自 `caslogin.asp` 的 302 `Location`，不是自定义值，改动须重新抓取"） | 同文件:31-35 |
| `CAS_SERVICE_URL`（教务 service） | `https://jiaowu.sicau.edu.cn/jiaoshi/aspsso/caslogin.asp` | 否 | 同文件:37-41 |
| `RESPONSE_SALT` | `":sicau-cas"`（响应体加密盐） | 否 | 同文件:43-46 |
| `ASP_COOKIE_PREFIX` | `"ASPSESSIONID"` | 否 | 同文件:55-56 |
| `CAS_MAX_REDIRECTS` | `5` | 否 | 同文件:58-59 |
| `CAS_TIMEOUT` | `15000` ms | 否 | 同文件:61-62 |
| `CALLBACK_TIMEOUT` | `20000` ms | 否 | 同文件:64-65 |
| `SESSION_PROBE_URL` | `https://jiaowu.sicau.edu.cn/xuesheng/dangan/banji/bjiben.asp` | 否 | 同文件:380-381 |
| `WEBVPN_PORTAL_APPID` | `aea49a21-6b26-4e8f-bcfc-80e3de591c93` | **是**（根导出面） | 同文件:679-680 |
| `WEBVPN_PORTAL_SERVICE` | `https://webvpn.sicau.edu.cn/login?cas_login=true` | 否（只在 `cas.ts` 内导出，根 `index.ts` 未 re-export） | 同文件:682-683 |
| `axios.defaults.withCredentials` | `true`（全局打开，"登录链路与业务请求必须共享同一份会话"） | 否（副作用） | 同文件:48-53 |

两套 appid / service 不可混用：门户 `aea49a21-…` + `https://webvpn.sicau.edu.cn/login?cas_login=true` → 有效 `wengine_vpn_ticket*`；教务 `b54a2f27-…` + 直连 `caslogin.asp`（经 VPN 时应改为编码后的 caslogin URL）→ `ASPSESSIONID*`；"ticket / service / appid 禁止跨应用混用"。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:150-153；@OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:679-683]

#### 4.2 CAS 登录链路（文件头注释与 `TECH.md`）

```
GET  /jiaoshi/aspsso/caslogin.asp                    → 302 到统一身份认证（appid + service）
GET  /api/v1/captcha?appid=<教务 appid>              → 图形验证码（加密响应体）
POST /api/v1/cas/login                               → 校验通过后返回 ticket（加密响应体）
GET  <callbackUrl>?ticket=<st>:<userId>&userId=<id>  → 教务系统据此建立 ASP 会话
```
[来源: @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:1-13；@OLD/packages/nongyu-tool-jiaowu/TECH.md:21-29]

各步骤实际请求契约：

| 步骤 | 方法 / URL | 入参 | 出参 | 来源 |
| --- | --- | --- | --- | --- |
| 取验证码 | `GET rewriteOutboundUrl(CAS_BASE_URL + "/api/v1/captcha")`，`params:{appid}`，`timeout:CAS_TIMEOUT` | `appid` 默认 `CAS_APPID` | 信封 `data`（加密 `data:image/...`）+ `id`（即 uuid）；`{success:true;result:{uuid,image}}` | @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:236-256；TECH.md:25 |
| 密码登录换票 | `POST rewriteOutboundUrl(CAS_BASE_URL + "/api/v1/cas/login")`，JSON `{appid:CAS_APPID, code, number:studentId, password, uuid}` | `JiaowuCasLoginInput` | 解密后 `{callbackUrl, st, users:[{id}]}` | 同文件:534-559；TECH.md:27 |
| 短信发送 | `POST rewriteOutboundUrl(CAS_BASE_URL + "/api/v1/sms/send")`，`{uuid, code, type:"login", phone}`；`body.code===200` 判成功 | `JiaowuSendSmsInput` | `{success:true}` / 失败 | 同文件:602-626 |
| 短信登录换票 | `POST rewriteOutboundUrl(CAS_BASE_URL + "/api/v1/cas/loginByPhone")`，`{code:smsCode, phone, appid:CAS_APPID}`；`body.code===200` 才继续 | `JiaowuPhoneLoginInput` | 同构 payload | 同文件:657-673 |
| 回投教务建会话 | `completeServiceLogin`：最多 `CAS_MAX_REDIRECTS=5` 跳，`maxRedirects:0` 手动跟随 302 `Location`，收集 `Set-Cookie` 中 ASPSESSIONID 族 | ticket + userId + entry cookie | `{success:true;cookie}` 或失败 | 同文件:431-498 |
| 会话探测兜底 | `probeJiaowuSession()`：GET `SESSION_PROBE_URL`，`status===200` 且 body 长度≥300 且不含「登录超时」「账号验证失败」、不含 `type=password` / `name=pwd` | 无 | `boolean` | 同文件:380-417 |

`buildCallbackUrl`（set 语义覆盖 ticket）：平台 `callbackUrl` 自带 `?ticket=ST:xxx`（无 userId 后缀），本函数过滤掉同名 `ticket`/`userId` 后追加 `ticket=encodeURIComponent(st + ":" + userId)` 与 `userId=...`——注释明确若改成字符串追加会出现两个 `ticket`、教务取第一个（缺 `:userId`）→ 会话不成立。[来源: @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:285-312,428-430]

`guardServiceUrl`（WebVPN 模式下回调必须留在 webvpn 域，直连模式只允许教务域名）：webvpn 时 `rewriteOutboundUrl(url)` 必须以 `https://webvpn.sicau.edu.cn/` 开头否则回落到 `rewriteOutboundUrl(fallback)`；直连时 `assertJiaowuOrigin`（必须 `startsWith("https://jiaowu.sicau.edu.cn/")`）否则回落 `CAS_SERVICE_URL`。[来源: 同文件:258-283]

#### 4.3 WebVPN 门户换票 / 探测三个函数

| 函数 | 签名 | 输入 / 输出 / 行为 | 来源 |
| --- | --- | --- | --- |
| `probeDirectJiaowu` | `export async () => Promise<boolean>`（**未在根 `index.ts` 导出**） | `GET https://jiaowu.sicau.edu.cn/web/web/web/index.asp`，`timeout:4000`，`validateStatus:()=>true`；任何 HTTP 响应返回 `true`，超时/网络错误 `false` | @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:690-703 |
| `prepareJiaowuTransport` | `export async () => Promise<"direct" \| "webvpn">`（根导出面） | 调 `probeDirectJiaowu()`，可达 `setJiaowuTransport("direct")` 否则 `"webvpn"`，返回该值 | 同文件:705-713；index.ts:25 |
| `loginWebVpnPortal` | `export async (input: JiaowuCasLoginInput) => Promise<WebVpnPortalLoginResult>`（根导出面） | 先 `setJiaowuTransport("webvpn")`；`POST /api/v1/cas/login` 用 `appid:WEBVPN_PORTAL_APPID`（其余 body 同 CAS）；解出 `{st, users[0].id}`；`callback` 用 `payload.callbackUrl`（仅当以 `https://webvpn.sicau.edu.cn/` 开头，否则 `WEBVPN_PORTAL_SERVICE`）+ `buildCallbackUrl`；GET callback 后若 body 含 `INVALID_TICKET` 判失败；再 `tryJiaowuEntryThroughVpn()` 决定 `needJiaowuCaptcha` | 同文件:738-805 |

`tryJiaowuEntryThroughVpn()`（私有）：从 `rewriteOutboundUrl(CAS_SERVICE_URL)` 起最多 5 跳跟随 302 `Location`，遇 `/wui/` 或 `cascallback=choose` 返回 `false`，否则最终 `probeJiaowuSession()`。[来源: 同文件:715-736]

---

### 5. 302 `Location` 再编码规则 + captcha/cas/login 响应体加密格式

#### 5.1 302 `Location` 再编码

| 契约 | 内容 | 来源 |
| --- | --- | --- |
| 规则（规格） | "WebVPN 模式下 `service` / `callbackUrl` / 302 `Location` 若指向真站，再编码一次，禁止回落到校外不可达的真站" | @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:30 |
| 规则（探索笔记） | "跟随 `Location` 时若仍是真站绝对地址需再 encode"；改写行为表："上游 302 的站内目标改为 webvpn 绝对 URL；`service=` 可保留调用方传入的（含已编码的 caslogin）" | @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:98,210 |
| 源码实现 | `resolveRedirect(base, location)` 手工按 RFC 3986 拼相对路径（处理 `../`、`./`、保留 query，绝对 http(s) 原样返回）；随后 `guardServiceUrl(resolveRedirect(...), currentUrl)` 在 webvpn 模式下再编码/回落 | @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:314-344,467；718-734 |
| 触发原因 | "RN 不执行 `main.js`，必须自己把 `service` / `callbackUrl` 编成 WebVPN URL"；浏览器靠 `main.js` 的 `vpn_rewrite_url` 改写 Location 留在 WebVPN 路径 | @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:20,100,285 |
| 停止条件 | WebVPN 门户回调链遇 `/wui/` 或 `cascallback=choose` 停，交给第二次图形码 | @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:732 |

#### 5.2 captcha / cas/login 响应体加密格式（源码内有完整解密实现）

`decryptCasData(encryptedText, requestId)`（私有，`cas.ts:132-158`），注释"与平台前端 `qr()` 一致"：[来源: @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:117-158；TECH.md:35]

| 步骤 | 算法 / 值 | 来源 |
| --- | --- | --- |
| 1. key 派生 | `aesKeyHex = CryptoJS.MD5(requestId + RESPONSE_SALT).toString(Hex)`，即 `MD5(requestId + ":sicau-cas")` 取 32 位 hex 作为 AES-128 key | 同文件:133；RESPONSE_SALT 定义:43-46 |
| 2. 拆 IV | base64 解码后**前 16 字节是 IV**（`iv.sigBytes=16`，`iv.clamp()`），其余是密文（`raw.words.slice(4)`） | 同文件:135-141 |
| 3. 解密 | `CryptoJS.AES.decrypt`，`mode: CBC`，`padding: NoPadding` | 同文件:143-151 |
| 4. 去自定义填充 | 明文最后一个字节 = 填充长度 N，去掉尾部 N 字节（`padLength = plain.words[last] & 0xff`） | 同文件:153-155 |
| 5. 结果 | 剩余部分是 JSON，`JSON.parse` | 同文件:157 |
| 信封 | `CasEnvelope<T> = { code?:number; msg?:string; status?:string; data?:T\|string\|null; requestId?:string; id?:string }`；`unwrapEnvelope`: `data` 为字符串才解密，为对象/null 原样返回；失败响应（如「验证码错误」）`data` 是 `null`，不能拿去解密 | 同文件:67-77,160-173 |
| 适用接口 | captcha 的 `data` 是 `data:image/png;base64,...`，`id` 是 uuid；`cas/login` 的 `data` 解密后是 `{callbackUrl, st, users:[{id}]}` | 同文件:236-256,549-559；TECH.md:25-27 |
| 实测形态 | `GET captcha`（正确 key_hex 前缀）→ 200 JSON `code=200`，含加密 `data` + `id` + `requestId`；`POST cas/login` 错误验证码 → 200 体 `code=500` `msg=验证码错误` | @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:160-162 |
| 不用 `node:crypto` | 注释明确"本包同时跑在 RN 上，那里没有该模块" | @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:127；TECH.md:8 |

注：captcha / cas/login 的 AES 加密是**统一身份认证平台响应体**的密码学契约，与 §2 的 WebVPN 主机名 AES-128-CFB 编码是两套不同算法（前者 AES-128-CBC + MD5 派生 key + 自定义尾填充，后者 AES-128-CFB + 固定 key/iv）。[来源: @OLD/packages/nongyu-tool-jiaowu/core/webvpn/encode.ts:74-91；core/login/cas.ts:132-158]

---

### 6. 错误类型与错误码清单

仓内**没有**统一的数字错误码枚举表；错误以「TS 类型 + 常量 + 字符串 message + axios error.code」散落。逐项：

| 类别 | 名称 | 精确值 / 判定 | 来源 |
| --- | --- | --- | --- |
| 错误类 | `JiaowuSessionExpiredError extends Error` | `name = "JiaowuSessionExpiredError"`，默认 message `"教务会话已过期，请重新登录"` | @OLD/packages/nongyu-tool-jiaowu/core/utils/errors.ts:15-20 |
| 类型守卫 | `isJiaowuSessionExpiredError` | `instanceof` 或 `err.name === "JiaowuSessionExpiredError"` | 同文件:23-28 |
| 超时守卫 | `isJiaowuTimeoutError` | `code === "ECONNABORTED" \| "ETIMEDOUT" \| "ESOCKETTIMEDOUT"`，或 message 含 `/timeout/i`，或递归 `cause` | 同文件:33-50 |
| 提示常量 | `JIAOWU_CAMPUS_NETWORK_HINT` | `"教务网迁入内网，可尝试连接校园网使用农屿"`（超时固定返回） | 同文件:6,55-58 |
| 统一 message | `resolveJiaowuErrorMessage(error, fallback="请求失败")` | 超时→常量；否则取 `error.message` / 对象 `message` / fallback | 同文件:55-67 |
| 失败结果构造 | `jiaowuFailResult<T>(result, error)` | `{success:false; result; message}` | 同文件:72-85 |
| 平台响应码（信封 `code`） | `code === 200` 判成功（短信发送、短信登录） | `body?.code === 200` / `body?.code !== 200` | @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:620,667 |
| 平台响应码（实测） | 验证码错误 `code=500`、`msg=验证码错误`；captcha 正常 `code=200` | 探索笔记实测 | @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:160-162 |
| 信封字段 | `CasEnvelope.code?:number; msg?:string; status?:string` | 类型定义 | @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:67-77 |
| 业务失败字符串 | `"INVALID_TICKET"` | WebVPN 门户 callback 响应体含此串 → `"WebVPN 票据校验失败，请重试"` | 同文件:795-798 |
| 会话过期触发 | 响应体含「登录超时」/「账号验证失败」，或最终 URL 以 `https://webvpn.sicau.edu.cn/login` 开头（vpn ticket 死） | 清 Cookie 并 `Promise.reject(new JiaowuSessionExpiredError())` | @OLD/packages/nongyu-tool-jiaowu/core/utils/request.ts:203-212 |
| 重试 | 网络错误最多重试 3 次，每次延迟 1s | `_retryCount < 3` | 同文件:219-227；TECH.md:13 |
| 失败文案集合（登录类） | `"未提供学号或密码"` / `"请输入图形验证码"` / `"验证码已失效，请刷新后重试"` / `"统一身份认证未返回有效票据"` / `"教务系统回调跳转异常，请稍后重试"` / `"教务系统未建立会话，请确认已连接校园网后重试"` / `"登录异常"` / `"WebVPN 登录失败"` / `"WebVPN 登录异常"` / `"短信发送失败"` / `"短信登录失败"` / `"短信登录异常"` | 分布在 cas.ts 各 return/catch | @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:439,475,487,506,510,514,565,576,607,610,622,624,639,668,675,749,750,752,777,797,803 |

仓内**未找到**：统一错误码常量表 / `enum` / 错误码到 HTTP 状态的映射（只有一个 `code?:number` 信封字段与 `code===200` 判定）。[来源: 全仓 grep `enum|错误码|code` 命中见上表，无枚举定义；@OLD/packages/nongyu-tool-jiaowu/core/utils/errors.ts 全文、`core/login/cas.ts:67-77`]

---

### 7. RN 侧调用契约（谁调哪个导出、传什么、拿什么；不展开 UI）

| 调用点 | 调的导出 / 桥接 | 传入 | 拿到 / 后续动作 | 来源 |
| --- | --- | --- | --- | --- |
| `jiaowuSession.ts:bridgeGetJiaowuCaptcha` | `getJiaowuCaptcha()`（默认教务 appid） | 无 | `JiaowuCaptchaResult`（`{uuid,image}` 给界面） | @OLD/apps/nongyu-rn-app/src/modules/jiaowu/auth/jiaowuSession.ts:38-40；import:1-19 |
| `jiaowuSession.ts:bridgeGetWebVpnPortalCaptcha` | `getJiaowuCaptcha(WEBVPN_PORTAL_APPID)` | 门户 appid 常量 | `JiaowuCaptchaResult`（注释"appid 与教务不同，不能复用教务那张图"） | 同文件:42-47 |
| `jiaowuSession.ts:bridgePrepareJiaowuTransport` | `prepareJiaowuTransport()` | 无 | `"direct" \| "webvpn"` | 同文件:49-54 |
| `jiaowuSession.ts:bridgeLoginWebVpnPortal` | `loginWebVpnPortal(input)` | `JiaowuCasLoginInput` | `WebVpnPortalLoginResult` | 同文件:56-61 |
| `jiaowuSession.ts:bridgeJiaowuLoginByCas` | `jiaowuLoginByCas(input)` | `JiaowuCasLoginInput` | `JiaowuCasLoginResult`；成功且 `cookie` 非空则 `setJiaowuAspCookie(result.cookie)`（写 MMKV） | 同文件:63-74 |
| `jiaowuSession.ts:bridgeJiaowuLoginByPhone` | `jiaowuLoginByPhone(input)` | `JiaowuPhoneLoginInput` | `JiaowuCasLoginResult`；成功备份 MMKV | 同文件:83-94 |
| `jiaowuSession.ts:bridgeSendJiaowuSmsCode` | `sendJiaowuSmsCode(input)` | `JiaowuSendSmsInput` | 成功/失败结果 | 同文件:76-81 |
| `jiaowuSession.ts:bridgeJiaowuLogin`（旧） | `jiaowuLogin(studentId?, password?)` | 学号/密码 | 结果；成功备份 MMKV | 同文件:96-108 |
| `jiaowuSession.ts:restoreAspCookieFromStorage` | `getJiaowuAspCookie()` + `setCookie(cookie)` | MMKV 值 | `boolean` | 同文件:110-118 |
| `jiaowuSession.ts:persistCurrentAspCookie` | `getCookie()` + `setJiaowuAspCookie` | 内存 Cookie | 无 | 同文件:120-128 |
| `jiaowuSession.ts:clearJiaowuToolSession` | `clearLoginData()` + `clearJiaowuAspCookie()` | 无 | 无 | 同文件:130-136 |
| `performJiaowuLogin.ts:performJiaowuLogin` | `bridgeJiaowuLoginByCas({studentId,password,captchaCode,captchaUuid})` | 表单四字段 | 失败透出 `message/needRefreshCaptcha`；成功 `captureWebVpnTicket()` 再 `finalizeJiaowuLogin` | @OLD/apps/nongyu-rn-app/src/modules/jiaowu/auth/performJiaowuLogin.ts:62-94 |
| `performJiaowuLogin.ts:performWebVpnPortalLogin` | `bridgeLoginWebVpnPortal({...})` | 表单四字段 | 失败透出；成功 `captureWebVpnTicket()`；`portal.needJiaowuCaptcha` 为真则要求第二次教务验证码 | 同文件:100-130 |
| `performJiaowuLogin.ts:finalizeJiaowuLogin` | `getPersonalInfo()` / `setCookie(cookie)` | 学号/密码/queryClient | 档案 → `appAuthLogin` → 持久化（WebVPN 票据不在此处） | 同文件:137-218,305 |
| `performJiaowuLogin.ts:performJiaowuLogout` | `clearWebVpnTicket()` + `clearJiaowuToolSession()` | queryClient | 无 | 同文件:318-360 |
| `JiaowuLoginForm.tsx` | `bridgePrepareJiaowuTransport()`（挂载时）→ `bridgeGetWebVpnPortalCaptcha()` 或 `bridgeGetJiaowuCaptcha()` → `performWebVpnPortalLogin(payload)` 或 `performJiaowuLogin(payload)` | `mode` / `stage` / `{studentId,password,captchaCode,captchaUuid,queryClient}` | `mode==="webvpn"` 决定走门户；`result.needJiaowuCaptcha` 决定是否再要一张教务码 | @OLD/apps/nongyu-rn-app/src/modules/jiaowu/components/JiaowuLoginForm.tsx:67-92,128-144 |
| `webvpnTicket.ts:captureWebVpnTicket` | 原生 `NongyuWebVpnCookie.readTicket()` → `SecureStore.setItemAsync("webvpn_ticket", value)` | 无 | 无（登录后抄 HttpOnly 票据） | @OLD/apps/nongyu-rn-app/src/modules/jiaowu/auth/webvpnTicket.ts:18-35 |
| `webvpnTicket.ts:restoreWebVpnTicket` | `SecureStore.getItemAsync("webvpn_ticket")` → 原生 `writeTicket(value)` → `setJiaowuTransport("webvpn")` | 无 | `boolean`（冷启动恢复并切模式） | 同文件:37-53 |
| `webvpnTicket.ts:clearWebVpnTicket` | `SecureStore.deleteItemAsync` + 原生 `clearTicket()` → `setJiaowuTransport("direct")` | 无 | 无 | 同文件:55-70 |
| `handleAuthInvalid.ts` 登出 | `clearWebVpnTicket()` | 无 | 无 | @OLD/apps/nongyu-rn-app/src/api/handleAuthInvalid.ts:7,39 |
| `useJiaowuBootstrap.ts` 冷启动 | `restoreWebVpnTicket()` | 无 | 无 | @OLD/apps/nongyu-rn-app/src/modules/jiaowu/hooks/useJiaowuBootstrap.ts:9,70 |

Android 原生模块（Expo `NongyuWebVpnCookie`）：[来源: @OLD/apps/nongyu-rn-app/modules/nongyu-webvpn-cookie/android/src/main/java/expo/modules/nongyuwebvpncookie/NongyuWebVpnCookieModule.kt]

| 契约 | 值 / 行为 | 行号 |
| --- | --- | --- |
| 模块名 | `Name("NongyuWebVpnCookie")` | :15 |
| 导出函数 | `AsyncFunction("readTicket")` / `writeTicket(value: String)` / `clearTicket()` | :17-27 |
| Cookie 罐 | `java.net.CookieHandler.getDefault()`（RN XHR 用），明确"不用 android.webkit.CookieManager" | :9-11,30 |
| `ORIGIN` | `https://webvpn.sicau.edu.cn/` | :74 |
| `TICKET_NAME` | `wengine_vpn_ticketwebvpn_sicau_edu_cn` | :75 |
| `readTicket` | 从 `Cookie` 头找 `wengine_vpn_ticketwebvpn_sicau_edu_cn=`，返回值或 null | :42-48 |
| `writeTicket` | `Set-Cookie: <name>=<value>; Path=/; Secure; HttpOnly` 写回罐 | :50-60 |
| `clearTicket` | `Set-Cookie: <name>=; Path=/; Max-Age=0` | :62-71 |

---

## 可重写性备注

> 只记「哪些契约是纯计算可 1:1 重写 / 哪些依赖 RN 原生 Cookie 罐 / 哪些依赖浏览器 main.js」这类**事实判断**，不给方案、不拍板。

**A. 纯计算 / 协议常量，行为由仓内源码完全确定（与运行时环境无关）：**

- WebVPN 主机名编码：`encodeVpnHost` / `aesCfb128Encrypt` / `encodeWebVpnUrl` 全部是纯函数，输入字符串输出字符串，无 I/O、无 Cookie、无 `main.js`。算法与常量（`WEBVPN_ORIGIN` / `WEBVPN_KEY` / `WEBVPN_KEY_HEX` / BLOCK=16 / `'0'` 右填充 / 只取 host 长度 / key_hex 前缀）都在 `encode.ts` 内写死。[来源: @OLD/packages/nongyu-tool-jiaowu/core/webvpn/encode.ts:11-128]
- `REWRITE_HOSTS` 白名单、`JiaowuTransportMode`、`rewriteOutboundUrl` 的触发条件（mode + host 命中）是纯逻辑（仅依赖一个模块级 `mode` 变量）。[来源: @OLD/packages/nongyu-tool-jiaowu/core/webvpn/transport.ts:10-47]
- CAS 响应体解密 `decryptCasData`（MD5 派生 key / CBC / NoPadding / 自定义尾填充）、`CasEnvelope` 形状、`buildCallbackUrl` 的 set 语义、`resolveRedirect` 的 RFC 3986 相对拼接，均为纯计算。[来源: @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:67-77,132-173,285-344]
- 两套 appid / 两套 service URL / 超时常量 / 盐值 / 端点路径都是源码内常量。[来源: @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:28-65,679-683]
- 两个 host 的期望密文有 `encode.selfcheck.ts` 可作逐字节对照。[来源: @OLD/packages/nongyu-tool-jiaowu/core/webvpn/encode.selfcheck.ts:6-9]

**B. 依赖「客户端 RN 原生 / 系统 Cookie 罐」的契约（源码注释明确 RN 读不到 `Set-Cookie`）：**

- 登录态载体 `wengine_vpn_ticketwebvpn_sicau_edu_cn` 是 `webvpn.sicau.edu.cn` 上的 HttpOnly 会话 Cookie；网关按 ticket 在服务端补上游 `ASPSESSIONID*`。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:179,295-300,377]
- `axios.defaults.withCredentials = true`、`request.ts` 实例 `withCredentials: true`，注释"RN 读不到 Set-Cookie，只能依赖原生网络栈自动保存/回送"。[来源: @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:48-53；core/utils/request.ts:43-46]
- `completeServiceLogin` 在 `finalCookie` 为空时用 `probeJiaowuSession()` 反查会话（因为 RN XHR 读不到 Set-Cookie），成功也返回 `cookie: ""`，会话交给原生罐。[来源: @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:480-497]
- 票据持久化/恢复走 `expo-secure-store` + `NongyuWebVpnCookie`（`java.net.CookieHandler`）；规格边界明确"不实现 iOS 原生模块（当前本地模块只有 Android）"。[来源: @OLD/apps/nongyu-rn-app/src/modules/jiaowu/auth/webvpnTicket.ts:1-70；@OLD/apps/nongyu-rn-app/modules/nongyu-webvpn-cookie/android/src/main/java/expo/modules/nongyuwebvpncookie/NongyuWebVpnCookieModule.kt:9-11,30；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:22]
- `getCookie`/`setCookie` 只是包内**内存全局字符串**，与系统 Cookie 罐不是同一份；`jiaowuSession.ts` 另把 `ASPSESSIONID*` 备份到 MMKV（`jiaowu:asp_cookie`）。[来源: @OLD/packages/nongyu-tool-jiaowu/core/utils/cookie.ts:11-29；@OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:304]

**C. 依赖「浏览器 `main.js`」的契约（RN 不执行，故仓内用手工编码替代）：**

- 浏览器场景由 `/wengine-vpn/js/main.js`（`vpn_rewrite_url` / `vpn_real_set_cookie` / `/wengine-vpn/cookie`）改写 HTML 绝对链、`Location`、挂钩 `document.cookie` 显示 `ASPSESSIONID*`；"原生 RN 请求不会执行 main.js"，故 `service` / `callbackUrl` / 302 `Location` 必须由客户端自己编码。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:20,95-100,184-188,269-285；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:9,30]
- 规格边界"不保证永远免第二次验证码。浏览器里出现过 SSO，RN 不跑 main.js，失败就再要一次码"。[来源: @OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:23]

**D. 探测 / 风控类（依赖真实网络与账号，非纯计算）：**

- `probeDirectJiaowu`（4s 短超时探测教务首页）、`probeJiaowuSession`（用「需登录才 200」的业务页反查会话）、`tryJiaowuEntryThroughVpn` 都是发真实请求并依赖响应体/状态码/重定向。[来源: @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:380-417,690-703,715-736]
- 每次登录必须由**用户输入图形验证码**（不做 OCR），因此"不再具备会话超时后静默用本地密码重登的能力"。[来源: @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:15-16；core/utils/errors.ts:8-13]

---

## 尚未查清

以下为**仓内查不到或标注为未测**，不推测填充：

1. **端口 `-443` 形态在 TS 源码里没有实现**：探索笔记/规格记录 `/https-443/{key_hex}{cipher}/…` 实测 200，但 `encodeWebVpnUrl` 只用 `/{scheme}/`（`scheme` 为 `http`/`https`）且 `hostPort.replace(/:\d+$/, "")` 直接丢弃端口。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:36,42；@OLD/packages/nongyu-tool-jiaowu/core/webvpn/encode.ts:119-127] 仓内未找到把端口拼进 scheme 段（如 `https-443`）的 TS 代码。
2. **`/wengine-vpn/cookie` 读写语义**：探索笔记自列"登录后 jiaowu get 返回 `ASPSESSIONID*` 名称；set 未主动调用"，"上游 `Set-Cookie` 域名/路径改写规则未测"。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:99,172,183,323]
3. **WebVPN ticket TTL / 空闲超时精确值**：仅知会话 Cookie、无 Max-Age；学校未公开空闲超时。[来源: 同文件:179,295,326]
4. **RN 不跑 `main.js` 时 `ASPSESSIONID*` 是否仅靠 `Set-Cookie` 进系统罐**：探索笔记明确"未拆响应头验证"。[来源: 同文件:22,330]
5. **已登录 CAS 是否可无验证码进 WebVPN / 双 CAS 合并**：探索笔记标"部分 / 本次是先登 WebVPN 再进教务，不是先直连 auth 再进 WebVPN"；规格"不保证永远免第二次验证码"。[来源: 同文件:324-325；@OLD/docs/nongyu-rn-app/specs/WebVPN传输适配.md:23]
6. **`probeDirectJiaowu` / `WEBVPN_PORTAL_SERVICE` 是否对外暴露**：二者在 `cas.ts` 内 `export`，但根 `index.ts` 与 `dist/index.d.ts` 的导出清单里都没有，消费方无法通过包名引用。[来源: @OLD/packages/nongyu-tool-jiaowu/core/login/cas.ts:683,693；index.ts:12-68；dist/index.d.ts:1424]
7. **统一错误码枚举表**：仓内未找到（见 §6 末尾）。
8. **真账号链路未验收项**（沿用事实底稿第 207 行）：上述 2-5 项均可能影响任何重写；本清单不展开。[来源: @NEW/docs/tech/旧农屿WebVPN迁移调研.md:207]
9. **`@OLD/packages/nongyu-agent-sdk/src/jiaowu.ts` 等其它消费方**：本任务限定 RN 侧四个文件与 Android 模块，未逐行读取 agent-sdk 消费面。[来源: @NEW/docs/tech/旧农屿WebVPN迁移调研.md:80,205]

---

## 不适用说明

- 本文**不是**设计、不是迁移方案、不是范围拍板、不含任何 Go 接口/模块/存储建议；按 `/research` 与票 #2 约束，只做「对外契约的事实陈列」。[来源: 本任务约束]
- 未引用任何二手转述（博客、问答、外部网页）；探索笔记中带「实测 2026-10-30 / 2026-10-31」的记录是维护者当时的一手实测记录，本清单按一手来源引用，且其中「未测 / 未验证 / 环境相关」条目已如实标注为未查清，未被当作已证实结论。[来源: @OLD/docs/nongyu-rn-app/tech/WebVPN探索.md:13,154,173,219,316-332；@NEW/docs/tech/旧农屿WebVPN迁移调研.md:216]
- 未创建 issue、未建分支、未 git commit、未调用任何 `deck_*` 工具、未修改除本清单外的任何文件；是否提交由父代理/维护者决定。[来源: 本任务约束；`glob docs/tech/*` 实测本清单为本会话在 @NEW 工作区唯一新增文件]
- 全局 `~/.dsh/AGENTS.md` 要求「任务完成后提交 git」等，但本任务明确要求「不要建 issue、不要建分支、不要 git commit、不改除指定产物外的任何文件」；本子代理未执行 git 操作。[来源: 本任务约束；全局 AGENTS.md]
- 事实底稿 `@NEW/docs/tech/旧农屿WebVPN迁移调研.md` 已把 WebVPN 相关实体、路径、行号列全；本清单顺着其「问题 1 / 1.2 / 1.3 / 1.4」深挖 `index.ts` 与 `core/login/cas.ts` 源码，不重复其目录级结论。[来源: @NEW/docs/tech/旧农屿WebVPN迁移调研.md:28-80]
