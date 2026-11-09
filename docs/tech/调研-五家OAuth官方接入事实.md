# 调研：五家 OAuth 官方接入事实（QQ / 微信 / Google / X(Twitter) / Apple）

> 调研日期：2026-11-09。本文只记录**一手官方文档、官方 SDK 仓库源码、RFC**能确证的技术事实，不替项目下架构决策。凡本轮**无法联网从一手来源确认**的条目，统一标注 **待实测/待核实**，见文末清单。所有 URL 均为可点击一手链接；二手资料（CSDN / 博客 / StackOverflow / 社区帖）仅作线索，**不作为结论依据**，如引用会明确标注「二手」。

## 一句话结论

五家里 **Google / Apple 是标准 OIDC（有 discovery、JWKS、RS256 签名 id_token），X 是标准 OAuth 2.0 Authorization Code + PKCE（无 OIDC id_token），而 QQ 互联 / 微信是各自私有化改造的 OAuth2.0 授权码变体**：都要求服务端持 client_secret 换 token、都无 JWKS、身份主键分别是 openid/unionid 与 openid/unionid，且**微信移动应用登录强制走官方原生 SDK 拉起微信客户端**（不是网站 OAuth 的浏览器 redirect）。QQ 移动 SDK 甚至官方文档仍在提供 Implicit Grant（隐式）流程，与 Google/X/Apple 禁止或弃用隐式流程的方向相反 —— 这是最容易被混为一谈的差异点。

---

## 通用基线（RFC，五家共用的判定尺子）

| 项 | 事实 | 引用 |
| --- | --- | --- |
| PKCE | 为授权码拦截攻击而生；`code_verifier` 43–128 字符、`S256` 为服务器 **MTI（强制实现）**，客户端「能用 S256 就必须用 S256」，禁止从 S256 降级到 plain | [RFC 7636 §4.2 / §7.2](https://datatracker.ietf.org/doc/html/rfc7636) |
| 客户端机密性 | 原生 App 内嵌的 `client_secret` 「不能被视作机密」（`Secrets provisioned in client binary applications cannot be considered confidential`），同一 `client_id` 被所有实例共用 | [RFC 7636 §1](https://datatracker.ietf.org/doc/html/rfc7636) |
| 授权码 | 标准流程为 `response_type=code`，token endpoint 用 `grant_type=authorization_code` | [RFC 6749 §4.1](https://datatracker.ietf.org/doc/html/rfc6749) |
| state / CSRF | `state` 用于维持请求与回调状态、防 CSRF，授权服务器原样带回 | [RFC 6749 §10.12](https://datatracker.ietf.org/doc/html/rfc6749#section-10.12) |
| OIDC / id_token | OIDC Core 在 OAuth2 之上定义 `id_token`（JWT）、标准 claims、`sub`、`nonce`、签名校验 | [OIDC Core 1.0](https://openid.net/specs/openid-connect-core-1_0.html) |
| JWT access token | 定义 JWT 作为 access token 的 profile（本笔记仅作名词对齐，五家中**无一家**给 access_token 用 JWT profile） | [RFC 9068](https://datatracker.ietf.org/doc/html/rfc9068) |
| 原生 App 浏览器 | 推荐系统浏览器（而非嵌入式 WebView），自定义 scheme / claimed HTTPS 回调 | [RFC 8252](https://datatracker.ietf.org/doc/html/rfc8252) |

---

## 1. QQ 互联（connect.qq.com / wiki.connect.qq.com，国内）

> 官方把接入拆成「网站接入（Server-side / Client-side 两套 OAuth2.0）」与「移动应用接入（iOS/Android/HarmonyOS SDK）」两条线；**移动端 SDK 流程与网站 OAuth 不同**，且移动/网站是不同的 appid 绑定关系。

### 端点 / 流程 / 凭据 / 移动端约束 / 引用

| 维度 | 官方事实 | 引用 |
| --- | --- | --- |
| 是否有 OAuth2.0 | 是，自称「QQ登录OAuth2.0」，支持网站接入与移动应用接入两种方式；**未找到官方 OIDC / discovery / id_token 说明**（无 OIDC 能力） | [OAuth2.0简介](https://wiki.connect.qq.com/oauth2-0%E7%AE%80%E4%BB%8B/) |
| authorize | `GET https://graph.qq.com/oauth2.0/authorize`（`response_type=code`、`client_id`=appid、`redirect_uri`、`state`、`scope`、`display=mobile`） | [使用Authorization_Code获取Access_Token](https://wiki.connect.qq.com/%e4%bd%bf%e7%94%a8authorization_code%e8%8e%b7%e5%8f%96access_token/) |
| token | `GET https://graph.qq.com/oauth2.0/token`（`grant_type=authorization_code`、`client_id`、`client_secret`=appkey、`code`、`redirect_uri`；可选 `fmt=json`、`need_openid=1`） | 同上 |
| userinfo（自定义，非 OIDC userinfo） | `GET https://graph.qq.com/oauth2.0/me?access_token=...&unionid=1` 返回 `openid`（`unionid=1` 时额外返回 `unionid`）；另 `GET https://graph.qq.com/user/get_user_info`（昵称/头像/性别） | [获取用户OpenID](https://wiki.connect.qq.com/%e8%8e%b7%e5%8f%96%e7%94%a8%e6%88%b7openid_oauth2-0/)、[概念和术语·UnionID](https://wiki.connect.qq.com/%e6%a6%82%e5%bf%b5%e5%92%8c%e6%9c%af%e8%af%ad/)、[get_user_info](https://wiki.connect.qq.com/get_user_info/) |
| JWKS | **未找到官方说明**（QQ 不发签名 id_token，走 opaque access_token + `/me` 回调） | — |
| revoke | **未找到官方说明**（token 文档只列 code 换 token / refresh_token 续期） | [获取Access_Token](https://wiki.connect.qq.com/%e4%bd%bf%e7%94%a8authorization_code%e8%8e%b7%e5%8f%96access_token/) |
| PKCE | **未找到官方说明**；文档流程是 server-side `client_secret` 换 token，与 PKCE public-client 模型不同 | 同上 |
| 隐式流程 | **官方仍提供 Implicit Grant（client-side）**：`response_type=token`，`#access_token=...` 挂在回调 URL fragment 上，文档称「适用于电脑/移动终端上的客户端访问」 | [使用Implicit_Grant方式获取Access_Token](https://wiki.connect.qq.com/%e4%bd%bf%e7%94%a8implicit_grant%e6%96%b9%e5%bc%8f%e8%8e%b7%e5%8f%96access_token/) |
| 移动端 SDK | 官方提供 iOS / Android / HarmonyOS SDK；SDK「支持 web view 与浏览器两种模式」，但文档建议移动应用用 **webview** 打开登录页取 access_token+openid；APP 内嵌 H5 用 QQ 登录「互联本身是不支持的」 | [OAuth2.0简介](https://wiki.connect.qq.com/oauth2-0%E7%AE%80%E4%BB%8B/)、[获取Access_Token](https://wiki.connect.qq.com/%e4%bd%bf%e7%94%a8authorization_code%e8%8e%b7%e5%8f%96access_token/) |
| redirect URI | 网站侧 `redirect_uri` 必须是「注册 appid 时填写的主域名下的地址」并 URLEncode；iOS 侧有 **Universal Links 适配**（OpenSdk 3.3.7+ / 手Q 8.1.3+ / iOS13+，需 `apple-app-site-association`） | [获取Access_Token](https://wiki.connect.qq.com/%e4%bd%bf%e7%94%a8authorization_code%e8%8e%b7%e5%8f%96access_token/)、[Universal Links适配FAQ](https://wiki.connect.qq.com/universal-links%e9%80%82%e9%85%8dfaq/) |
| 凭据 | `access_token`（默认 60 天 / 7776000 秒，文档示例 5184000）、`refresh_token`（续期最长 3 个月、仅一次有效）、**`openid`（应用内唯一，跨应用不同）**、**`unionid`（同一 QQ 互联平台下不同应用相同，需申请打通，最多 60 个应用，建议预留 64 字节）** | [获取Access_Token](https://wiki.connect.qq.com/%e4%bd%bf%e7%94%a8authorization_code%e8%8e%b7%e5%8f%96access_token/)、[概念和术语](https://wiki.connect.qq.com/%e6%a6%82%e5%bf%b5%e5%92%8c%e6%9c%af%e8%af%ad/) |
| 校验方式 | 服务端用 access_token 调 `/oauth2.0/me` 取 openid（错误码 `100016` = 凭据过期/不正确、`100048` = 未申请 unionID 权限）；**无 JWT 签名校验** | [概念和术语](https://wiki.connect.qq.com/%e6%a6%82%e5%bf%b5%e5%92%8c%e6%9c%af%e8%af%ad/) |
| state | server-side 流程 `state` **必填**、回调原样带回、文档明确「请务必严格按照流程检查用户与 state 参数状态的绑定」；client-side 流程 state 可选 | [获取Access_Token](https://wiki.connect.qq.com/%e4%bd%bf%e7%94%a8authorization_code%e8%8e%b7%e5%8f%96access_token/) |
| client_secret | **需要**：`client_secret`=appkey，用于 code 换 token / refresh。**官方未找到「App 端可否存放 secret」的明文态度**（但流程把 secret 用在服务端 token 请求上） | 同上 |

### 要点与易混淆点
- **QQ 有两套流程**：网站 server-side（授权码 + client_secret）与 client-side（Implicit Grant，access_token 直接进 fragment）。移动端既可用 SDK，也可「自主开发」，官方对移动端直接点名 Implicit Grant。因此「QQ 支持 PKCE / 禁隐式」**不成立**。
- 身份主键是 `openid`（每应用不同），跨应用靠 `unionid`，且 unionid **需申请打通**、不是默认就有。
- 无 OIDC、无 JWKS、无 id_token，服务端「校验」= 拿 access_token 去 `/me` 问 openid。
- **官方最后能确认到的版本/日期**：WIKI 页脚为 `Copyright © 1998–2026 Tencent`；单页**未找到明确「最后更新」日期**（get_user_info 页保留了 2012/2013 的 What's New，属旧文档）。**待实测/待核实**：这些 WIKI 页面的实际最后修订日期、以及 QQ 移动 SDK 当前是否已支持/要求 PKCE。

---

## 2. 微信开放平台 · 移动应用微信登录（developers.weixin.qq.com，国内）

> **关键区分**：本文核实的是「移动应用微信登录」（`Mobile_App/WeChat_Login/`），**不是**网站应用微信登录（`Website_App/WeChat_Login/`），也不是小程序登录。移动应用登录官方文档明确「只提供原生的登录方式，需要用户安装微信客户端」，通过官方 SDK 拉起微信 App 授权后回传 code，属于 SDK 拉起流程，**不是浏览器 302 redirect 的网站 OAuth**。

### 端点 / 流程 / 凭据 / 移动端约束 / 引用

| 维度 | 官方事实 | 引用 |
| --- | --- | --- |
| 是否有 OAuth2.0 | 是，官方称「基于 **OAuth2.0 协议标准** 构建的微信 OAuth2.0 授权登录系统」，「目前支持 **authorization_code 模式**，适用于拥有 server 端的应用授权」；**未找到官方 OIDC / discovery / id_token 说明** | [移动应用微信登录开发指南](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Development_Guide.html) |
| authorize（SDK 拉起，非 URL） | 无浏览器 authorize URL；App 用 SDK 构造 `SendAuthReq`（`scope=snsapi_userinfo`、`state`），调用 `WXApi.sendReq` / `api.sendReq` **拉起微信客户端**，用户确认后微信拉回 App 并带上 `code`。iOS 需 `isWXAppInstalled` 检测，未装微信时按 App Store 规则隐藏按钮 | [开发指南·第一步](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Development_Guide.html) |
| token（code 换） | `GET https://api.weixin.qq.com/sns/oauth2/access_token?appid=APPID&secret=SECRET&code=CODE&grant_type=authorization_code` | [开发指南·第二步](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Development_Guide.html)、[授权后接口调用（UnionID）](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Authorized_API_call_UnionID.html) |
| refresh | `GET https://api.weixin.qq.com/sns/oauth2/refresh_token?appid=APPID&grant_type=refresh_token&refresh_token=REFRESH_TOKEN` | 同上 |
| 校验 token | `GET https://api.weixin.qq.com/sns/auth?access_token=ACCESS_TOKEN&openid=OPENID` | [授权后接口调用](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Authorized_API_call_UnionID.html) |
| userinfo | `GET https://api.weixin.qq.com/sns/userinfo?access_token=ACCESS_TOKEN&openid=OPENID&lang=zh_CN` | 同上 |
| JWKS / revoke | **均未找到官方说明**（无 id_token，无 JWKS；文档未列 revoke 端点） | — |
| PKCE / 隐式 | **未找到官方说明**：文档只支持 authorization_code 模式，未提 PKCE，也未提隐式流程 | [开发指南](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Development_Guide.html) |
| 移动端 SDK | **强制**：「移动应用上微信登录只提供原生的登录方式，需要用户安装微信客户端」；需 iOS / Android / 鸿蒙 OpenSDK | [开发指南](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Development_Guide.html)、[接入指南目录](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/Access_Guide/iOS.html) |
| redirect URI 形态 | 移动 SDK 是通过 App 间跳转回传 code；平台侧要求 iOS 配置 **Universal Links**（`apple-app-site-association`）、Android 配置**应用签名/包名**。«微信开放文档» 目录含「配置 iOS 应用备用 universal link」「配置 Android 应用备用签名」。**注意**：这与网站 OAuth 的 https 回调域名是两套东西 | [开放平台导航](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Development_Guide.html)、[iOS 接入指南](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/Access_Guide/iOS.html)、[Android 接入指南](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/Access_Guide/Android.html) |
| 凭据 | `access_token`（2 小时）、`refresh_token`（180 天）、`openid`（授权用户唯一标识）、`unionid`（当且仅当移动应用已获 userinfo 授权时出现；**同一微信开放平台账号下不同应用/公众号/小程序，同一用户 unionid 唯一**）、`scope=snsapi_userinfo` | [开发指南·第二步](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Development_Guide.html)、[授权后接口调用](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Authorized_API_call_UnionID.html) |
| state | `SendAuthReq.state` 可选但**建议必带**防 CSRF，原样回传，长度 ≤ 1K，建议 urlencode | [开发指南·参数说明](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Development_Guide.html) |
| code 特性 | 超时 10 分钟、**一次性**（换取一次即失效） | [开发指南·FAQ](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Development_Guide.html) |
| client_secret 态度 | **官方明文**：`AppSecret` 是应用密钥，「存储在客户端，极有可能被恶意窃取（如反编译获取 AppSecret）」；`access_token`/`refresh_token` 存客户端有泄漏风险；**「建议将 AppSecret、用户数据（如 access_token）放在 App 云端服务器，由云端中转接口调用请求」** | [开发指南·第三步注意](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Development_Guide.html) |
| 其他约束 | 接口频率：同一用户（openid）一分钟内上述接口合计 ≤ 180 次（超限 45011）；未上架应用微信登录 100 次/天（错误码 10060）；2021-10-20 起 userinfo 不再返回性别/地区（旧字段仍留在示例里） | [开发指南·第五步](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Development_Guide.html)、[授权后接口调用](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Authorized_API_call_UnionID.html) |

### 要点与易混淆点
- **没有 authorize URL，也没有浏览器 redirect**：第一步是 SDK `SendAuthReq` → 拉起微信客户端 → `SendAuth.Resp` 带回 `code`。把它当成「标准 OAuth2.0 授权码 + 浏览器回调」会踩空。
- 换 token 在**服务端**做（AppSecret 必须留云端，官方明文禁止放客户端）。
- 身份主键：`openid`（每应用不同）+ `unionid`（开放平台账号下跨应用唯一）。官方建议保存 unionID 做跨应用互通。
- 无 OIDC、无 id_token、无 JWKS；服务端「校验」= `/sns/auth` + `/sns/userinfo`。
- **官方最后能确认到的版本/日期**：《移动应用微信登录开发指南》与《授权后接口调用（UnionID）》页面均标注 **「最后一次编辑于 2026-06-25」**。**待实测/待核实**：微信移动登录是否（未来）支持 PKCE；Android App Links 是否被官方作为标准回调形态明确文档化（目录里只见「备用签名」，未逐页核实）。

---

## 3. Google（OAuth 2.0 for Mobile & Desktop Apps / Identity Services，境外）

### 端点 / 流程 / 凭据 / 移动端约束 / 引用

| 维度 | 官方事实 | 引用 |
| --- | --- | --- |
| 是否有 OAuth2.0 / OIDC | 是。desktop/native app 用 OAuth 2.0 授权码 + PKCE；同时是标准 OIDC Provider，有 discovery 文档与 id_token | [OAuth 2.0 for Desktop Apps](https://developers.google.com/identity/protocols/oauth2/native-app)、[OIDC discovery](https://accounts.google.com/.well-known/openid-configuration) |
| authorize | `https://accounts.google.com/o/oauth2/v2/auth`（仅 SSL，拒绝 HTTP） | [native-app Step 2](https://developers.google.com/identity/protocols/oauth2/native-app)、[discovery](https://accounts.google.com/.well-known/openid-configuration) |
| token | `https://oauth2.googleapis.com/token` | [discovery](https://accounts.google.com/.well-known/openid-configuration) |
| userinfo | `https://openidconnect.googleapis.com/v1/userinfo` | 同上 |
| JWKS | `https://www.googleapis.com/oauth2/v3/certs` | 同上 |
| revoke | `https://oauth2.googleapis.com/revoke` | [native-app Token revocation](https://developers.google.com/identity/protocols/oauth2/native-app)、[discovery](https://accounts.google.com/.well-known/openid-configuration) |
| 移动端授权方式 | installed/desktop app 被视作 **public client**（「cannot keep secrets」），官方：「**Always use PKCE** when requesting authorization codes」；`response_type=code`；`code_challenge_method` 支持 `S256`（推荐）与 `plain`（默认 plain） | [native-app 创建凭据 / Step 1 / Step 2](https://developers.google.com/identity/protocols/oauth2/native-app) |
| 隐式流程态度 | discovery 仍列 `token`、`id_token` 等 response_type（即旧隐式/OIDC 混合响应类型能力仍在元数据中），但官方客户端隐式流文档称「Interacting ... to implement the implicit flow is **strongly discouraged** due to security vulnerabilities」；另有 2022 年博客宣布弃用 OOB、禁用 loopback 等旧流程 | [JavaScript implicit-flow 文档](https://developers.google.com/identity/protocols/oauth2/javascript-implicit-flow)（本条为搜索摘要，正文受工具限流未逐字复核，标注见待核实清单）、[Google 2022 安全博客](https://developers.googleblog.com/making-google-oauth-interactions-safer-by-using-more-secure-oauth-flows/)（二手索引） |
| 浏览器 / SDK | 官方推荐用 Google API Client Libraries；native/desktop app **必须打开系统浏览器**并配本地 redirect；移动端另有 Android / iOS 专属授权指南。Google 官方另有「Remediation for OAuth via WebView」帮助页，要求用 **Chrome Custom Tabs** 取代 WebView 做认证 | [native-app Libraries](https://developers.google.com/identity/protocols/oauth2/native-app)、[Android 授权](https://developer.android.com/identity/authorization)、[WebView 整改帮助](https://support.google.com/faqs/answer/12284343?hl=en)（二手索引） |
| redirect URI 形态 | 支持 **Custom URI scheme**（`com.example.app:redirect_uri_path` 或 `com.googleusercontent.apps.123:redirect_uri_path`，scheme 须含句点）与 **Loopback IP**（`http://127.0.0.1:port`，仅桌面；**iOS/Android/Chrome 客户端类型已 DEPRECATED**）；还列 Manual copy/paste（Deprecated）。Android App Links / iOS Universal Links 的「标准」形态在本文档未逐条展开（见 Android 专属指南） | [native-app App redirect methods](https://developers.google.com/identity/protocols/oauth2/native-app) |
| 凭据 | `access_token`、`refresh_token`（`access_type=offline`）、OIDC `id_token`（标准 claims）。`id_token_signing_alg_values_supported = ["RS256"]`，`issuer=https://accounts.google.com` | [discovery](https://accounts.google.com/.well-known/openid-configuration)、[native-app](https://developers.google.com/identity/protocols/oauth2/native-app) |
| 校验方式 | 服务端用 JWKS（`RS256`）验 id_token 签名 + 校验 `iss`/`aud`/`exp`；access_token 可经 tokeninfo/userinfo 或 revoke/refresh 管理 | [discovery](https://accounts.google.com/.well-known/openid-configuration)、[go-oidc README 示例](https://github.com/coreos/go-oidc) |
| state / nonce | `state` 推荐（维持请求/回调状态）；OIDC `nonce` 由 OIDC Core 定义用于 id_token 防重放 | [native-app Step 2](https://developers.google.com/identity/protocols/oauth2/native-app)、[OIDC Core](https://openid.net/specs/openid-connect-core-1_0.html) |
| client_secret | Desktop app 是 **public client**、「cannot keep secrets」，但仍会生成 client ID/secret；官方要求配合 PKCE。App 端存 secret 的官方态度：native-app 文档强调无法保密（= 不应把 secret 当机密） | [native-app 创建凭据](https://developers.google.com/identity/protocols/oauth2/native-app) |

### 要点
- 五家里最「标准」的一家：完整的 discovery + userinfo + JWKS + revoke + RS256。
- 对 installed app 明确 **public client + PKCE**；`code_challenge_methods_supported = ["plain","S256"]`，但 RFC 7636 与 Google 文档都推荐 S256。
- **官方最后能确认到的版本/日期**：`developers.google.com` 该页未显示单页更新日期；`naver-app` 文档引用的弃用时间点来自 2022 年博客（新客户端 2022-03-14 起禁用 loopback、2022-08-31 存量禁用）。**待实测/待核实**：Google Identity Services（新版 GIS）与「OAuth 2.0 for Desktop Apps」在移动端的分工，本轮未逐页核实 GIS 移动端文档。

---

## 4. X / Twitter（docs.x.com / developer.x.com，境外）

### 端点 / 流程 / 凭据 / 移动端约束 / 引用

| 维度 | 官方事实 | 引用 |
| --- | --- | --- |
| 是否有 OAuth2.0 | 是，**OAuth 2.0 Authorization Code Flow with PKCE**（用于「Sign In with X」/ 代表用户调用 v2 API）；另有 App-only OAuth 2.0 Bearer Token（不涉及用户）。**未找到官方 OIDC / discovery / id_token 说明**（不是 OIDC Provider） | [OAuth 2.0 概览](https://docs.x.com/fundamentals/authentication/oauth-2-0/overview)、[Authorization Code Flow with PKCE](https://docs.x.com/fundamentals/authentication/oauth-2-0/authorization-code) |
| authorize | `https://x.com/i/oauth2/authorize`（示例：`?response_type=code&client_id=...&redirect_uri=https://www.example.com&scope=tweet.read%20users.read...&state=state&code_challenge=challenge&code_challenge_method=plain`） | [authorization-code](https://docs.x.com/fundamentals/authentication/oauth-2-0/authorization-code) |
| token | `POST https://api.x.com/2/oauth2/token`（`grant_type=authorization_code`、`code`、`client_id`、`redirect_uri`、`code_verifier`；confidential client 可改用 `Authorization: Basic`） | [user-access-token](https://docs.x.com/fundamentals/authentication/oauth-2-0/user-access-token) |
| revoke | `POST https://api.x.com/2/oauth2/revoke`（`token`、`client_id`；confidential 用 Basic）。官方注明「The revoke token is for an App to revoke a token and not a user」；用户也可在 `x.com/settings/connected_apps` 撤销 | [user-access-token](https://docs.x.com/fundamentals/authentication/oauth-2-0/user-access-token) |
| userinfo / JWKS | **未找到官方 userinfo/JWKS 说明**；用户身份经 `GET /2/users/me` 类 v2 端点（带 access_token）取得，而非 OIDC userinfo；无 id_token、无 JWKS 验签 | [OAuth 2.0 概览](https://docs.x.com/fundamentals/authentication/oauth-2-0/overview) |
| 移动端授权方式 / PKCE | 明确 Authorization Code + **PKCE**；参数 `code_challenge` / `code_challenge_method`（**S256 或 plain** 均列出）。开发者控制台 App 类型：`Native App` / `Single page App` = **public client**（无 client secret），`Web App` / `Automated App or bot` = **confidential client**（有 client secret）。public client 必须在请求体带 `client_id` | [authorization-code.md](https://docs.x.com/fundamentals/authentication/oauth-2-0/authorization-code.md)、[user-access-token](https://docs.x.com/fundamentals/authentication/oauth-2-0/user-access-token) |
| OAuth 1.0a 现状 | 仍受支持但**正在退役（being retired）**：官方提供 `grant_type=urn:ietf:params:oauth:grant-type:token-exchange` 到 `POST /2/oauth2/token` 把存量 1.0a token 换成 2.0 token；「OAuth 1.0a token remains valid until OAuth 1.0a is retired」「v1.1 endpoints ... keep working」「After retirement, the user must re-authorize via the OAuth 2.0 flow」。官方迁移文档另有「Apps have until **January 31, 2027**, to move to OAuth 2.0. After that date, requests signed with OAuth 1.0a stop ...」 | [oauth-1-0a-token-exchange](https://docs.x.com/fundamentals/authentication/oauth-2-0/oauth-1-0a-token-exchange)、[X API changelog](https://docs.x.com/changelog) |
| 浏览器 / SDK | 授权走系统浏览器打开 authorize URL；未找到「必须用官方移动端 SDK」的强制说明（可用 AppAuth / OAuth 通用库） | [authorization-code](https://docs.x.com/fundamentals/authentication/oauth-2-0/authorization-code) |
| redirect URI | `redirect_uri` 必须是 App 设置里登记的 Callback URL 之一（示例为 `https://www.example.com`） | [authorization-code](https://docs.x.com/fundamentals/authentication/oauth-2-0/authorization-code) |
| 凭据 | `access_token`（**2 小时过期**）、`refresh_token`（约 6 个月、单次使用、每次刷新返回新的，需 `offline.access` scope）、scope（如 `tweet.read users.read offline.access`）、`state` | [oauth-1-0a-token-exchange 前置条件](https://docs.x.com/fundamentals/authentication/oauth-2-0/oauth-1-0a-token-exchange)、[authorization-code](https://docs.x.com/fundamentals/authentication/oauth-2-0/authorization-code) |
| 校验 | access_token 作 Bearer 调 v2 端点；无 JWT 签名校验（无 JWKS） | [OAuth 2.0 概览](https://docs.x.com/fundamentals/authentication/oauth-2-0/overview) |
| client_secret 态度 | 官方按 App 类型区分：public client（Native App/SPA）**不使用** client secret，confidential client（Web App/bot）才有并应保密；confidential 用 `Authorization: Basic` 传 client_id:secret | [authorization-code.md](https://docs.x.com/fundamentals/authentication/oauth-2-0/authorization-code.md) |

### 要点与易混淆点
- X 是**标准 OAuth 2.0（不是 OIDC）**：有 authorize/token/revoke，但**没有 id_token / JWKS / discovery**，别拿 go-oidc 去验 X 的「id_token」。
- PKCE 支持 `S256` 与 `plain`（官方参数表两者都列），但 RFC 7636 要求能用 S256 就必须 S256 —— 实现应固定 S256。
- **OAuth 1.0a 仍在用但已被判定退役**，存量用户可用 token-exchange 无感迁移；有 2027-01-31 的时间点（来自官方迁移文档/搜索摘要，正文受工具限流未逐字复核，见待核实清单）。
- **官方最后能确认到的版本/日期**：`authorization-code.mdx` 的 `lastModified` = **2026-09-22T18:13:13Z**；`oauth-1-0a-token-exchange` 页面时间戳 **2026-09-23**；`docs.x.com/changelog` 抓取时间 **2026-11-06**。

---

## 5. Apple · Sign in with Apple（developer.apple.com，境外）

### 端点 / 流程 / 凭据 / 移动端约束 / 引用

| 维度 | 官方事实 | 引用 |
| --- | --- | --- |
| 是否有 OAuth2.0 / OIDC | 是。官方提供 Sign in with Apple REST API（授权码流程）+ 标准 **OIDC discovery 文档**；`issuer=https://appleid.apple.com`、`id_token_signing_alg_values_supported=["RS256"]`、`scopes_supported=["openid","email","name"]`、`subject_types_supported=["pairwise"]` | [OIDC discovery](https://appleid.apple.com/.well-known/openid-configuration)、[Authenticating users with Sign in with Apple](https://developer.apple.com/documentation/signinwithapple/authenticating-users-with-sign-in-with-apple) |
| authorize | `GET https://appleid.apple.com/auth/authorize`（`response_type` 合法值 `code` 或 `code id_token`；**只请求 `id_token` 不支持**；有 scope 时 `response_mode` 必须 `form_post`；`state`、`nonce`） | [Request an authorization](https://developer.apple.com/documentation/signinwithapplerestapi/request-an-authorization-to-the-sign-in-with-apple-server.)、[Incorporating into other platforms](https://developer.apple.com/documentation/signinwithapple/incorporating-sign-in-with-apple-into-other-platforms) |
| token | `POST https://appleid.apple.com/auth/token`（`client_id`、`client_secret`、`code`、`grant_type=authorization_code`、`redirect_uri`；refresh 用 `grant_type=refresh_token`） | [Token validation](https://developer.apple.com/documentation/signinwithapplerestapi/generate-and-validate-tokens) |
| JWKS | `GET https://appleid.apple.com/auth/keys`（返回 `JWKSet`） | [Fetch Apple's public key](https://developer.apple.com/documentation/signinwithapplerestapi/fetch-apple's-public-key-for-verifying-token-signature)、[discovery](https://appleid.apple.com/.well-known/openid-configuration) |
| revoke | `POST https://appleid.apple.com/auth/revoke`（`client_id`、`client_secret`、`token`、`token_type_hint=refresh_token|access_token`） | [Token revocation](https://developer.apple.com/documentation/signinwithapplerestapi/revoke-tokens) |
| userinfo | **未找到官方 userinfo 端点**（discovery 未列 `userinfo_endpoint`）；用户信息在 `id_token` claims + 首次授权的 `user` 对象里 | [discovery](https://appleid.apple.com/.well-known/openid-configuration)、[Request an authorization](https://developer.apple.com/documentation/signinwithapplerestapi/request-an-authorization-to-the-sign-in-with-apple-server.) |
| 移动端授权方式 | **原生 iOS**：用 `ASAuthorizationAppleIDProvider` + `ASAuthorizationController`（AuthenticationServices 框架），`ASAuthorizationAppleIDButton`，`requestedScopes=[.fullName,.email]`，结果给 `ASAuthorizationAppleIDCredential`。**Web / 其他平台（含 Android）**：手动构造 authorize URL、在浏览器标签打开，服务端接 `form_post`，再用自定义 URL scheme 把控制权交还 App（iOS 12 及更早 / Android）。`ASWebAuthenticationSession` 是 Apple 官方另一套做 web OAuth 会话的类。**未找到「强制 PKCE」的官方说明**（Apple 的 code 是单次、5 分钟有效） | [Implementing User Authentication with Sign in with Apple](https://developer.apple.com/documentation/authenticationservices/implementing-user-authentication-with-sign-in-with-apple)、[Incorporating into other platforms](https://developer.apple.com/documentation/signinwithapple/incorporating-sign-in-with-apple-into-other-platforms)、[ASWebAuthenticationSession](https://developer.apple.com/documentation/authenticationservices/aswebauthenticationsession) |
| redirect URI | 必须 **HTTPS**、含域名、不能是 IP / localhost、不能有 `#` fragment（REST 授权流程）；原生 App 走自定 URL scheme 回跳 | [Request an authorization](https://developer.apple.com/documentation/signinwithapplerestapi/request-an-authorization-to-the-sign-in-with-apple-server.)、[Incorporating into other platforms](https://developer.apple.com/documentation/signinwithapple/incorporating-sign-in-with-apple-into-other-platforms) |
| 凭据 / claims | `id_token`（JWT，claims：`iss=https://appleid.apple.com`、`aud=client_id`、`sub`、`iat`、`exp`、`nonce`、`nonce_supported`、`email`、`email_verified`、`is_private_email`、`real_user_status`、`transfer_sub`）、`access_token`、`refresh_token`、`authorization code`（单次、5 分钟） | [id_token](https://developer.apple.com/documentation/signinwithapplejs/authorizationi/id_token)、[Token validation](https://developer.apple.com/documentation/signinwithapplerestapi/generate-and-validate-tokens) |
| `sub` 与隐藏邮件 | `sub` 是「唯一、稳定、作为用户主键」的标识，**同一开发团队所有 App 相同、不同开发团队不同、停用后重新启用不变**；`email` 可能是真实邮箱或**私人邮件中继的代理地址**（`is_private_email` 标识，示例 `@privaterelay.appleid.com`），官方明确「**Don't use this value as an identifier of the user**」，要用 `sub` | [id_token](https://developer.apple.com/documentation/signinwithapplejs/authorizationi/id_token)、[Incorporating into other platforms](https://developer.apple.com/documentation/signinwithapple/incorporating-sign-in-with-apple-into-other-platforms) |
| 校验方式 | 服务端：用 `https://appleid.apple.com/auth/keys` 的公钥**验 JWS 签名**、校验 `nonce`、`iss=https://appleid.apple.com`、`aud=client_id`、`exp`。«Verifying a user» 正文写的是「Verify the JWS **E256** signature using the server's public key」，而 discovery 与 `id_token` 签名为 **RS256** —— 两处官方表述不一致，**待实测/待核实**（见清单） | [Verifying a user](https://developer.apple.com/documentation/signinwithapple/verifying-a-user)、[discovery](https://appleid.apple.com/.well-known/openid-configuration)、[Creating a client secret](https://developer.apple.com/documentation/accountorganizationaldatasharing/creating-a-client-secret) |
| state / nonce | `state` 防 CSRF（与回调值比对）；`nonce` 关联客户端会话与 id_token、防重放，仅当请求里传了才出现在 id_token；`nonce_supported=true` 而缺 nonce 时须判定失败 | [Request an authorization](https://developer.apple.com/documentation/signinwithapplerestapi/request-an-authorization-to-the-sign-in-with-apple-server.)、[id_token](https://developer.apple.com/documentation/signinwithapplejs/authorizationi/id_token) |
| client_secret | **不是静态串，而是开发者自己签的 JWT**：`alg=ES256`、`kid`=私钥 Key ID；payload `iss`=Team ID、`iat`、`exp`（最长 6 个月 / 15777000 秒）、`aud=https://appleid.apple.com`、`sub`=client_id；用从开发者账号下载的 **.p8 私钥（ECDSA P-256 / SHA-256）**签名 | [Creating a client secret](https://developer.apple.com/documentation/accountorganizationaldatasharing/creating-a-client-secret)、[Token validation](https://developer.apple.com/documentation/signinwithapplerestapi/generate-and-validate-tokens) |

### 要点与易混淆点
- Apple 是标准 OIDC（discovery/JWKS/RS256 id_token），但**没有 userinfo 端点**，用户信息全在 id_token 与首次 `user` 里。
- `client_secret` 是**动态生成的 ES256 JWT**（私钥在服务端，绝不下发到 App），这与 QQ/微信的静态 appkey/AppSecret、X 的静态 secret 都不同。
- 身份主键 `sub`（团队内稳定、跨团队不同），隐藏邮件是代理地址**不能当主键**。
- 原生 iOS 用系统 SDK（非浏览器）；Android/Web 走浏览器 + form_post + scheme 回跳。
- **官方最后能确认到的版本/日期**：Sign in with Apple REST API 页面标注 `Sign in with Apple REST API 1.0+`；Apple 文档页脚 `Copyright © 2026 Apple Inc.`；单页**未找到明确「最后更新」日期**。`id_token` 页标注 `Sign in with Apple JS 1.4.1+`。

---

## 横向对比矩阵

| 维度 | QQ 互联 | 微信（移动应用） | Google | X (Twitter) | Apple |
| --- | --- | --- | --- | --- | --- |
| 标准 | 私有化 OAuth2.0（server-side / client-side 两套） | 私有化 OAuth2.0（SDK 拉起） | OAuth2.0 + OIDC | OAuth2.0（非 OIDC） | OAuth2.0 + OIDC |
| OIDC discovery | 无 | 无 | 有 | 无 | 有 |
| authorize | `graph.qq.com/oauth2.0/authorize` | 无 URL（SDK `SendAuthReq`） | `accounts.google.com/o/oauth2/v2/auth` | `x.com/i/oauth2/authorize` | `appleid.apple.com/auth/authorize` |
| token | `graph.qq.com/oauth2.0/token` (GET) | `api.weixin.qq.com/sns/oauth2/access_token` (GET) | `oauth2.googleapis.com/token` | `api.x.com/2/oauth2/token` (POST) | `appleid.apple.com/auth/token` (POST) |
| userinfo | `/oauth2.0/me` + `/user/get_user_info` | `/sns/userinfo` + `/sns/auth` | `openidconnect.googleapis.com/v1/userinfo` | 未找到（用 v2 `/2/users/me`） | 未找到（在 id_token / `user`） |
| JWKS | 无 | 无 | `googleapis.com/oauth2/v3/certs` | 无 | `appleid.apple.com/auth/keys` |
| revoke | 未找到 | 未找到 | `oauth2.googleapis.com/revoke` | `api.x.com/2/oauth2/revoke` | `appleid.apple.com/auth/revoke` |
| id_token / 签名 | 无 | 无 | 有，RS256 | 无 | 有，RS256（官方另有 E256 表述，待核实） |
| PKCE | 未找到（且提供隐式流程） | 未找到 | 强制/推荐（public client，S256 推荐） | 支持（S256 或 plain） | 未找到强制说明 |
| 隐式流程 | **官方仍提供**（response_type=token） | 未提 | 官方「strongly discouraged」 | 未提（用授权码+PKCE） | 未提（code / code id_token） |
| 移动端必用官方 SDK | 可选（SDK 或自主开发） | **强制**（只提供原生登录、需装微信） | 否（推荐系统浏览器/Client Library） | 否（通用 OAuth 库可） | 原生 iOS **必用** AuthenticationServices；Android/Web 走浏览器 |
| 浏览器要求 | 建议 webview；APP 内嵌 H5 不支持 | 拉起微信客户端 | 系统浏览器 / Chrome Custom Tabs（禁 WebView 认证） | 系统浏览器 | 原生 SDK 或浏览器标签 |
| redirect 形态 | 主域名下 URL；iOS Universal Links | iOS Universal Links / Android 签名+包名 | 自定义 scheme / loopback（移动已弃用） | 登记的 Callback URL（https） | HTTPS 域名 + form_post，原生用 scheme 回跳 |
| 身份主键 | `openid`（应用内）/ `unionid`（需打通） | `openid`（应用内）/ `unionid`（开放平台内） | `sub`（OIDC） | v2 user id（无 OIDC sub） | `sub`（团队内稳定） |
| 服务端验签方式 | 拿 access_token 问 `/me` | `/sns/auth` + `/sns/userinfo` | JWKS RS256 验 id_token | Bearer 调 v2 端点 | JWKS 验 id_token + nonce/iss/aud/exp |
| client_secret | 静态 appkey，服务端 | 静态 AppSecret，**官方明文要求放云端** | public client（不能保密）；仍生成 secret | 按 App 类型：public 无 / confidential 有 | **动态 ES256 JWT**（.p8 私钥签） |
| state / nonce | state（server-side 必填） | state（建议必带） | state（推荐）/ OIDC nonce | state（示例带） | state + nonce（均官方定义） |
| 地区可用性 | 国内 | 国内 | 境外 | 境外 | 境外 |
| 官方页面可确认日期 | 页脚 ©1998–2026 | **2026-06-25** | 无单页日期（引用 2022 弃用节点） | **2026-09-22**（authorization-code.mdx） | 页脚 ©2026，REST API 1.0+ |

> 产品语境提示（仅陈述事实关联，不下决策）：QQ/微信属国内可用、Google/X/Apple 属境外可用；QQ 与微信的「移动应用 SDK 登录」与 Google/X/Apple 的「OAuth 2.0 授权码 + 系统浏览器」是两类不同流程，实现层不能复用同一套客户端抽象。

---

## Go 后端可复用库现状

### `golang.org/x/oauth2` 内置 Endpoint（一手来源：仓库 `endpoints/endpoints.go` 源码）

| Provider | 是否内置 | 内置值 | 引用 |
| --- | --- | --- | --- |
| Google | ✅ | `AuthURL: https://accounts.google.com/o/oauth2/auth`、`TokenURL: https://oauth2.googleapis.com/token`、`DeviceAuthURL: .../device/code` | [endpoints.go](https://raw.githubusercontent.com/golang/oauth2/master/endpoints/endpoints.go) |
| Apple | ✅ | `AuthURL: https://appleid.apple.com/auth/authorize`、`TokenURL: https://appleid.apple.com/auth/token` | [endpoints.go](https://raw.githubusercontent.com/golang/oauth2/master/endpoints/endpoints.go) |
| QQ | ✅ | `AuthURL: https://graph.qq.com/oauth2.0/authorize`、`TokenURL: https://graph.qq.com/oauth2.0/token` | [endpoints.go](https://raw.githubusercontent.com/golang/oauth2/master/endpoints/endpoints.go) |
| X (Twitter) | ✅ | `AuthURL: https://x.com/i/oauth2/authorize`、`TokenURL: https://api.x.com/2/oauth2/token` | [endpoints.go](https://raw.githubusercontent.com/golang/oauth2/master/endpoints/endpoints.go) |
| 微信 | ❌ 未内置 | 无 `Wechat`/`Weixin` 常量（全表无），须自定义 `oauth2.Endpoint` 或直接 HTTP | [endpoints.go](https://raw.githubusercontent.com/golang/oauth2/master/endpoints/endpoints.go) |

补充事实：
- 内置 Endpoint 只有 `AuthURL` / `TokenURL` / `DeviceAuthURL`，**不含** userinfo / JWKS / revoke；这些都要自己写 HTTP 调用或配别的库。
- `endpoints.go` 里 Google 的 `AuthURL` 是旧版 `https://accounts.google.com/o/oauth2/auth`，而 OIDC discovery 给的是 `https://accounts.google.com/o/oauth2/v2/auth`（v2）—— 两者并存，属版本差异而非冲突。
- QQ/X 的内置 Endpoint 只是「网站/标准授权码」端点；**微信移动 SDK 流程与 QQ 移动 SDK 流程用不上 `oauth2.Config.AuthCodeURL()`**（无浏览器 authorize 跳转），需另写 SDK 回传 code 的接收逻辑。
- 仓库 master 最新提交可确认到 **2026-11-06**（"oauth2: prevent NewTicker panic"），`google` 子包 2026-08-25、`endpoints` 2025-12-01 有改动。[仓库首页](https://github.com/golang/oauth2)

### `github.com/coreos/go-oidc/v3/oidc`（一手来源：官方 README）

- 是 OIDC 客户端：**依赖 OIDC discovery**（`oidc.NewProvider(ctx, issuerURL)`）、`provider.Endpoint()` 喂给 `oauth2.Config`、`provider.Verifier(...)` 验 `id_token`（`oauth2Token.Extra("id_token")`）。[README](https://github.com/coreos/go-oidc)
- README 明确列出可识别的 Consumer IdP 含 **Google**、**Apple (Sign in with Apple)**、Microsoft、Facebook Login、LinkedIn → 对 Google / Apple 适用；示例 issuer 即 `https://accounts.google.com`。[README](https://github.com/coreos/go-oidc)
- **对 QQ / 微信 / X**：三家都没有官方 OIDC discovery（无 `.well-known/openid-configuration`、无 id_token/JWKS），因此 `oidc.NewProvider` / `Verifier` **不适用**；它们要走各自的用户信息接口 + 自建绑定逻辑。
- Apple 的特殊点：discovery 存在（`appleid.apple.com/.well-known/openid-configuration`），但 `client_secret` 是动态 ES256 JWT、`token_endpoint_auth_methods_supported=["client_secret_post"]`，用 go-oidc + x/oauth2 时仍需自己生成 client_secret JWT（go-oidc 不会替你签）。
- 仓库 v3 分支最新可确认到 **2026-09-01**（v3.21.0 release，2026-09-01）。[README](https://github.com/coreos/go-oidc)

### 其它一手库线索
- `golang.org/x/oauth2/jwt`、`/jws`、`/google`、`/clientcredentials` 等子包存在（仓库目录可见），本轮未逐一核实其对 Apple ES256 client_secret 的适用性 → **待实测/待核实**。

---

## 「待实测 / 待核实」清单

以下条目本轮**未能从一手官方页面逐字确认**（工具限流 / 页面 JS 渲染 / 文档本身未写），按主题列出，供后续复核，切勿当既成事实使用：

1. **QQ 互联**：各 WIKI 页面的实际「最后更新」日期（页脚只有 ©1998–2026）；QQ 移动 SDK 当前是否支持或强制 PKCE；QQ 是否有 revoke 端点。
2. **微信**：移动应用微信登录是否（未来）支持 PKCE；「Android App Links」是否被官方作为标准回调形态明确文档化（目录仅见「配置 Android 应用备用签名」，未逐页核实）；`get_quota` / `authorization_change` 等相邻页未逐页读。
3. **Google**：`developers.google.com/identity/protocols/oauth2/javascript-implicit-flow` 关于隐式流程「strongly discouraged」的原文本轮仅经搜索摘要获得、正文受 429 限流未逐字复核；Google Identity Services（GIS）与「OAuth 2.0 for Desktop Apps」在 Android/iOS 移动端的分工未逐页核实；Chrome Custom Tabs / App Links 的官方要求散落在帮助页与博客，未全部读一手正文。
4. **X**：docs.x.com 多个页面（user-access-token / authorization-code 正文）本轮经搜索摘要与 `.md` 线索获得，直接 `web_fetch` 报 `fetch failed`、`read_page` 触发 Firecrawl 429，**未逐字读正文**；「January 31, 2027 OAuth 1.0a 停用」的具体措辞来自官方迁移页搜索摘要，待正文复核；`code_challenge_method` 是否强制 S256 待正文复核（参数表列了 S256 与 plain）；X 是否存在任何 OIDC discovery 端点待确认。
5. **Apple**：«Verifying a user» 写「JWS **E256** signature」，而 OIDC discovery 与 `id_token` 页为 **RS256** —— 两处官方表述不一致，需实测 `/auth/keys` 返回的 `alg`/`kty` 确认；Apple 单页「最后更新」日期未找到；Apple 是否对移动端要求 PKCE 未找到明确说明。
6. **Go 库**：`x/oauth2/jwt`、`/jws` 等子包对 Apple ES256 `client_secret` 的实际可用性未核实；`endpoints.Apple` 是否覆盖 revoke/keys（源码只含 Auth/Token，已确认，但配套用法未跑通）。

> 说明：本轮**任何**「二手」资料（CSDN / StackOverflow / devcommunity / 博客 / Reddit）都只作为定位官方文档的线索，未作为上表结论的依据；Google 2022 安全博客、Google WebView 整改帮助页在矩阵中已标「二手索引」。

---

## 全部引用链接列表（一手）

### RFC / 标准
- [RFC 6749 — OAuth 2.0](https://datatracker.ietf.org/doc/html/rfc6749)
- [RFC 6749 §10.12 — CSRF / state](https://datatracker.ietf.org/doc/html/rfc6749#section-10.12)
- [RFC 7636 — PKCE](https://datatracker.ietf.org/doc/html/rfc7636)
- [RFC 8252 — OAuth 2.0 for Native Apps](https://datatracker.ietf.org/doc/html/rfc8252)
- [RFC 9068 — JWT Profile for OAuth 2.0 Access Tokens](https://datatracker.ietf.org/doc/html/rfc9068)
- [OIDC Core 1.0](https://openid.net/specs/openid-connect-core-1_0.html)

### QQ 互联
- [OAuth2.0简介](https://wiki.connect.qq.com/oauth2-0%E7%AE%80%E4%BB%8B/)
- [使用Authorization_Code获取Access_Token](https://wiki.connect.qq.com/%e4%bd%bf%e7%94%a8authorization_code%e8%8e%b7%e5%8f%96access_token/)
- [使用Implicit_Grant方式获取Access_Token](https://wiki.connect.qq.com/%e4%bd%bf%e7%94%a8implicit_grant%e6%96%b9%e5%bc%8f%e8%8e%b7%e5%8f%96access_token/)
- [获取用户OpenID_OAuth2.0](https://wiki.connect.qq.com/%e8%8e%b7%e5%8f%96%e7%94%a8%e6%88%b7openid_oauth2-0/)
- [概念和术语（含 UnionID）](https://wiki.connect.qq.com/%e6%a6%82%e5%bf%b5%e5%92%8c%e6%9c%af%e8%af%ad/)
- [get_user_info](https://wiki.connect.qq.com/get_user_info/)
- [API列表](https://wiki.connect.qq.com/api%e5%88%97%e8%a1%a8/)
- [Universal Links适配FAQ](https://wiki.connect.qq.com/universal-links%e9%80%82%e9%85%8dfaq/)
- [开发接入相关问题](https://wiki.connect.qq.com/%E5%BC%80%E5%8F%91%E8%81%94%E8%B0%83%E7%9B%B8%E5%85%B3%E9%97%AE%E9%A2%98/)

### 微信开放平台
- [移动应用微信登录开发指南](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Development_Guide.html)
- [授权后接口调用（UnionID）](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Authorized_API_call_UnionID.html)
- [iOS 接入指南](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/Access_Guide/iOS.html)
- [Android 接入指南](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/Access_Guide/Android.html)
- [配置 iOS 应用备用 universal link](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/guideline/backup_ul.html)
- [配置 Android 应用备用签名](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/guideline/backup_signature.html)
- [移动应用扫码登录](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Login_via_Scan.html)
- [网站应用微信登录开发指南（作对照用）](https://developers.weixin.qq.com/doc/oplatform/Website_App/WeChat_Login/Wechat_Login.html)

### Google
- [OAuth 2.0 for Desktop Apps](https://developers.google.com/identity/protocols/oauth2/native-app)
- [OIDC discovery](https://accounts.google.com/.well-known/openid-configuration)
- [OAuth 2.0 for Client-side Web Applications（隐式流）](https://developers.google.com/identity/protocols/oauth2/javascript-implicit-flow)
- [Android 授权指南](https://developer.android.com/identity/authorization)
- [iOS 授权指南](https://developers.google.com/identity/sign-in/ios/api-access)
- [Google 安全博客 2022（二手索引）](https://developers.googleblog.com/making-google-oauth-interactions-safer-by-using-more-secure-oauth-flows/)
- [WebView 认证整改帮助（二手索引）](https://support.google.com/faqs/answer/12284343?hl=en)

### X / Twitter
- [OAuth 2.0 概览](https://docs.x.com/fundamentals/authentication/oauth-2-0/overview)
- [OAuth 2.0 Authorization Code Flow with PKCE](https://docs.x.com/fundamentals/authentication/oauth-2-0/authorization-code)
- [authorization-code.md（含 confidential/public client 定义）](https://docs.x.com/fundamentals/authentication/oauth-2-0/authorization-code.md)
- [OAuth 2.0 user access token（token / revoke 端点）](https://docs.x.com/fundamentals/authentication/oauth-2-0/user-access-token)
- [Migrate from OAuth 1.0a to OAuth 2.0](https://docs.x.com/fundamentals/authentication/oauth-2-0/oauth-1-0a-token-exchange)
- [X API changelog](https://docs.x.com/changelog)
- [Getting Access（v2 认证入口）](https://docs.x.com/x-api/getting-started/getting-access)

### Apple
- [Sign in with Apple 首页](https://developer.apple.com/sign-in-with-apple/)
- [Authenticating users with Sign in with Apple](https://developer.apple.com/documentation/signinwithapple/authenticating-users-with-sign-in-with-apple)
- [Verifying a user](https://developer.apple.com/documentation/signinwithapple/verifying-a-user)
- [Receiving a User's Identity Token](https://developer.apple.com/documentation/signinwithapple/receiving-a-users-identity-token)
- [Request an authorization to the Sign in with Apple server](https://developer.apple.com/documentation/signinwithapplerestapi/request-an-authorization-to-the-sign-in-with-apple-server.)
- [Token validation](https://developer.apple.com/documentation/signinwithapplerestapi/generate-and-validate-tokens)
- [Fetch Apple's public key to verify token signatures](https://developer.apple.com/documentation/signinwithapplerestapi/fetch-apple's-public-key-for-verifying-token-signature)
- [Token revocation](https://developer.apple.com/documentation/signinwithapplerestapi/revoke-tokens)
- [Creating a client secret](https://developer.apple.com/documentation/accountorganizationaldatasharing/creating-a-client-secret)
- [id_token claims](https://developer.apple.com/documentation/signinwithapplejs/authorizationi/id_token)
- [Incorporating Sign in with Apple into other platforms](https://developer.apple.com/documentation/signinwithapple/incorporating-sign-in-with-apple-into-other-platforms)
- [Implementing User Authentication with Sign in with Apple（原生 SDK）](https://developer.apple.com/documentation/authenticationservices/implementing-user-authentication-with-sign-in-with-apple)
- [ASAuthorizationAppleIDProvider](https://developer.apple.com/documentation/authenticationservices/asauthorizationappleidprovider)
- [ASWebAuthenticationSession](https://developer.apple.com/documentation/authenticationservices/aswebauthenticationsession)
- [OIDC discovery](https://appleid.apple.com/.well-known/openid-configuration)

### Go 库
- [golang/oauth2 仓库](https://github.com/golang/oauth2)
- [endpoints/endpoints.go 源码](https://raw.githubusercontent.com/golang/oauth2/master/endpoints/endpoints.go)
- [pkg.go.dev/golang.org/x/oauth2/google](https://pkg.go.dev/golang.org/x/oauth2/google)
- [coreos/go-oidc 仓库 README](https://github.com/coreos/go-oidc)
- [pkg.go.dev/golang.org/x/oauth2](https://pkg.go.dev/golang.org/x/oauth2)
