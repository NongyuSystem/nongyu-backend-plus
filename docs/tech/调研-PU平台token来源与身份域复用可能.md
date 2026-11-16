# 调研：PU 平台 x-access-token 的来源 / 刷新 / 失效链路与身份域复用可能

> **性质**：wayfinder map #58 的 research 票「研究：PU 平台 x-access-token 的来源/刷新/失效链路与身份域复用可能」的**第一手事实底稿**，不是实现方案、不产可运行代码、不下「选哪个」的决策（决策归 map #58 的 grilling 票 #69）。
> **分工**：本笔记**专攻 token 凭据链路**，与同 effort 已落盘的 `docs/tech/调研-学校二课一手契约与新仓接入现状.md`（276 行）和 `docs/research/学校二课一手契约与新仓接入现状-结论.md`（85 行）互补，**不重复它们的 172 接口 A~E 分组计数 / 只读·写分类**。那两篇管「全套接口与接入现状」，本笔记管「一个 token 从哪来、怎么带、怎么失效、能不能复用身份域」。
> **方法**：只读本次 @ 指向的**离线 APK 逆向产物**（`work/iciannong-apk/`）与本仓一手文档 / 源码，逐条回指来源；**未重新反编译、未做动态抓包、未连真账号、未假装做 Frida**。
> **路径写法**：相对当前工作区 `apps/nongyu-backend-plus/`；APK 产物一律 `work/iciannong-apk/...`，行号均为对应文件的真实行号。
> **命名与存放惯例**：照 `docs/tech/调研-六类常见功能的旧农屿一手契约与新仓现状.md`（底稿，逐条 `[来源: 路径:行号]`）+ `docs/research/六类常见功能-旧农屿一手契约与新仓现状-结论.md`（结论索引）的样板；本票只产这一篇底稿。

## 证据等级铁律（贯穿全文）

REPORT.md §7 明写「**未做动态抓包（无设备/无 Frida），所有结论均为静态证据；`code=0/5` 来自转换器 IL，未在真实响应上验证**」「无 jadx，未做控制流级反编译」「外网直连不可用」；`scope.md` 记 `network_profile.mode=offline`、`auth.status=granted`、`preset:offline-sample`，样本 build 时间 `2024-03-14 16:36:06`、版本 `12.10.0.10010599`。[来源: work/iciannong-apk/REPORT.md:3-20,258-262; work/iciannong-apk/scope.md:13-18,31-35]

| 标记 | 含义 |
| --- | --- |
| **【DEX 静态】** | 来自 DEX 注解 / IL / 字符串池 / VO 字段 / Room 表 DDL 字面量，产物内可读；**未经真实响应验证** |
| **【仓内一手】** | 来自本仓 ADR / Spec / README / GLOSSARY / Go 源码，行号可核对 |
| **【静态判读】** | 由方法名 / 参数 / 返回壳 / 字符串上下文推断的语义，非运行时事实 |
| **【仓内未找到／待 HITL】** | 产物与本仓均无，须真账号 / 真网络 / 维护者拍板 |

> **绝不编造真实网络行为或真账号验证结果**；查不到一律写「仓内未找到／APK 产物里未见」。
> 第一性前提：本仓 `README.md:3` 声明「独立于旧农屿 monorepo 的全新仓库，不复用旧后端代码、表结构与部署链路」；GLOSSARY「功能重写」词条 `_Avoid_` 排除「迁移 / 移植 / 复用 / 复刻」。[来源: README.md:3; GLOSSARY.md:109-118]

---

## 一、`x-access-token` 的形态与注入方式

### 1.1 形态：header 字段，值来自 PU 本地会话对象

- **字段名 `x-access-token`，由 Retrofit `@Header("x-access-token") String token` 注入**，是 `annotations2.py` 直接解出的 DEX 注解，非推测。【DEX 静态】[来源: work/iciannong-apk/REPORT.md:43; work/iciannong-apk/dump/ALL_API.md:5]
- **取值链**（5 个二课 presenter 完全一致，IL 逐指令可核）：`Session.getLoginInfo(mContext) → LoginInfoVO.getToken() → Api.getXxx(token, …) → Observable.compose(RxSchedulers.handleResult())`。【DEX 静态】[来源: work/iciannong-apk/REPORT.md:45-50,85-97; work/iciannong-apk/dump/sc_final/tiangong.com.pu.module.secondclass.presenter.CreditRulePresenter.txt:11,13; work/iciannong-apk/dump/sc_final/tiangong.com.pu.module.secondclass.presenter.ApplyPeriodPresenter.txt:11,13]
  - 类名是 `tiangong/com/pu/module/account/login/LoginInfoVO`（**不是** `data/vo/LoginUserVO`）。[来源: work/iciannong-apk/dump/sc_final/tiangong.com.pu.module.secondclass.presenter.CreditRulePresenter.txt:11]
- **token 是普通 `String`**，注解 / IL 里没有任何 JWT / Bearer / 签名 / 加密形态的证据；是否自包含、长度、字符集**APK 产物里未见**。【DEX 静态 / 仓内未找到】[来源: work/iciannong-apk/dump/ALL_API.md:48,55,63; work/iciannong-apk/dump/sc_final/tiangong.com.pu.module.secondclass.presenter.CreditRulePresenter.txt:13]
- 全 172 接口**总口径**是「统一 `@Header("x-access-token")`」，全为 `POST`，`FormUrlEncoded` 用 `@Field` 传参——但**逐条有例外**，见 §1.3。【DEX 静态】[来源: work/iciannong-apk/dump/ALL_API.md:5,7; work/iciannong-apk/REPORT.md:120]

### 1.2 两套 Retrofit 与 `Api$1` 的 7 个设备头（IL 实证，非报告转述）

- **BaseURL 两处 IL 实证**：`getGsonApi(String)` 用 `StringBuilder("https://jk.sicau.edu.cn:443/").append(prefix)`；`getRxApi()` 用 `Retrofit.Builder.baseUrl("https://jk.sicau.edu.cn:443/")`。【DEX 静态】[来源: work/iciannong-apk/dump/net_final/tiangong.com.pu.common.network.Api.txt:936-940,1425-1426; work/iciannong-apk/REPORT.md:29-35]
- **`Api$1`（`implements okhttp3.Interceptor`）追加 7 个设备头**：`pu-version`（版本名）、`client-dev-brand`（运营商）、`client-dev-type`（机型）、`client-os-type="android"`、`client-os-version`（系统版本）、`client-long="0"`、`client-lat="0"`。【DEX 静态】[来源: work/iciannong-apk/dump/net_final/tiangong.com.pu.common.network.Api$1.txt:24-45; work/iciannong-apk/REPORT.md:52]
- **只挂 `getGsonApi`、Rx 侧不带**——这是 `Api.java` IL 直接实证的差异，非报告推测：
  - `getGsonApi(String)`：先 `addInterceptor(LoggingInterceptor)`，再 `addInterceptor(new Api$1())`（`L0044` / `L0047`）。[来源: work/iciannong-apk/dump/net_final/tiangong.com.pu.common.network.Api.txt:964-967]
  - `getRxApi()`：只有 `addInterceptor(LoggingInterceptor)`（`L0033`），随后直接 `build()`，无 `Api$1`。[来源: work/iciannong-apk/dump/net_final/tiangong.com.pu.common.network.Api.txt:1412,1421]
  - 故 **Rx 侧二课接口只带 `x-access-token`，不带 7 个设备头**。【DEX 静态】[来源: work/iciannong-apk/REPORT.md:53; work/iciannong-apk/dump/net_final/tiangong.com.pu.common.network.Api.txt:964-967,1412]
- 两套 OkHttp 都配 `SSLSocketClient`（信任所有证书）+ `LoggingInterceptor(BASIC)`；BASIC 级别与真实抓包行为的关联**待 HITL**。【DEX 静态 / 待 HITL】[来源: work/iciannong-apk/dump/net_final/tiangong.com.pu.common.network.Api.txt:976-980,1413-1419; work/iciannong-apk/REPORT.md:39,261-262]

### 1.3 逐条核对「哪些接口带 token、哪些不带」

> 依据 `ALL_API.md` 各分组表的「请求参数」列逐行核对；`Header(x-access-token)` 出现即算「带」，只列 `@Field`/`@Part` 或无参即算「不带」。这是**注解静态判读**，不代表运行时 header 一定为空（OkHttp 拦截器可能补），运行时行为待 HITL。

- **A 组（二课学分 / 成绩单 13 个）**：逐行均 `0:Header(x-access-token)`，**全带**。【DEX 静态】[来源: work/iciannong-apk/dump/ALL_API.md:27-39]
- **C 组（部落 / 社群 37 个）**：逐行均带（Header 位置有 `0` 也有 `2`/`3`/`4`，但都在参数列内）。【DEX 静态】[来源: work/iciannong-apk/dump/ALL_API.md:581-617]
- **E 组（消息 / 通知 / 私信 19 个）**：逐行均 `0:Header(x-access-token)`，**全带**。【DEX 静态】[来源: work/iciannong-apk/dump/ALL_API.md:1420-1438]
- **B 组（活动 48 个）例外 3 个（不带 token）**：
  - `actInfo/v1.0.0/getGpsR` → `getGpsR()` 无参。[来源: work/iciannong-apk/dump/ALL_API.md:173,373-377]
  - `scanCode/v1.0.0/getImportMemberResult` → `Field(uuid)`。[来源: work/iciannong-apk/dump/ALL_API.md:176,396-401]
  - `scanCode/v1.0.0/getUploadResult` → `Field(uuid)`。[来源: work/iciannong-apk/dump/ALL_API.md:185,465-470]
  - 注意 `getActEvalList`（`2:Header`）与 `getMemberList`（`2:Header`）**虽 Header 在后但仍带**，不算例外。[来源: work/iciannong-apk/dump/ALL_API.md:157,178]
- **D 组（用户 / 账号 / 设置 55 个）例外 15 个（不带 token）**——全部是登录 / 注册初始化 / 重置 / 验证码 / 基础字典类：

| # | 路径 | 方法 | 参数 | 返回 |
| --- | --- | --- | --- | --- |
| D11 | `login/v1.0.0/getCityList` | `getCityList` | `Field(id)` | `CityListRespVO` |
| D13 | `/user/feedback/v1.0.0/getCommonProblemList` | `getCommomProblemList` | 无参 | `CommonProblemVO` |
| D14 | `login/v1.0.0/getCountryList` | `getCountryList` | 无参 | `CountryListRespVO` |
| D24 | `/user/homePage/v1.0.0/getOpenScreenAd` | `getOpenScreenAd` | `Field(version)`, `Field(clientType)` | `HomeBannerVO` |
| D28 | `reset/v1.0.0/getPhoneValidCode` | `getPhoneValidCode` | `Field(uuid)`, `Field(mobileParam)` | `GetPhoneValidCodeRespVO` |
| D32 | `login/v1.0.0/getProvinceList` | `getProvinceList` | `Field(id)` | `ProvinceListRespVO` |
| D34 | `reset/v1.0.0/getResetMobile` | `getResetMobile` | `Field(sid)`, `Field(loginName)` | `GetResetMobileRespVO` |
| D35 | `login/v1.0.0/getSchoolList` | `getSchoolList` | 无参 | `GetSchoolListRespVO` |
| D39 | `login/v1.0.0/mobileLogin` | `mobileLogin` | `Field(mobile)`, `Field(password)` | `LoginRespVO` |
| D40 | `reset/v1.0.0/resetUserPasswd` | `resetUserPassword` | `Field(uuid)`, `Field(newPwd)` | `BaseRespVO` |
| D41 | `login/v1.0.0/saveInitUser` | `saveInitUserPath`（表单版） | 9 个 `Field` | `BaseRespVO` |
| D45 | `login/v1.0.0/snoLogin` | `snoLogin` | `Field(sid)`, `Field(loginName)`, `Field(password)` | `LoginRespVO` |
| D46 | `/user/thirdParty/v1.0.0/saveThirdPartyUserInfo` | `thirdPartLogin` | 5 个 `Field` | `BaseResponse<LoginInfoVO>` |
| D54 | `reset/v1.0.0/validPhoneCode` | `validPhoneCode` | `Field(mobileParam)`, `Field(validCode)` | `BaseRespVO` |
| D55 | `login/v1.0.0/webLoginFromApp` | `webLoginFromApp` | `Field(uuid)` | `BaseRespVO` |

【DEX 静态】[来源: work/iciannong-apk/dump/ALL_API.md:932-935,944-946,949-951,953-956,960-963,966-967,975-976,1061-1066,1076-1080,1083-1088,1161-1166,1193-1198,1224-1229,1239-1251,1276-1282,1284-1290,1292-1298,1323-1329,1331-1337,1394-1399,1402-1408]

- **D 组里「登录/凭据类但带 token」的 3 个**（说明「登录类一律不带」也不成立）：
  - D29 `/user/setting/v1.0.0/getVerificationCode` → `getPhoneValidCodeByToken`，`0:Header(x-access-token); 1:Field(phoneNumber)`。[来源: work/iciannong-apk/dump/ALL_API.md:950,1201-1206]
  - D30 `login/v1.0.0/getPhoneValidCodeForInit` → `getPhoneValidCodeForInti`，`0:Field(mobile); 1:Header(x-access-token)`。[来源: work/iciannong-apk/dump/ALL_API.md:951,1209-1214]
  - D42 `login/v1.0.0/saveInitUser` → `saveInitUserPath`（multipart 版），`0:PartMap(); 1:Part(); 2:Header(x-access-token)`，返回 `LoginRespVO`。[来源: work/iciannong-apk/dump/ALL_API.md:963,1300-1305]
- **计数（本笔记口径，非 A~E 分组计数）**：D 组 55 = 不带 token 15 + 带 token 40；全 172 = 不带 token 18（B 组 3 + D 组 15）+ 带 token 154。【DEX 静态 / 静态判读】[来源: work/iciannong-apk/dump/ALL_API.md:12-18,917-976]

---

## 二、token 从哪来：D 组 login / 凭据方法逐条核查（核心）

### 2.1 D 组 12 个 login / 凭据相关方法（路径 / 参数 / 返回 / 是否带 token）

> 口径：D 组 55 个里与「取得或重置登录凭据」直接相关的 12 个。顺序按 `ALL_API.md` 表内 # 号，便于回查；与同 effort 底稿 §2.1 的 12 条一致，但**本表补上「返回 VO 的精确签名行号」与「相对路径拼接」说明**。

| # | 方法（ApiInterface） | 路径 | `@Field` 参数 | 返回 VO（签名块） | 带 token |
| --- | --- | --- | --- | --- | --- |
| D28 | `getPhoneValidCode` | `reset/v1.0.0/getPhoneValidCode` | `uuid`, `mobileParam` | `Call<GetPhoneValidCodeRespVO>` | 否 |
| D29 | `getPhoneValidCodeByToken` | `/user/setting/v1.0.0/getVerificationCode` | `phoneNumber` | `Observable<BaseResponse>` | **是** |
| D30 | `getPhoneValidCodeForInti` | `login/v1.0.0/getPhoneValidCodeForInit` | `mobile` | `Call<BaseRespVO>` | **是** |
| D34 | `getResetMobile` | `reset/v1.0.0/getResetMobile` | `sid`, `loginName` | `Call<GetResetMobileRespVO>` | 否 |
| D39 | `mobileLogin` | `login/v1.0.0/mobileLogin` | `mobile`, `password` | `Call<LoginRespVO>` | 否 |
| D40 | `resetUserPassword` | `reset/v1.0.0/resetUserPasswd` | `uuid`, `newPwd` | `Call<BaseRespVO>` | 否 |
| D41 | `saveInitUserPath`（表单） | `login/v1.0.0/saveInitUser` | `uid`, `password`, `verifyPassword`, `email`, `mobile`, `valideCode`, `countryId`, `provinceId`, `cityId`（9） | `Call<BaseRespVO>` | 否 |
| D42 | `saveInitUserPath`（multipart） | `login/v1.0.0/saveInitUser` | `PartMap()`, `Part()` | `Call<LoginRespVO>` | **是** |
| D45 | `snoLogin` | `login/v1.0.0/snoLogin` | `sid`, `loginName`, `password` | `Call<LoginRespVO>` | 否 |
| D46 | `thirdPartLogin` | `/user/thirdParty/v1.0.0/saveThirdPartyUserInfo` | `face`, `name`, `sex`, `unionId`, `authtype` | `Observable<BaseResponse<LoginInfoVO>>` | 否 |
| D54 | `validPhoneCode` | `reset/v1.0.0/validPhoneCode` | `mobileParam`, `validCode` | `Call<BaseRespVO>` | 否 |
| D55 | `webLoginFromApp` | `login/v1.0.0/webLoginFromApp` | `uuid` | `Call<BaseRespVO>` | 否 |

【DEX 静态】[来源: work/iciannong-apk/dump/ALL_API.md:949-951,955-957,960-963,966-967,975-976（表）; :1193-1214,1239-1251,1276-1305,1323-1337,1394-1408（签名块）; work/iciannong-apk/dump/api_table2_utf8.txt:741,830,870,876（LoginRespVO / LoginInfoVO 返回签名）]

- **配套账号绑定写操作**（与凭据相关但属「登录后绑定」，不在上表 12 个内）：
  - `bindPU` → `/user/thirdParty/v1.0.0/bindPu`，`Header(x-access-token)` + `sid`, `loginName`, `password`，返回 `Observable<BaseResponse<LoginInfoVO>>`（**带 token**）。[来源: work/iciannong-apk/dump/ALL_API.md:925,1005-1011]
  - `bindThirdPart`(`unionId`,`authtype`)、`unbindThirdPart`(`authtype`)、`bindPhone`(`currentPassword`,`newPhoneNumber`,`verificationCode`)、`updatePassword`(`oldPassword`,`newPassword`,`confirmPassword`) 均带 token。[来源: work/iciannong-apk/dump/ALL_API.md:926-927,968,972]
  - 基础字典（无 token）：`getSchoolList()`、`getCityList(id)`、`getProvinceList(id)`、`getCountryList()`。[来源: work/iciannong-apk/dump/ALL_API.md:932,935,953,956]

### 2.2 是否存在「用川农统一身份认证换 PU token」的桥接接口

**结论：APK 产物里未发现任何桥接接口；可见的是 PU 自带独立账密 / 第三方 / 手机验证码体系。**【DEX 静态 + 静态判读】

事实依据（逐条）：

1. **登录端点全部 POST 到 `jk.sicau.edu.cn:443`**（两套 Retrofit 的 baseUrl 都是它，§1.2 已实证），**不带任何 CAS / WebVPN 票据形态的参数**：`snoLogin(sid, loginName, password)`、`mobileLogin(mobile, password)`、`thirdPartLogin(unionId, authtype)`、`saveInitUser(uid, password, …)`、`webLoginFromApp(uuid)`、`reset*(uuid, …)`。参数族是「账号 + 密码 / 手机号 + 验证码 / 第三方 unionId / 一次性 uuid」，**没有** `ticket` / `ST` / `CASTGC` / `wengine_vpn_ticket*` / `service` / `appid` 任何一个。【DEX 静态】[来源: work/iciannong-apk/dump/ALL_API.md:949-951,960-963,966-967,975-976; work/iciannong-apk/dump/net_final/tiangong.com.pu.common.network.Api.txt:936,1425; work/iciannong-apk/REPORT.md:29]
2. **`sid` 是「学校 id」不是票据**：它出现在 `snoLogin` / `getResetMobile` / `bindPu` / `getSearchGroup` / `getGpsR` 等多处，是 PU 用来区分学校/租户的业务字段；`LoginUserVO` 与 `login_info` 表都有 `sid` 列。【静态判读 + DEX 静态】[来源: work/iciannong-apk/dump/ALL_API.md:966,955,925,771; work/iciannong-apk/dump/vo/tiangong.com.pu.data.vo.LoginUserVO.txt:35,172; work/iciannong-apk/dump/str/all_strings.txt:74197]
3. **字符串池里没有川农 CAS / WebVPN 形态**：全量 `all_strings.txt` grep `auth.sicau` / `cas.sicau` / `webvpn` / `wengine_vpn_ticket` / `CASTGC` / `jiaowu.sicau` 的命中，只有 `https://jk.sicau.edu.cn:443/` 与 `https://h5.sicau.edu.cn` 这两类二课自己的 host；`SSO*` / `SSOAuthorizeActivity` / `SSOListener` / `disableSSO` 等均来自 Mob/ShareSDK 第三方登录库（`cn/sharesdk/...`、`com/sina/sso/...`、`com/mob/...`），**不是川农 CAS**。【DEX 静态 + 静态判读】[来源: work/iciannong-apk/dump/str/all_strings.txt:109606-109612,134411-134424,17782-17783,18054,18074,25643-25646,35570-35572,37157-37158]
4. **`webLoginFromApp(uuid)` 是 PU 自己的换态端点，不是 CAS 端点**：`uuid` 与 `getPhoneValidCode(uuid, mobileParam)` / `resetUserPasswd(uuid, newPwd)` / `getUploadResult(uuid)` 同族，是一次性校验标识；返回 `BaseRespVO`，不是票据。【静态判读】[来源: work/iciannong-apk/dump/ALL_API.md:976,949,961,185,1402-1408]
5. **`LoginRespVO` / `LoginInfoVO` 的字段清单产物里未见 VO 类字段声明**（`dump/vo/` 151 个文件只收 `data.vo.*`）；但 `login_info` 表 DDL 可还原持久化字段集，见 §3.1。**token 精确字段名 / 有效期 / 是否由登录响应下发**：登录响应类 `LoginRespVO` 无对应表 DDL、无字段 dump，**仓内未找到／APK 产物里未见**，不得据 `LoginUserVO` 或表名臆断。【DEX 静态 / 仓内未找到】[来源: work/iciannong-apk/dump/ALL_API.md:1279,1303,1326,1334; work/iciannong-apk/dump/vo/tiangong.com.pu.data.vo.LoginUserVO.txt:36,176; work/iciannong-apk/REPORT.md:249-256]

> **诚实边界**：「PU 服务端底层是否与川农统一身份认证打通、`snoLogin` 的 `sid`+账密是否被 PU 后台代理到 CAS、能否用 CAS 票换 `x-access-token`」——**仓内未找到／APK 产物里未见**，须真账号 / 真网络 HITL，不得推测。[来源: work/iciannong-apk/REPORT.md:258-262; work/iciannong-apk/scope.md:31-35]

---

## 三、失效与刷新：`LoginInfoVO` 身份字段 / Session 持久化 / refresh 核查

### 3.1 `LoginInfoVO` 除 token 外还有哪些身份字段（本笔记新挖到的一手证据）

> 同 effort 底稿 §2.2 写「`dump/vo/` 未收 `LoginInfoVO` 字段清单 → APK 产物里未见」。本轮在**字符串池的 Room 表 DDL** 里找到了可还原 `LoginInfoVO` 持久化字段集的一手证据，在此补上；这是本笔记相对那两篇的增量，不改动它们。

- **`login_info` 表 ↔ `LoginInfoVO` 实体的对应关系由 Room 迁移报错字符串直接点明**：`Migration didn't properly handle login_info(tiangong.com.pu.module.account.login.LoginInfoVO).`（`all_strings.txt:88698`）；配套 `LoginInfoDao.java` / `LoginInfoDao_Impl.java`（`:86530-86531`）、`Ltiangong/com/pu/data/LoginInfoDao;`（`:87635-87638`）、`__insertionAdapterOfLoginInfoVO` / `__deletionAdapterOfLoginInfoVO`（`:96832-96835`）。【DEX 静态】[来源: work/iciannong-apk/dump/str/all_strings.txt:86530-86531,87635-87638,88698,96832-96835]
- **建表 DDL（28 列，`uid` 为 INTEGER 主键）**：`uid`, `addEventFlag`, `cid`, `cityId`, `cityName`, `cname`, `countryId`, `countryName`, `canInit`, `mobile`, `name`, `nativePlace`, `nativePlaceModifyFlag`, `password`, `personalitySignature`, `photo`, `provinceId`, `provinceName`, `realName`, `schoolName`, `sex`, `sexModifyFlag`, `sid`, `token`, `encryptMobile`, `userType`, `hasTag`, `enrollmentYear`。【DEX 静态】[来源: work/iciannong-apk/dump/str/all_strings.txt:74197]
  - 另有迁移语句 `ALTER TABLE login_info ADD COLUMN enrollmentYear NOT NULL DEFAULT "0"`（说明 `enrollmentYear` 是后加列）。[来源: work/iciannong-apk/dump/str/all_strings.txt:72432]
- **除 `token` 外的身份 / 画像字段归类**（静态判读）：
  - 身份标识：`uid`（主键）、`sid`（学校 id）、`userType`、`cid`、`name` / `realName` / `cname`、`schoolName`、`enrollmentYear`（入学年份，`SecondClassFragment` 用它参与展示）。[来源: work/iciannong-apk/dump/str/all_strings.txt:74197; work/iciannong-apk/dump/sc_final/tiangong.com.pu.module.secondclass.view.SecondClassFragment.txt:49,52]
  - 联系 / 凭据：`mobile`、`encryptMobile`、`password`（**列类型是 TEXT，值是否明文存储未验证，不得断言**）、`canInit`、`hasTag`、`addEventFlag`。[来源: work/iciannong-apk/dump/str/all_strings.txt:74197]
  - 画像：`nativePlace` / `provinceId` / `provinceName` / `cityId` / `cityName` / `countryId` / `countryName`、`sex` / `sexModifyFlag`、`personalitySignature`、`photo`。[来源: work/iciannong-apk/dump/str/all_strings.txt:74197]
- **交叉佐证**：`data/vo/LoginUserVO`（330 行 VO 字段 dump，36 个 `// FIELD`）与 `login_info` 表列高度重合，且带 `getToken()` / `setToken()` / `getSid()` / `getEnrollmentYear()`；它**不是登录响应类**，但与 `LoginInfoVO` 同属一套 PU 用户模型。【DEX 静态】[来源: work/iciannong-apk/dump/vo/tiangong.com.pu.data.vo.LoginUserVO.txt:3-38,92,172-178,180-182,320-322]
- **诚实边界**：以上是 **Room 持久化表的列**，由「Room 实体字段 ≈ 表列」**静态判读**得来，不是 `LoginInfoVO` 类字段声明直出；`LoginRespVO`（登录响应）**没有**对应表 DDL 或字段 dump，**仓内未找到／APK 产物里未见**。【DEX 静态 + 静态判读 / 仓内未找到】

### 3.2 Session 的持久化口径（是否落本地、退出登录是否清除）

- **落本地持久化，且是 Room/SQLite**：字符串池里有完整的 CRUD 字面量——
  - `SELECT * FROM login_info` / `SELECT * FROM login_info WHERE uid = ?`；[来源: work/iciannong-apk/dump/str/all_strings.txt:91257-91258]
  - `INSERT OR REPLACE INTO login_info(uid, addEventFlag, …)`（28 列全列插入）；[来源: work/iciannong-apk/dump/str/all_strings.txt:77844]
  - `DELETE FROM login_info` / `DELETE FROM login_info WHERE uid = ?`；[来源: work/iciannong-apk/dump/str/all_strings.txt:75392-75393]
  - `DROP TABLE IF EXISTS login_info` / `CREATE TABLE IF NOT EXISTS room_master_table`（Room 身份哈希）。[来源: work/iciannong-apk/dump/str/all_strings.txt:74198,75544]
  - 读取入口 `Session.getLoginInfo(Context) → LoginInfoVO`（≥7 处 IL 命中）。[来源: work/iciannong-apk/dump/sc_final/tiangong.com.pu.module.secondclass.presenter.CreditRulePresenter.txt:11; work/iciannong-apk/dump/sc_final/tiangong.com.pu.module.secondclass.view.ApplyPeriodFragment.txt:426-430; work/iciannong-apk/dump/sc_final/tiangong.com.pu.module.secondclass.view.EventPeriodFragment.txt:353-357]
  → **结论：token 与整套身份字段由客户端本地 Room 表 `login_info` 持久化，`Session.getLoginInfo` 是唯一读取来源**。【DEX 静态 + 静态判读】[来源: work/iciannong-apk/REPORT.md:47,233]
- **退出登录的可见证据（字符串层）**：`btn_logout` / `btn_logout_setting`（`:99456-99457`）、`logout` / `logoutTv`（`:113200-113201`）、`already_logout`（`:98186`）、`event_logout`（`:103903`）、`isSessionValid`（`:111604`）、`Session.java`（`:92005`）；加上 `DELETE FROM login_info` 两条 DAO 语句。【DEX 静态】[来源: work/iciannong-apk/dump/str/all_strings.txt:92005,98186,99456-99457,103903,111604,113200-113201,75392-75393]
- **诚实边界**：「点退出登录按钮 → 是否真的调用 `DELETE FROM login_info`、是否清掉 `token` 列、是否同时清 H5 的 `?token=`」——**`Session.java` / `LoginActivity` 的点击 IL 不在本次 `net_final/` / `sc_final/` dump 内，未逐指令验证**；字符串只能证明「存在退出登录 UI 与清表 SQL」，不能证明二者连通。【仓内未找到／待 HITL】

### 3.3 失效链路：`code=5 → TokenException → 全局跳登录`

- 转换器 IL 三分支（`ResponseConverterFactory$GsonResponseBodyConverter.convert`）：`body.string()` → `gson.fromJson(json, BaseResponse.class)` → `getCode()`；`code==0` 重新按目标 `type` 解析返回 `data`（`L0012` `if-nez` + `L0014-001c`）；`code==5` 抛 `TokenException(getMessage())`（`L0021-002d`）；其它非 0 抛 `ServerException`（`L002e-0037`）。【DEX 静态】[来源: work/iciannong-apk/dump/net_final/tiangong.com.pu.common.network.ResponseConverterFactory$GsonResponseBodyConverter.txt:21-50; work/iciannong-apk/REPORT.md:59-67]
- `TokenException` 类本身存在（`extends RuntimeException`，构造 `TokenException(String)`）。[来源: work/iciannong-apk/dump/net_final/tiangong.com.pu.common.network.TokenException.txt:1-4]
- 业务规则第 7 条：`code=5` → `TokenException` → 全局跳登录；`Session.getLoginInfo` 是唯一 token 来源。[来源: work/iciannong-apk/REPORT.md:233]
- **真实响应未验证**：`code=0/5` 全部来自转换器 IL，REPORT §7 明写「未在真实响应上验证」；`code=5` 的触发条件（过期 / 被顶号 / 服务端吊销）**APK 产物里未见**。【DEX 静态 / 待 HITL】[来源: work/iciannong-apk/REPORT.md:261]

### 3.4 有没有 refresh / renew / logout 类接口

- **172 接口清单里没有**：`ALL_API.md` / `api_annotations.txt` / `api_table2.txt` 全量 grep `refresh` / `renew` / `logout` / `signOut` / `exitLogin` / `revoke`，**无端点命中**（仅 `LoginInfoVO`/`LoginRespVO` 返回签名里的 `login` 子串）。【DEX 静态】[来源: work/iciannong-apk/dump/ALL_API.md:1-1595; work/iciannong-apk/dump/api_annotations.txt:1-732; work/iciannong-apk/dump/api_table2.txt:1-986]
- **字符串池里的 `refreshToken` 不是 PU token 刷新**：`[refreshToken] romType:`、`[refreshToken] third disabled`、`refreshToken romtype is <= 0` 与 `[registerToken] failed`、`register`、`appkey`、`romType` 同段，属**极光 / 厂商推送 SDK 的注册令牌刷新日志**（REPORT §0 列「极光推送」为主 SDK）。【静态判读】[来源: work/iciannong-apk/dump/str/all_strings.txt:30998-31003,55916; work/iciannong-apk/REPORT.md:18]
- **结论**：**仓内未找到／APK 产物里未见** PU `x-access-token` 的 refresh / renew / logout 网络端点；可见的失效处理只有「`code=5` → 抛异常 → 跳登录（重新走 §2.1 的登录端点）」这一条客户端链路。是否有服务端静默续期、token 有效期多长、是否被顶号——**APK 产物里未见**。【DEX 静态 / 仓内未找到】

---

## 四、与身份域 effort 的复用面（只摆事实与候选，不下决策）

> 决策归 map #58 的 grilling 票 #69。本节只把「事实依据」和「候选形态」摊开。

### 4.1 新仓身份域已落地的一手事实

- `internal/identity/` 已有 `login/`（handler/service/initials）、`session/`（handler/service）、`user/`（handler/service/validate）、`flow/`（handler/service/validate/logoutall）四组代码；GLOSSARY 已正名：**学校身份**（学号唯一标识，农屿不可自行创建）、**用户**（与学校身份一一对应，无感注册产生）、**登录方式**（学校统一身份认证 / 农屿账密 / 农屿邮箱验证码 / 身份证后四位，四种绑定到同一用户）、**无感注册**（只有学校统一身份认证能直接建号）、**会话**（App 同用户同时只有一个有效会话，换设备顶替；管理台独立且可吊销、有明确过期）。【仓内一手】[来源: GLOSSARY.md:35-65; internal/identity/ 目录清单（login/session/user/flow，共 22 个 .go）; docs/tech/领域模型-用户与登录-消息.md:1-10]
- **CAS 换票**词条：「川农 e 认证下以**图形验证码换取目标应用登录票据**的过程；分 WebVPN 门户与教务两路，两路的 `appid` 与 `service` 不可混用」；`_Avoid_` = SSO / 单点登录 / 登录。【仓内一手】[来源: GLOSSARY.md:21-23]
- 逐站实测目录 `SiteProfiles`（10 站：jiaowu / ywtb / webvpn / xsc / zjc / cwc / lib / xsgz / opac / ecology）**不含 `jk.sicau.edu.cn` / `h5.sicau.edu.cn`**；其中 `opac` 被标为「独立账号体系（username/password 表单），页内无 CAS/SSO 入口」，`xsgz` 标「SSO 后拿 `main-app-access-token`」——说明新仓已见过「学校站点里存在非 CAS 的独立 token」这种形态。【仓内一手】[来源: internal/school/cas/exchange.go:68-113]
- 换票产物是 `LoginResult{Ticket string, ExpiresAt time.Time}`（仅 2 字段），票据经 `s.tickets.Put(ctx, userID, sealed, expiresAt)` 按用户键控加密外置落库；`Service.Address` **绝不发起任何出站请求**（只返回经 WebVPN 编码的地址）。【仓内一手】[来源: docs/tech/回传端点契约-现有能力底座.md:170-178; internal/school/jiaowu/service.go:245-249,296,330-343]

### 4.2 「PU token 能否由既有学校统一身份认证派生」的事实依据

| # | 事实 | 指向 | 等级 |
| --- | --- | --- | --- |
| 1 | 二课登录端点的参数族是 `(sid, loginName, password)` / `(mobile, password)` / `(unionId, authtype)` / `(uuid)`，**无** `ticket` / `ST` / `CASTGC` / `wengine_vpn_ticket*` / `service` / `appid` | 与身份域「CAS 换票 = 图形验证码换票据」不是同一输入 | 【DEX 静态】[来源: work/iciannong-apk/dump/ALL_API.md:949-976; GLOSSARY.md:21-23] |
| 2 | 二课 BaseURL 是 `jk.sicau.edu.cn:443`（公网 HTTPS 直连），**不经 WebVPN 编码地址**；`Service.Address` 产出的是 WebVPN 编码地址且零出站 | 两条出站路径不同 | 【DEX 静态 + 仓内一手】[来源: work/iciannong-apk/dump/net_final/tiangong.com.pu.common.network.Api.txt:936,1425; docs/tech/回传端点契约-现有能力底座.md:170] |
| 3 | 身份域 `SiteProfiles` 10 站 + ADR-0001 第 1 条固定白名单（jiaowu / auth / webvpn）都**不含** `jk` / `h5` | 没有现成的目标名映射 | 【仓内一手】[来源: internal/school/cas/exchange.go:72-113; docs/adr/0001-服务端代请求边界由新后端重写.md:25] |
| 4 | GLOSSARY「登录方式」四种均为「向农屿证明学校身份」，与 PU 的 `snoLogin/mobileLogin/thirdPartLogin` 是两套体系；`bindPu(sid, loginName, password)` 带 `x-access-token`，是**登录之后**的绑定写操作 | `sid` 只是关联字段，不构成「CAS 派生」证据 | 【仓内一手 + DEX 静态】[来源: GLOSSARY.md:43-49; work/iciannong-apk/dump/ALL_API.md:925,1005-1011] |
| 5 | ADR-0001 第 3 条管的是 `wengine_vpn_ticket*`（可下发本人客户端直连、按用户键控加密外置、不落日志、退出登录即清除），**不覆盖** PU `x-access-token` | 现成票据保管口径不能直接套用 | 【仓内一手】[来源: docs/adr/0001-服务端代请求边界由新后端重写.md:27-28,40-45] |
| 6 | 「PU 后台是否代理到 CAS、能否用 CAS 票换 `x-access-token`」产物与本仓均无 | 不能下「能/不能派生」的定论 | 【仓内未找到／待 HITL】[来源: work/iciannong-apk/REPORT.md:258-262] |

→ **事实层结论**：APK 产物里**未发现**「用川农统一身份认证换 PU token」的桥接接口，可见的是 PU 自带独立账密体系；但「底层是否打通」缺一手证据。因此是否必须新托管一套「PU 平台凭据」，是 **grilling 票 #69 的决策**，本笔记不下结论。

### 4.3 「后端是否必须持有 token」的事实依据（两种候选形态）

> 只列事实与各自约束，不选边。

- **候选 A：客户端直连 PU（照搬 APK 形态），后端不持有 token**
  - 事实：APK 客户端自己从本地 Room `login_info` 读 token，逐请求注入 `@Header("x-access-token")`（§1.1 / §3.2）；H5 页面由客户端拼 `?token=` 直接打开（§5.1）。【DEX 静态】
  - 约束：ADR-0001 第 3 条承认「票据可下发已认证本人客户端直连」，第 4 条明确「**客户端直连学校的只读请求不经后端，后端无法限流也无法审计**」，且这是「本修订明确接受的盲区，不得声称所有请求均已限流」。【仓内一手】[来源: docs/adr/0001-服务端代请求边界由新后端重写.md:27-28,40-45]
  - 冲突：ADR-0001 第 1 条「调用方永不传入 host 或完整 URL，只传逻辑目标名」；而二课 `@POST` 注解值本身是绝对路径常量、H5 是完整 URL + query token，两套目标表达方式不同。【仓内一手 + DEX 静态】[来源: docs/adr/0001-服务端代请求边界由新后端重写.md:25; work/iciannong-apk/REPORT.md:37]
- **候选 B：后端代请求，后端必须持有每用户的 PU token**
  - 事实：若后端代为调用二课 172 接口，则每个请求都需要 `x-access-token`，而 token 只能由 §2.1 的登录端点（账密 / 手机验证码 / 第三方）取得 → 后端须持有并保管**每用户**的 PU 凭据。【DEX 静态 + 静态判读】[来源: work/iciannong-apk/dump/ALL_API.md:5,949-976; work/iciannong-apk/REPORT.md:43-50]
  - 约束：经后端的交互「**必须**记审计（谁、何时、目标逻辑名、结果码）并按用户分级限流」（ADR-0001 第 4 条）；PU token 作为新凭据，其获取 / 加密外置 / 续期 / 失效 / 脱敏 / 退出清除须新立边界，且同样受 `AuditEntry` 铁律「绝不添加票据、密文、验证码、上游响应体」约束。【仓内一手】[来源: docs/adr/0001-服务端代请求边界由新后端重写.md:27-28; docs/tech/回传端点契约-现有能力底座.md:109-114]
  - 现成接缝不足：`HTTPDoer` 是「后端有没有权发外部请求的唯一闸口」，`Enforce=true` 时白名单外 `ErrTargetDenied` 零出站，`jk` 不在内建清单；`LoginResult` 只有 `ticket`/`expiresAt` 两字段，没有 PU token 位置。【仓内一手】[来源: docs/research/学校二课一手契约与新仓接入现状-结论.md:56,65; docs/tech/回传端点契约-现有能力底座.md:172-178]
- **共同约束（脱敏铁律，见 §5.2）**：无论哪种形态，PU `x-access-token` 作为凭据都不得进日志 / 审计 / 埋点 / 错误信息 / 接口响应。【仓内一手】[来源: docs/adr/0001-服务端代请求边界由新后端重写.md:27; docs/adr/0002-身份仓数据边界.md:21-23,28-45; docs/tech/回传端点契约-现有能力底座.md:112]

---

## 五、H5 query token：逐条清单与脱敏风险

### 5.1 把 token 放 URL 的形态（逐条，DEX 静态）

> 完整 URL 字面量集中在 `all_strings.txt:134411-134424` 与 `:109606-109612`；相对路径字面量在 `:128884-128887`；二课 Fragment 的 IL 常量在 `sc_final/*.txt`。**这些 H5 页面自身的接口不在 172 接口清单产物里**。

**完整 host 形态（7 条，`https://h5.sicau.edu.cn/...?token=`）**：[来源: work/iciannong-apk/dump/str/all_strings.txt:134411-134424,109608]

| # | URL 字面量 | 用途（静态判读） | 别处佐证 |
| --- | --- | --- | --- |
| 1 | `https://h5.sicau.edu.cn/apply_gt/apply/#/credit?token=` | 申请类学分 | `ApplyPeriodFragment.URL_APPLY`，IL 常量 [来源: work/iciannong-apk/dump/sc_final/tiangong.com.pu.module.secondclass.view.ApplyPeriodFragment.txt:22-23; work/iciannong-apk/dump/secondclass_api.txt:120] |
| 2 | `https://h5.sicau.edu.cn/cover/#/cover?token=` | 封面 / 成绩单封面 | REPORT §5 第 5 条 `previewReportCard` 返回 `downloadUrl`/`viewUrl` [来源: work/iciannong-apk/REPORT.md:231] |
| 3 | `https://h5.sicau.edu.cn/cover/#/past?token=` | 往期 | [来源: work/iciannong-apk/dump/str/all_strings.txt:134414] |
| 4 | `https://h5.sicau.edu.cn/cxnl/?token=` | 查询能力页（含义未证） | [来源: work/iciannong-apk/dump/str/all_strings.txt:134415] |
| 5 | `https://h5.sicau.edu.cn/gth5/#/appeal?token=` | 申诉 | `EventPeriodFragment.URL_APPEAL`，IL 常量；`appealCount` 在 `EventPeriodVO` [来源: work/iciannong-apk/dump/sc_final/tiangong.com.pu.module.secondclass.view.EventPeriodFragment.txt:23-24; work/iciannong-apk/dump/secondclass_api.txt:177; work/iciannong-apk/REPORT.md:230] |
| 6 | `https://h5.sicau.edu.cn/gth5/#/notice?token=` | 通知 | [来源: work/iciannong-apk/dump/str/all_strings.txt:134421] |
| 7 | `https://h5.sicau.edu.cn/gth5/#/quality?token=%s&type=` | 质量 / 学分（`%s` 为 token 格式化位） | `EventPeriodFragment.URL_PERIOD`，IL 常量 [来源: work/iciannong-apk/dump/sc_final/tiangong.com.pu.module.secondclass.view.EventPeriodFragment.txt:21-22; work/iciannong-apk/dump/secondclass_api.txt:174] |
| 8 | `https://h5.sicau.edu.cn/gth5/#/activeQR/?token=` | 活动二维码 | [来源: work/iciannong-apk/dump/str/all_strings.txt:109608] |

**相对路径形态（2 条，同一 `cover` 页的另一种拼接字面量）**：[来源: work/iciannong-apk/dump/str/all_strings.txt:128884-128885]
- `/cover/#/cover?token=`（与上面 #2 同页）
- `/cover/#/rank?token=`

**不带 token 的 H5 对照**（说明不是所有 H5 都带）：`/gt/privacy.html`、`/gt/service.html`、`/gt/about_up.html?version=`、`/gth5/#/audit?actID=`、`/gth5/#/setSign?actID=`、`/gth5/#/titbits?actID=`、`/gth5/#/notice?actID=`、`/gth5/#/share?code=activityDetail&desc=`。[来源: work/iciannong-apk/dump/str/all_strings.txt:109607-109611,128886-128887,134416-134424,137090]

- **token 值来源实证**：`ApplyPeriodFragment.initView()` / `EventPeriodFragment.initView()` 的 IL 是 `Session.getLoginInfo(Context) → LoginInfoVO.getToken() → iput` 到 Fragment 的 `token` 字段；`doClick` 里用 `StringBuilder` 把 `URL_APPLY` 常量 + `this.token` 拼成最终 URL 再打开。即 **H5 `?token=` 与 header `x-access-token` 是同一个 token 值**。【DEX 静态】[来源: work/iciannong-apk/dump/sc_final/tiangong.com.pu.module.secondclass.view.ApplyPeriodFragment.txt:398-403,423-430; work/iciannong-apk/dump/sc_final/tiangong.com.pu.module.secondclass.view.EventPeriodFragment.txt:350-357]
- REPORT §1.2 亦点名三例：`apply_gt/apply/#/credit?token=`、`/cover/#/cover?token=`、`/gth5/#/appeal?token=`。[来源: work/iciannong-apk/REPORT.md:55]

### 5.2 脱敏风险（对照 ADR-0001 / ADR-0002 铁律）

- **铁律原文**：ADR-0001 第 3 条「日志与埋点对票据字段**一律脱敏**」「**不下发给第三方、不写入 MMKV/埋点、退出登录即清除**」；ADR-0002「学校密码……**绝不出现在任何日志、审计、埋点、错误信息、接口响应中**」「日志 / 审计 / 埋点 / 错误信息**永不**出现身份证号」；`AuditEntry` 铁律「只允许放标识性字段，**绝不添加票据、密文、验证码、上游响应体**等字段」。脱敏对象全集 = **票据、密码、身份证号、验证码、上游响应体**。【仓内一手】[来源: docs/adr/0001-服务端代请求边界由新后端重写.md:27; docs/adr/0002-身份仓数据边界.md:21-23,28-45,45; docs/tech/回传端点契约-现有能力底座.md:112; docs/tech/持久层结构.md:62]
- **query token 的结构性泄漏面**（静态判读，非实测）：token 出现在 URL query 时，会经过浏览器/WebView 历史、`Referer`、代理与网关访问日志、页面截图、分享 / 复制链接、崩溃上报、埋点 URL 采集等多条不受后端控制的通道；`#` fragment 部分虽不发送到服务端，但 `?token=` 在片段**之前或之内**的字面量会被 WebView / 客户端日志整体记录，且客户端拼接 URL 的代码本身可能把完整 URL 打进日志。【静态判读】[来源: work/iciannong-apk/dump/sc_final/tiangong.com.pu.module.secondclass.view.ApplyPeriodFragment.txt:398-403]
- **APK 自身已有「token 拼进字符串」的形态（诚实观察）**：字符串池有 `TokenResult{token='`、`TokenResult{code=`, `, token=` / `, token='` 等 toString / 日志拼接模板（`:28341-28342,2553-2554`），以及 `TokenException` 的 `getMessage()` 传递（转换器 IL `:42-44`）。**是否真的落日志、BASIC 级 `LoggingInterceptor` 是否打印含 token 的 URL——未在真实运行验证**。【DEX 静态 / 待 HITL】[来源: work/iciannong-apk/dump/str/all_strings.txt:2553-2554,28340-28342; work/iciannong-apk/dump/net_final/tiangong.com.pu.common.network.ResponseConverterFactory$GsonResponseBodyConverter.txt:42-44; work/iciannong-apk/REPORT.md:39,261-262]
- **对新仓的约束（事实层，不是方案）**：若后端代请求 PU 或由后端下发 / 中转这些 H5 URL，则 `?token=` 与 `x-access-token` 都必须落在上述脱敏铁律的禁止集合内——**不得进日志 / 审计 / 埋点 / 错误信息 / 接口响应**；审计只能记「谁、何时、目标逻辑名、结果码」等标识性字段。【仓内一手】[来源: docs/adr/0001-服务端代请求边界由新后端重写.md:27-28; docs/tech/回传端点契约-现有能力底座.md:109-114]

---

## 六、证据等级 / 仓内未找到／待 HITL

> 照 map #43 / #50 样板 `docs/research/六类常见功能-旧农屿一手契约与新仓现状-结论.md:74-83` 的口径列；全部为诚实标注，**绝不编造真实网络行为或真账号验证结果**。

1. **真实网络行为 / 真账号验证整体缺位**：REPORT §7「无动态抓包、无 Frida、`code=0/5` 未在真实响应验证、无 jadx 控制流级反编译、`Presenter$1.onNext` 未逐条展开」；`scope.md` `network_profile.mode=offline`、样本 `2024-03-14` build。→ 所有「接口能否调通、返回什么、`code=5` 何时触发」须真账号 / 真网络 HITL。【DEX 静态 / 待 HITL】[来源: work/iciannong-apk/REPORT.md:3-20,258-262; work/iciannong-apk/scope.md:13-18,31-35]
2. **`LoginRespVO` / `LoginInfoVO` 的 VO 类字段声明**：`dump/vo/` 151 个文件只收 `data.vo.*`，**未收** `module.account.login.*`。本轮补到的 `login_info` 表 DDL 只是 **Room 持久化列**（「列 ≈ 实体字段」属静态判读），不是登录响应类字段直出。→ token 精确字段名、有效期、登录响应里 token 放在哪一层，**仓内未找到／APK 产物里未见**。[来源: work/iciannong-apk/REPORT.md:249-256; work/iciannong-apk/dump/str/all_strings.txt:74197,88698; work/iciannong-apk/dump/ALL_API.md:1279,1303,1326,1334]
3. **能否复用身份域 CAS 换票 / 是否必须新托管「PU 平台凭据」**：APK 产物里**未发现**桥接接口（§2.2 六条依据），但「PU 后台是否与学校统一身份认证打通、`sid`+账密是否被代理到 CAS、能否用 CAS 票换 `x-access-token`」**仓内未找到／APK 产物里未见**。→ 须真账号 / 真网络 HITL；**「选哪个」归 grilling 票 #69**，本笔记不下决策。[来源: work/iciannong-apk/dump/ALL_API.md:925,960,966-967; internal/school/cas/exchange.go:72-113; GLOSSARY.md:21-23,43-49]
4. **后端是否必须持有 token**：只有两种候选形态的事实与约束（§4.3），没有一手证据能定「必须 / 不必」。→ 与 ADR-0001 白名单 / 凭据边界冲突一起拍板（扩展目标集合，还是新增一类非 WebVPN 出站目标 + 新凭据边界）。[来源: docs/adr/0001-服务端代请求边界由新后端重写.md:25-28,40-45; docs/research/学校二课一手契约与新仓接入现状-结论.md:73]
5. **刷新 / 续期 / 登出网络端点**：172 接口清单里**仓内未找到** refresh / renew / logout / revoke 端点；字符串池的 `refreshToken` 属推送 SDK。【DEX 静态 / 静态判读】→ token 有效期、是否静默续期、是否被顶号失效，**APK 产物里未见**。[来源: work/iciannong-apk/dump/ALL_API.md:1-1595; work/iciannong-apk/dump/str/all_strings.txt:30998-30999,55916]
6. **退出登录是否真的清 token**：只有 `btn_logout` / `logout` / `already_logout` / `event_logout` / `isSessionValid` 等字符串与 `DELETE FROM login_info` 两条 DAO 语句；**`Session.java` / `LoginActivity` 的点击 IL 不在本次 dump 内，调用链未逐指令验证**，「H5 `?token=` 是否一并清除」亦未见。[来源: work/iciannong-apk/dump/str/all_strings.txt:75392-75393,92005,98186,99456-99457,103903,111604,113200-113201]
7. **H5 页面自身的接口 / 参数**：10 条 query-token URL 字面量已逐条列清（§5.1），但 H5 页面内部调什么接口、token 在 H5 侧如何校验与失效，**不在 172 清单产物里、仓内未找到**。[来源: work/iciannong-apk/dump/str/all_strings.txt:109606-109612,128884-128887,134411-134424; work/iciannong-apk/REPORT.md:55]
8. **相对路径登录端点的 `getGsonApi(prefix)` 取值**：D 组 `login/…` 与 `reset/…` 是不带前导 `/` 的相对路径，按 ALL_API.md 顶部口径走 `getGsonApi(prefix)` 拼接；但 REPORT §1.1 列的 prefix 集合是 `/act/ /user/ /group/ /msg/ /query/`，**未覆盖 `login/` 与 `reset/`**。→ 具体拼接产物与最终 URL **APK 产物里未见**，须 HITL。[来源: work/iciannong-apk/dump/ALL_API.md:9,932-976; work/iciannong-apk/REPORT.md:35-37; work/iciannong-apk/dump/Api_constants_utf8.txt:290-291,312,342]
9. **`login_info.password` 列是否存明文**：DDL 只有 `password TEXT` 列名，**值形态未验证**；`LoginUserVO` 有 `getPassword()` / `setPassword()`，但「是否把学校账密明文落本地」不得据列名断言。[来源: work/iciannong-apk/dump/str/all_strings.txt:74197; work/iciannong-apk/dump/vo/tiangong.com.pu.data.vo.LoginUserVO.txt:26,136-138,280-282]
10. **H5 query token 的实际泄漏面 / APK 是否真把 token 打进日志**：字符串池有 `TokenResult{token='`、`, token=` 等拼接模板，两套 OkHttp 配 `LoggingInterceptor(BASIC)`；**是否真落日志未验证**。[来源: work/iciannong-apk/dump/str/all_strings.txt:2553-2554,28340-28342; work/iciannong-apk/dump/net_final/tiangong.com.pu.common.network.Api.txt:946-964,1394-1412; work/iciannong-apk/REPORT.md:39]

---

> 本笔记只写了 `docs/tech/调研-PU平台token来源与身份域复用可能.md` 这**一个**新文件；未改任何既有文件（含同 effort 两篇笔记）、未改 Go 代码、未建 / 改 issue、未跑 go 命令、未 commit / push、未碰 git。所有 `[来源: …]` 为本仓与 `work/iciannong-apk/` 内可读的一手材料，行号逐条核对；凡「仓内未找到／APK 产物里未见」均如实标注，未编造真实网络行为或真账号验证结果，未下「选哪个」的决策（归 grilling 票 #69）。
