# 调研汇总：手机端五家 OAuth 登录（跨三份笔记的交叉核对）

> 2026-11-09。本文件是 `/research` 三轮调研的**收口**：把三份笔记的结论对齐、点出互相矛盾处、汇总「必须维护者拍板」的开放问题。**不写代码、不做架构决策、不改任何既有文件**。
>
> 三份一手笔记（各自带可点击引用，细节以它们为准）：
> | 主题 | 文件 | 行数 |
> | --- | --- | --- |
> | 五家官方协议/端点/SDK 事实 | [调研-五家OAuth官方接入事实.md](<调研-五家OAuth官方接入事实.md>)（本仓 `docs/tech/`） | 318 |
> | 后端落点与身份仓衔接 | [调研-OAuth后端落点.md](<调研-OAuth后端落点.md>)（本仓 `docs/tech/`） | 361 |
> | Android 端落点 | `apps/nongyu-app-plus/docs/research/mobile-oauth-landing.md`（手机端仓库 `docs/research/`） | 346 |
>
> 边界：本轮只新增这 4 份 Markdown（含本文件），**未改动任何 `.go` / `go.mod` / `build.gradle` / `AndroidManifest` / 既有文档**。

---

## 一、三份笔记一致的核心结论（可放心采用）

1. **五家不是同一套流程，不能合并成一个「OAuth 客户端」抽象**。三类：
   - **厂商 SDK 流程**：QQ 互联 Android SDK 的 `onComplete` **直接把 `access_token`+`openid` 返回给 App**（无 code、无 PKCE）；微信 SDK **强制装微信客户端**、`SendAuth.Resp` 回 **code**（无 PKCE，仅 `state`），靠固定路径 `<包名>.wxapi.WXEntryActivity` 承接。
   - **标准 OAuth 2.0 + PKCE 浏览器流程**：Google / X；Google 另有一条 **Credential Manager 一键登录**直接返 Google ID token（第三种形态）。
   - **Web/REST 流程**：**Apple 无官方 Android SDK**；官方把 Android 归为「other platform, like Android」，指定走 Sign in with Apple JS / REST。
2. **只有 Google / Apple 是标准 OIDC**（有 discovery / JWKS / RS256 签名 id_token，可用 `coreos/go-oidc` 验签、取 `sub`）；**X 是标准 OAuth 2.0 但非 OIDC**（无 id_token/JWKS，身份靠 `/2/users/me` 数字 id）；**QQ / 微信是私有化 OAuth 2.0 变体**（无 JWKS，服务端「校验」= 用 access_token 再调一次用户信息接口）。
3. **`client_secret` 与第三方 token 一律留在服务端**，App 不持有、不长期存。微信官方明文警告 AppSecret 存客户端会被反编译窃取、并「建议将 AppSecret、用户数据（如 access_token）放在 App 云端服务器」；Apple 的 `client_secret` 是用 `.p8` 私钥签的 **ES256 JWT**（非静态串），天然只能服务端；Google/X 的 native app 是 public client、必须 PKCE；QQ 的 appkey 即 client_secret 需保密。
4. **身份主键差异（决定 `login_methods.identifier`）**：QQ `openid`（应用内唯一）/`unionid`（需申请打通）、微信 `openid`/`unionid`（开放平台账号内跨应用唯一，**必须用 unionid**）、Apple `sub`、Google `sub`、X 数字 user id。`email` / `@handle` / 昵称 / Apple 隐藏邮件（中继代理地址）**都不能当主键**。
5. **「不得独立建号」是硬约束**：后端笔记确认 `GLOSSARY.md`、`领域模型`、架构落点流程图、代码 `CodeNeedSchoolAuth` 四处一致——只有学校统一身份认证能建号；OAuth 回调查不到用户时**不建号、不签发会话**，返回「需先完成学校认证」+ 一次性绑定票据，走 `LoginBySchool` 后再 `Bind`。
6. **后端落点**：扩展 `internal/identity/login/`（不新建顶层子域），复用已存在的 `flow/` 编排层与 `session.Service.Issue`（不绕过单设备顶替 / 可吊销 / PG+Redis 双写）；所有 provider 出站必须经 `platform.HTTPDoer` 唯一闸口（`ADR-0001`）；第三方 token / code / state / nonce / 上游响应体一律不落库、不进画像、不进审计表。
7. **手机端落点**：代码放 `:app` 的 `feature/login`（或 `auth`）分包、暂不拆模块；回调用 Activity 层 `onNewIntent`/`onCreate` 接 Intent（**不是** `rememberLauncherForActivityResult`）；QQ/微信 SDK 会强制引入真实 Activity + 改 Manifest。
8. **Go 库现状**（已逐字核对 [`golang/oauth2/endpoints.go` 源码](https://raw.githubusercontent.com/golang/oauth2/master/endpoints/endpoints.go)）：内置 Google / Apple / QQ / X 四个 Endpoint，**唯独没有微信**（须自定义）；四个常量只含 AuthURL/TokenURL，不含 userinfo/JWKS/revoke。本仓 `go.mod` 目前**无任何 oauth2 / OIDC / JWT 库**。

---

## 二、三份笔记之间需要裁决的分歧与更正

> 这是本文件最有价值的部分。三份笔记各自成立，但**放到一起**会撞出下面几处，必须有人拍板或实测。

### 2.1 【更正·已核实】Apple 在 Android 上的「后端中转」是官方规定形态，不是可选优化

- 后端笔记按一般 OAuth 常识写「App 拿 code 给后端换 token」；Android 笔记查到 [Apple《Incorporating Sign in with Apple into other platforms》](https://developer.apple.com/documentation/signinwithapplejs/incorporating_sign_in_with_apple_into_other_platforms) 原文：**「Platforms that support custom URL schemes—iOS 12.0 and earlier and Android—must handle the data resulting from the authorization flow by storing it on their app server in the logic of their `redirect_uri` endpoint. You must then redirect the custom URL scheme to give control back to the app.」**
- 即：Apple 的 `form_post`（请求了 name/email scope 时必须 form_post）**先落到后端 Web 端点**，再由后端 302 到自定义 scheme 把控制权交还 App。**App 连 code 都不直接接**。
- **更正**：我下发任务时写的「Apple 通常走 `ASWebAuthenticationSession` 的 Web 流程」是错的——`ASWebAuthenticationSession` 属 Apple 平台 AuthenticationServices 框架，**Android 上不存在**[（官方）](https://developer.apple.com/documentation/authenticationservices/aswebauthenticationsession)。以 Android 笔记为准。
- **后果**：后端必须多一个「Apple 回调接收端点」，其鉴权/限流/审计/公开路径白名单都要单独设计；后端笔记 §3.2 的 `OAuthProvider.Exchange` 抽象要能容纳「回调先进后端、App 后拿结果」这条反向链路，不能只假设「App 上行 code」。

### 2.2 【分歧·需拍板】QQ 到底走 SDK 还是自主开发 OAuth2.0

- 官方事实笔记与 Android 笔记都证实 QQ **两条路并存**：SDK 流程（App 直接拿 `access_token`+`openid`）与「自主开发」（Server-side 授权码 / Client-side **Implicit Grant**，后者官方仍提供、方向与 Google/X 禁用隐式相反）。
- 后端笔记默认「App 拿 code 给后端换 token」，这**只对 QQ 自主开发 Server-side 成立**；若走 QQ SDK，App 拿到的是长效 token，链路完全不同（甚至与「App 不持有第三方 token」的安全边界冲突，Android 笔记 §4 已点明）。
- **必须拍板**：QQ 选 SDK 还是自主开发；若选 SDK，后端 `OAuthProvider.Exchange` 抽象、审计口径、以及「App 直拿长效 token」的安全边界都要重定。

### 2.3 【分歧·需核实】X 的 OAuth 1.0a 是否「2027-01-31 退役」

- 官方事实笔记写「官方迁移文档另有『Apps have until **January 31, 2027**』」，并标注「正文受工具限流未逐字复核」；Android 笔记则引 [X 官方社区 2025 答复](https://devcommunity.x.com/t/will-oauth-1-0a-user-context-continue-to-be-supported-for-api-v2/245571/3)「OAuth 1.0a support will be continued... we highly recommend OAuth 2.0」。
- 我复核到 [X 官方《Migrating OAuth 1.0a user tokens to OAuth 2.0》](https://docs.x.com/fundamentals/authentication/oauth-2-0/oauth-1-0a-token-exchange) 原文：「**OAuth 1.0a token remains valid until OAuth 1.0a is retired**」「**because OAuth 1.0a is being retired**」，以及 [X API changelog](https://developer.x.com/en/updates/changelog) 的 token-exchange 条目；但**未在官方正文中确证「2027-01-31」这个具体日期**。
- **结论**：两处口径不冲突（「仍在用但将被退役」），但**日期不可当既成事实**。纯登录/身份场景用 OAuth 2.0 + PKCE 即可，不必引入 1.0a 签名；「2027-01-31」须进待实测清单。

### 2.4 【分歧·我复核后倾向后端笔记过度悲观】Apple `sub` 跨端稳定性

- 后端笔记把「iOS 原生 App 与 Android/Web 走 Apple REST/JS 两条路径下 `sub` **很可能不同**」列为**最可能推翻 identifier 方案**的开放问题（§7.3 第 10 条 / §8 第 2 条）。
- 我复核 [Apple Developer Forums 官方答复（2026-06-16）](https://developer.apple.com/forums/thread/834970)：「**Without using app groups, each App ID ... will be treated as a separate account creation flow. However, the same user ID and private email address will be provided since both App IDs are managed under the same developer team.**」以及 [Apple 官方 id_token 文档](https://developer.apple.com/documentation/signinwithapplejs/authorizationi/id_token) / [Authenticating users](https://developer.apple.com/documentation/signinwithapplerestapi/authenticating_users_with_sign_in_with_apple) 的定义「**Uses the same identifier across all of the apps in the development team** associated with your Apple Developer account」。
- 也有 [Apple 社区反例](https://discussions.apple.com/thread/250741481)（不同 client_id 下 `sub` 不同，2019 年）与 [StackOverflow/WWDC21 口径](https://stackoverflow.com/questions/67778700/multiple-apps-under-same-sign-in-with-apple)（同 team 下 `sub` 由 team+Apple ID 派生、应一致）。
- **修正后的判断**：`sub` 是 **pairwise、按 developer team 范围稳定**，不是「按 client_id 变」；跨 iOS App / Service ID 是否一致**取决于 Apple Developer 控制台的 App ID / Service ID 分组配置**（Primary App ID + grouped），不是天然不同。因此后端笔记的「很可能不同」应降级为「**取决于分组配置，必须实测**」。这仍是一个开放问题，但**不再是最可能推翻方案的头号风险**；头号风险应让位给 §2.1 的 Apple 后端中转链路与 §三 的资质合规。

### 2.5 【口径差异·非冲突】X 由 App 还是后端换 token

- 官方事实笔记：X 的 `Native App` 是 **public client**（无 client_secret，用 PKCE），`Web App` 是 confidential。
- Android 笔记 §2.4：若注册为 Native App(public)，token 交换**理论可由 App 用 `code_verifier` 自己完成**；若由后端代换，后端需是另一个 confidential client 且 `redirect_uri` 在该 App allowlist 里，两种架构取舍需与后端对齐。
- 后端笔记 §4.1：统一假设「App 拿 code 交给后端，后端用 `client_secret` 换票」。
- **不冲突但需统一**：后端笔记的假设对 X 的 public-client 形态不成立（后端没有 secret 可带）。三份合起来看，**「App 只拿 code、后端换 token」不是五家通用链路**——QQ SDK、Google Credential Manager、X public client、Apple form_post 四种都各有偏差。后端 `OAuthProvider` 抽象必须容纳这四种，不能只按标准授权码写。

### 2.6 【范围澄清·已核实】Apple 登录强制规则不约束纯 Android

- 后端笔记把「iOS 端 Apple 登录强制规则对 Android 端的影响」列为待拍板；Android 笔记查 [App Store Review Guidelines 4.8](https://developer.apple.com/app-store/review/guidelines/#sign-in-with-apple) 明确：这是 **App Store 审核规则**，只约束上架 App Store 的 Apple 平台应用，**纯 Android 本仓不因此被强制接 Apple 登录**；若将来发 iOS 版且提供这四家登录，iOS 版必须等价提供。
- 结论：该条从「必须拍板的头号风险」降级为「产品/上架决策」；仍需拍板的是「是否现在就为将来 iOS 版预留」。

---

## 三、必须维护者拍板的开放问题（合并去重后）

> 三份笔记各自的「必须人拍板」清单合并、去掉已被上面更正/澄清降级的条目，按「会推翻方案」的程度排序。

### A. 会改变架构形状的（先定这些，否则后端/端侧落点会返工）

1. **QQ 走 SDK 还是自主开发 OAuth2.0**（§2.2）。SDK 路线下 App 直拿长效 `access_token`，与「App 不持有第三方 token」冲突，后端换票抽象与审计口径要重定。
2. **Apple 后端中转端点的形态**（§2.1）：`form_post` 接收端点由谁托管、放在哪个域名/路径、如何进 `publiclyAccessible` 白名单、如何与 `HTTPDoer` 出站白名单共存、App 如何从后端取回结果（轮询 / 一次性票据 / scheme 回跳带句柄）。这条是三份笔记里**唯一被官方文档规定、且三份都没完整设计**的链路。
3. **`kind` 编码与「一人绑多家」**（后端笔记 §7.3 第 1 条）：建议 `kind=oauth:<provider>` 一 provider 一 kind；但代码 `LoginMethodRepo.Bind` 的 `(user_id, kind)` 唯一约束（`internal/platform/identity.go` 第 139 行）与「多种登录方式绑同一用户」需一起确认。是否允许一人同时绑 5 家、是否要改唯一约束，是数据模型级决定。
4. **X 由 App 还是后端换 token**（§2.5），以及是否引入 oauth2 / JWT-JWKS 库（后端笔记 §7.3 第 5 条）。本仓 `go.mod` 目前无此类依赖，加依赖属治理级决定。

### B. 产品/账号/合规（不阻塞设计，但阻塞真机联调与上线）

5. **各平台开发者账号与资质**：QQ 互联（开发者资质审核、应用审核约 5 工作日）、微信开放平台（移动应用审核 + 「微信登录」审核 + **包名 + 应用签名**登记 + 上架状态；未上架 100 次/天）、Google Cloud（项目 + OAuth 同意屏幕 + Android client 包名+SHA-1 / Web client ID）、X Developer（App + OAuth 2.0 开关 + Callback URL）、Apple Developer（App ID + Service ID + `.p8`/Key ID/Team ID）。**全部是人工步骤**。
6. **identifier 漂移的产品对策**：换 client_id / 换包名 / 换开发者账号 / 未打通 unionid 时，是否允许重新绑定、是否留「旧 identifier → 学号」映射表（留表本身是新的数据边界）。参见 §2.4 对 Apple `sub` 的修正判断。
7. **国内备案与隐私合规**：ICP 备案、隐私政策逐一列第三方 SDK、个保法下「第三方账号关联到学号 + 画像」的告知同意；`App Store Review Guidelines 4.8` 是否要为将来 iOS 版预留 Apple 登录。
8. **Google / X 在国内的可达性**、是否面向海外用户、是否需要服务端代理（出站仍须经 `HTTPDoer` 白名单，`ADR-0001`）。
9. **是否打破手机端「零新依赖 + Compose BOM 2025.12.00 锁定」约定**（Android 笔记 §6）：接任何 OAuth SDK 都要新增依赖，且可能与 BOM 映射的 Compose/lifecycle 版本冲突（AGP 9.2.1 / compileSdk 36 下 `minCompileSdk=37` 类硬阻断需实测）。这是维护者拍板过的治理约定。

### C. 数据边界与安全（后端笔记 §7.3，需与 ADR-0002 一起扩写）

10. `login_methods.secret_ref` 对 OAuth 行放什么（空 / 不可解密句柄）；并把 ADR-0002「凭据/令牌绝不落库、绝不进审计」**明确扩展**到第三方 token（目前文档只写了学校密码与身份证号）。
11. 审计是否记录 `subject`（哪怕 hashed）——`user_id + subject` 会让审计表变成「第三方账号 → 学号」映射表，与「表里只有标识性字段」的立法意图相冲；以及「发起 → 回调」两次 HTTP 请求间 `request_id` 如何串。
12. Apple 隐藏邮件、QQ/微信/Google/X 的昵称头像是否允许写入画像、走哪个字段（现有 `platform.Profile` 只有 `QQ` / `AvatarURL`）、是否算「本人可改的农屿扩展」。
13. `client_secret` / Apple ES256 私钥的密钥管理：是否复用 `Cipher`、是否再开第三把独立密钥（ADR-0002 已有密码镜像 / 身份证两把）。
14. `HTTPDoer` 出站白名单追加五家 host 的具体清单与「逻辑目标名 → URL」映射（`ADR-0001` 要求调用方不传 URL）。

---

## 四、必须实测才能定的清单（合并去重，逐项记录结论后才可进 Spec）

1. 五家逐个走通 `authorize → token → (JWKS 验签 或 userinfo)`，记录稳定主体标识字段名、是否发 id_token、`iss/aud/alg/nonce`、JWKS / userinfo 端点、是否需要 PKCE、`code_challenge_method`。
2. **Apple `sub` 在「iOS 原生 vs Android/Web」及不同 Service ID 分组配置下是否一致**（§2.4 修正后仍需实测，但不再是头号风险）；Service ID Return URL 是否只接受 `https://`；`form_post` → 自定义 scheme 回跳实测。
3. QQ / 微信 `unionid` 在「未绑 / 已绑开放平台账号」两种配置下是否返回；QQ 自主开发是否支持 PKCE；QQ SDK 当前 Maven 坐标与 3.5.19 的 `minCompileSdk`。
4. 微信开放平台登记的「应用签名」是否确为签名 MD5；Android 13 去掉 `WXEntryActivity` intent-filter 后的回跳实测；未上架 100 次/天限制对调试期影响。
5. `LoginMethodRepo` 的 `(user_id, kind)` 唯一约束下「一人绑多家 OAuth」是否被拒。
6. 各 SDK 在 AGP 9.2.1 / compileSdk 36 / minSdk 26 下的兼容性；`androidx.credentials` / `googleid` 稳定版本及与 Compose BOM 2025.12.00 的传递依赖冲突。
7. Android App Links 验证（`assetlinks.json` 托管、`autoVerify`、Android 15+ 动态规则）；X Callback URL 是否接受自定义 scheme；装了 X App 时 Web OAuth 回调是否被中断；Google 新 Android OAuth client 是否仍允许自定义 scheme。
8. Apple «Verifying a user» 写「JWS **E256**」而 discovery / id_token 页为 **RS256** 的官方表述冲突，实测 `/auth/keys` 返回的 `alg`/`kty`。
9. X「2027-01-31 OAuth 1.0a 停用」具体措辞与 [官方迁移页](https://docs.x.com/fundamentals/authentication/oauth-2-0/oauth-1-0a-token-exchange) 正文复核（§2.3）；`users.email` 权限层级。
10. 审计链路：模拟「发起 → 回调」两次请求，确认 `request_id` 是否断链。
11. 资质 / 合规：逐家确认开发者账号可用性、应用审核状态、包名与签名、回调域名备案要求、隐私政策清单。

---

## 五、给维护者的两条「先读这个」提示

1. **本仓文档已落后于代码**：`README.md` 与 `docs/tech/` 两份核心设计文档自称「骨架 / 讨论稿，尚未落代码」，但 `internal/identity/{login,session,user,flow}` 与 `internal/platform/identity.go` **已实现**；`GLOSSARY.md` 里「无感注册」有**两版语义直接冲突**的措辞——第一版「任何登录方式都遵循此语义」若照做会让 OAuth 独立建号，违反全篇最硬约束（应以第二版「只有学校统一身份认证能直接建号」为准）。后端笔记 §2.5 逐条列了 7 处文档/代码偏差，含 `ADR-0002` 第 89–90 行残留文案。**本轮只记录、没改任何既有文件**，何时回填由维护者定。
2. **进入 Spec 前的前置条件**：本仓 `README.md` 规定 SDD（PRD → Spec → 技术方案 → 实施计划 → 编码），手机端仓库骨架期「无网络层 / 无状态管理 / 无路由 / 无依赖注入」。三份笔记都指向同一结论：**OAuth 不能直接开工**，需先立一条 ADR 定落点（Android 笔记建议 ADR-0008），并把「网络层 + 状态管理 + 路由 + 后端接口契约 + 各平台控制台账号」五项前置按序落地。

---

## 六、本轮的外部一手来源（交叉核对用）

- [Apple《Incorporating Sign in with Apple into other platforms》](https://developer.apple.com/documentation/signinwithapplejs/incorporating_sign_in_with_apple_into_other_platforms) —— Android 必须后端 `redirect_uri` 端点 + 自定义 scheme 回跳（§2.1 更正依据）
- [Apple Developer Forums 834970（官方答复，2026-06-16）](https://developer.apple.com/forums/thread/834970) —— 同 team 下 `sub` 与隐藏邮件一致（§2.4 修正依据）
- [Apple id_token 文档](https://developer.apple.com/documentation/signinwithapplejs/authorizationi/id_token) / [Authenticating users](https://developer.apple.com/documentation/signinwithapplerestapi/authenticating_users_with_sign_in_with_apple) —— `sub` 团队内稳定、隐藏邮件不能当主键
- [X《Migrating OAuth 1.0a user tokens to OAuth 2.0》](https://docs.x.com/fundamentals/authentication/oauth-2-0/oauth-1-0a-token-exchange) / [X API changelog](https://developer.x.com/en/updates/changelog) —— 1.0a「being retired」但日期未确证（§2.3）
- [微信移动应用微信登录开发指南](https://developers.weixin.qq.com/doc/oplatform/Mobile_App/WeChat_Login/Development_Guide.html) —— 强制原生 SDK、AppSecret 放云端
- [golang/oauth2 endpoints.go 源码](https://raw.githubusercontent.com/golang/oauth2/master/endpoints/endpoints.go) —— 内置 Google/Apple/QQ/X、无微信
- [App Store Review Guidelines 4.8](https://developer.apple.com/app-store/review/guidelines/#sign-in-with-apple) —— 只约束 App Store 平台（§2.6）

（其余全部一手链接见三份笔记各自的「引用链接列表」节。）
