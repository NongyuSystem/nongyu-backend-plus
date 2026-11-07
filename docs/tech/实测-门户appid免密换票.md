# 实测：门户 appid（WebVPN 门户）能否用 go-admin-tgt 免密换票

- 实测时间：2026-11-07 10:50 ~ 11:00（Asia/Shanghai）
- 实测环境：Windows 主控机，DSH 沙箱（workspace-write），出口经 `web_fetch` 工具
- 目标问题：`Cookie: go-admin-tgt=<值>` + `GET /api/v1/cas/login?appid=aea49a21-6b26-4e8f-bcfc-80e3de591c93` 是否返回 `code=200` 且解密后含 `st`？

---

## 结论

- **已验证**：门户 appid `aea49a21-6b26-4e8f-bcfc-80e3de591c93` 在统一身份认证平台**已注册且可用** —— `GET /api/v1/captcha?appid=<门户appid>` 返回 `code=200` + 加密 `data` + `id`，说明平台给该 appid 正常签发登录事务（含验证码），不是「未知 appid / 该应用未接入」。
- **未验证**：`go-admin-tgt` 能否给**门户 appid** 免密换票。本次**没有**拿到任何 `go-admin-tgt` 值（沙箱内无用户会话、也不具备索取条件），因此这一条**没有任何实测证据**，既不能证成也不能证伪。
- **部分验证**：`GET /api/v1/cas/login?appid=<门户appid>` 在**未登录态**下确实走 `appid` 入参分支并返回该平台的登录态错误形态（HTTP 200 + `code=500` + `msg="未登录"`），接口存在、对门户 appid 不报「appid 非法」；但**这只证明接口存在与错误形态**，不能证明「带 cookie 能换票成功」。

> 一句话：接口侧与 appid 侧的门都开着，缺的是「带一份有效 `go-admin-tgt` 走一遍」这一步，而那一步需要用户本人配合（浏览器里已有登录态），本轮无法完成。

---

## 实测方法与原始证据

### 请求 1：门户 appid + 未登录态换票（核心问题）

```
GET https://auth.sicau.edu.cn/api/v1/cas/login?appid=aea49a21-6b26-4e8f-bcfc-80e3de591c93
（无 Cookie 头，完全未登录态）
```

原始响应（HTTP 200）：

```json
{"requestId":"<REDACTED>","code":500,"msg":"未登录","status":"error","data":null}
```

读法：

| 观察 | 说明 |
| --- | --- |
| HTTP 状态 200 | 平台用「200 + 业务码」模式，不用 401/403 —— 所以**不能靠 HTTP 状态判断成败**，必须看 `code` |
| `code=500`，`msg="未登录"` | 「未登录」是**会话缺失**的错误，不是「appid 非法」「应用不存在」「无权访问」。若门户 appid 未接入，预期文案应与之不同（本次未观察到任何 appid 相关报错） |
| `data=null` | 失败响应 `data` 为 `null`，与既有契约「失败响应 `data` 是 `null`，不能拿去解密」一致（见 §与既有笔记的一致性 第 3 条） |

### 请求 2：门户 appid 是否在平台注册（判别实验）

```
GET https://auth.sicau.edu.cn/api/v1/captcha?appid=aea49a21-6b26-4e8f-bcfc-80e3de591c93
```

原始响应（HTTP 200，`data` 为加密串，长 3000+ 字符）：

```json
{"code":200,"data":"<REDACTED-AES-CIPHERTEXT>","id":"<REDACTED-uuid>","msg":"success","requestId":"<REDACTED>"}
```

读法：`code=200` + `data` 非空 + 独立 `id`（即登录事务 uuid）= 平台**为该 appid 成功开了一个登录事务**。这是「appid 在服务端确实注册、可开事务」的直接证据，也说明门户 appid 与教务 appid 一样是一等公民。

### 未完成的部分（以及为什么）

| 想做的事 | 状态 | 原因 |
| --- | --- | --- |
| 带 `go-admin-tgt` 走免密换票 | **未执行** | 无任何有效 cookie 值；任务约束明确不得索要账密/凭据 |
| 走 POST 账密登录自取 cookie | **未执行**（按约束禁止） | 无账号密码，且不得索要 |
| 解密响应体验证 `st` | **未执行** | 无成功响应可解 |

### 本次探测的工程障碍（供后续复现参考）

沙箱内 `curl` 与 PowerShell `Invoke-WebRequest` **全部 TLS 握手失败**，最终是 `web_fetch` 工具跑通的：

| 手段 | 结果 |
| --- | --- |
| `curl`（Windows 自带 8.22.0，Schannel 后端） | `curl: (35) schannel: AcquireCredentialsHandle failed: SEC_E_NO_CREDENTIALS (0x8009030e)`，`HTTP=000` |
| `curl --ssl-backend openssl` | 该构建不支持此选项（`Protocols` 里 Schannel 为唯一 TLS 后端） |
| PowerShell `Invoke-WebRequest`（默认 / 强制 TLS1.2 / TLS1.3） | `基础连接已经关闭: 接收时发生错误` |
| `web_fetch` | **成功**，返回 HTTP 200 + JSON |

> 结论：本机沙箱下要打 `auth.sicau.edu.cn`，用 `web_fetch`；`curl` 因缺 Schannel 凭据不可用。若后续要在代码里复现，Go 的 `net/http` 走自己的 TLS 栈，不受此影响，但仍需在目标机器上单独验证。

### 频率限制自查

- 本任务**真正到达服务端**的请求共 **2 次**：`GET /api/v1/cas/login?appid=<门户appid>`（1 次）、`GET /api/v1/captcha?appid=<门户appid>`（1 次）。
- 另有 3 次本机尝试**在 TLS 握手阶段即失败**（curl 1 次 `SEC_E_NO_CREDENTIALS`、curl 1 次参数不支持、PowerShell 1 次连接中断），**未产生服务端请求**。
- 两次成功请求间隔 > 15 秒，**未观察到**任何限流/锁定返回。
- **已停止继续探测**，不再对该端点发请求。

---

## 与既有笔记的一致性

| 既有材料 | 一句话结论 |
| --- | --- |
| 知识库「川农 e 认证「一次登录处处可达」靠 go-admin-tgt cookie 免密换票」（`%USERPROFILE%\.dsh\notes\topics\sicau-sso\`，仓外条目，不随本仓提交） | 该笔记的「换票成功」实测**只覆盖了教务 appid** `b54a2f27-…`；本次对**门户 appid** 未取得同类证据，两者不能互相替代，该笔记**不构成本问题的答案**。 |
| 同上笔记「换票 GET 的 appid 参数决定给哪个应用发票；传错 appid 拿到的 ticket 回投会 INVALID_TICKET」 | 与本次观察**一致且更强化**：换票接口确实以 `appid` 为发放维度，故门户 appid 必须单独走一遍，不能复用教务的票。 |
| 同上笔记「cookie 名不是标准 CAS 的 TGC，是自研的 go-admin-tgt」 | 一致；本次未登录态报 `msg="未登录"`，也印证该平台是自研 JSON 登录，不是标准 CAS 的 401 语义。 |
| 知识库「川农 e 认证响应体 AES 解密」（`%USERPROFILE%\.dsh\notes\topics\sicau-sso\`，仓外条目） | 一致；本次 `captcha` 响应确为「明文信封 + 加密 `data` + 独立 `requestId` + `id`」结构，可直接套该解密方法；`cas/login` 失败时 `data=null`，印证「`data` 为 null 不能解密」。 |
| 知识库「验证码不接 OCR：靠会话复用把「每次输入」降为「首次输入」」（`%USERPROFILE%\.dsh\notes\topics\sicau-sso\`，仓外条目） | 一致；本次 `captcha` 对门户 appid 能正常出图，说明「首次输入一次、之后靠 cookie 免码」的前置条件（能开事务、能出验证码）在门户侧同样成立。 |
| 知识库「别用 serviceValidate+TGT 验证自研 CAS 平台的免密换票」（`%USERPROFILE%\.dsh\notes\topics\sicau-sso\`，仓外条目） | **本次严格遵守**：没有去猜标准 CAS 端点，直接打前端 bundle 里实证过的 `GET /api/v1/cas/login?appid=`。 |
| 仓内契约 [`docs/tech/旧农屿WebVPN可重写契约清单.md`](./旧农屿WebVPN可重写契约清单.md) §4.1 / §4.3 | `WEBVPN_PORTAL_APPID = aea49a21-…`、`loginWebVpnPortal` 用 `appid:WEBVPN_PORTAL_APPID` **走 POST /api/v1/cas/login（账密 + 验证码）**，即旧实现**从来没有**用 go-admin-tgt 给门户 appid 换过票 —— 旧代码不提供任何免密换票的先例。 |
| 同上 §5.2「失败响应 `data` 是 `null`」「`GET captcha` → 200 JSON `code=200`，含加密 `data` + `id` + `requestId`」 | 两条均被本次实测**逐字复现**，契约可信。 |

---

## 未验证与下一步

### 还差什么才能定论

**一句话：需要一份有效的 `go-admin-tgt` cookie 值，用它打一次 `GET /api/v1/cas/login?appid=aea49a21-6b26-4e8f-bcfc-80e3de591c93`，看 `code` 是否 200、`data` 解密后是否含 `st` 且 `callbackUrl` 是否指向 `https://webvpn.sicau.edu.cn/`。**

具体的前置条件（按优先级）：

1. **拿到 cookie**：用户本人已在浏览器登录过 `auth.sicau.edu.cn`，用 CDP 导出（cookie 是 HttpOnly，`document.cookie` 读不到），或从已登录的客户端请求头里截。**该值属长期凭据，禁止写入代码/日志/笔记/对话。**
2. **只看一个判据就能定论**：`code==200` 且解密 `data` 内含 `st`。若返回 `msg="未登录"` 即 cookie 失效；若返回其他业务错误（如 appid 相关文案），则说明门户 appid 不支持免密换票 —— 届时才是**证伪**。
3. 若要闭环到「门户真的免密进门」，还需：把 `st` 按 §4.3 的 `buildCallbackUrl` 语义回投门户 service `https://webvpn.sicau.edu.cn/login?cas_login=true`，跟随 302，看是否种下 `wengine_vpn_ticket*` 且不出现 `INVALID_TICKET`。

### 下一步建议（不改动其他文件，仅建议）

- 让维护者在浏览器登录后导出 `go-admin-tgt`，**只把「调用结果」告诉我**（`code` 值 + 解密后是否含 `st` + `callbackUrl` 主机名），不必传 cookie 本身 —— 这样可以在不泄露凭据的前提下定论。
- 定论前，后端实现应**按「门户 appid 免密换票可能不成立」做降级设计**：即 `loginWebVpnPortal` 保持账密 + 验证码路径（与旧农屿一致），把免密换票作为可开关的加速路径，失败即回落。
- 反面证据的处理：若拿到 cookie 后返回 appid 相关业务错误，请据此**更新**知识库那条「一次登录处处可达」笔记，补上「仅教务 appid 实测，门户 appid 不支持/未验证」的边界。

---

## 不适用说明

1. **本笔记不构成「门户 appid 支持免密换票」的证据**。本轮只做了未登录态探测，任何把它读成「已支持」的引用都是误读。
2. **不适用于标准 CAS（Apereo）服务端**。`GET /api/v1/cas/login?appid=` 换票是川农自研平台的私有接口；标准 CAS 用 `serviceValidate`/`proxyValidate` + TGC，不要照搬本笔记的任何结论。
3. **不适用于其他学校的 `auth.*` 域名**，即使前端同样是 go-admin 系；`":sicau-cas"` 盐、`go-admin-tgt` 名称均为本平台特有。
4. **`captcha` 返回 `code=200` 不能推出「可免密换票」**。它只证明该 appid 在平台注册、能开登录事务；免密换票依赖的是 `go-admin-tgt` 的有效性，是另一条独立链路。
5. **本次未验证 TLS/网络层细节的普适性**：沙箱内 `curl` 的 `SEC_E_NO_CREDENTIALS` 是**本机沙箱环境**问题，不代表目标服务器或生产环境不可达；勿据此改生产 HTTP 客户端配置。
6. **不要复用本笔记里的任何 `<REDACTED>` 去猜真实值** —— `requestId`、`id`、`data` 密文均与当次事务绑定，过期即无效。
