# 决策：学校二课接入的范围切分与 Out of scope

> **性质**：wayfinder map [#58](https://github.com/NongyuSystem/nongyu-backend-plus/issues/58) 子票 [决议：范围切分与 Out of scope](https://github.com/NongyuSystem/nongyu-backend-plus/issues/59) 的决议产物。
> **不是实现方案、不产可运行代码**。
> **事实底座**：[调研-学校二课一手契约与新仓接入现状.md](<docs/tech/调研-学校二课一手契约与新仓接入现状.md>)（172 接口逐条明细）、[调研-PU平台token来源与身份域复用可能.md](<docs/tech/调研-PU平台token来源与身份域复用可能.md>)、[决策-二课凭据边界与ADR-0001再修订.md](<docs/tech/决策-二课凭据边界与ADR-0001再修订.md>)。
> **拍板**：2026-11-16。

---

## 〇、先钉一个前提（否则整张判据表会算错）

本票开票时（2026-11-14）的 Q6 口径是「A+B 最小闭环」，前提是「**读类由后端承载**」。但 [#69](https://github.com/NongyuSystem/nongyu-backend-plus/issues/69) 已把终点改为「**读写都在客户端，后端只做请求配方下发 + 记录**」。

**这对范围的影响是决定性的**：既然后端不代取、客户端只请求它自己需要的东西，那么「纳入 61 个还是 172 个接口」的含义变了——

- 旧口径下，「纳入范围」意味着**后端要为它建端点/缓存/契约**，接口数直接等于后端的负担；
- 新口径下，「纳入范围」只意味着**后端要为它准备一条请求配方**（目标名 + 地址 + 方法 + 参数槽位），后端不写业务逻辑、不存数据、不代请求。

所以范围判断的性质从「哪些能力后端要实现」变成「**哪些能力后端要为其配置配方**」。判据相应改为三条（见 §二）。

---

## 一、A + B 是本 effort 的核心闭环（13 + 48 = 61）

### A 组：二课学分 / 成绩单（13）

| 接口 | 纳入理由 |
| --- | --- |
| `getSecondActivityCredit`（学期学分） | 「第二课堂成绩」这一产品语义的**本位**：学生打开二课第一眼要看的就是学时/学分 |
| `getApplyCredit`（申请类学分） | 同上，申请/导入类学分是成绩的另一半 |
| `getImportCreditDetail`（导入明细） | 学分的来源明细，闭环必需（否则只看到总数无法核对） |
| `getGoodFaithValue`（诚信值） | 二课特有的行为约束指标：报名爽约会扣分，直接影响学生决策 |
| `getCreditRuleExplain`（规则说明） | 学分的解释成本极高（什么算、怎么算），不给规则则学分数字不可信 |
| `getGradeList` / `getSingleGradeList`（成绩记录明细/按类型） | 学分构成的可下钻明细 |
| `getPerIntegrateDetail`（综测明细） | 综测是二课成绩的第三块 |
| `getRanking`（排名） | 排名是成绩的天然伴随物 |
| `myReportCard` / `reportCardSettingView` / `saveReportCardSetting` / `previewReportCard`（成绩单） | 成绩单是学分的产出物；设置项决定成绩单展示范围 |

**纳入理由（组级）**：这 13 个构成「**我的二课成绩长什么样**」的完整回答链——总分 → 分项 → 明细 → 规则 → 排名 → 可导出成绩单。缺任一环，学生都得回官方 App 补看，本 effort 就失去意义。

### B 组：活动（48）

| 子类 | 大致接口 | 纳入理由 |
| --- | --- | --- |
| 活动列表/详情/成员/状态/基础数据（读，约 35） | `getActDetail`、`getActStatusById`、`getMemberList`、`getActType/Level/Tag/Group`、`getActivitySearch` 等 | 「**有哪些活动、能不能报、报了谁**」是学生用二课的主要动机；没有活动列表，学分模块成了无源之水 |
| 报名/签到签退/评价/花絮（写，约 13） | `signInOrOut`×2、`addActEval`、`updateActMemberStatus`、`tidbit` 增删、`updateActivityCollect` 等 | 「**报名、签到**」是维护者原话里点名的第二件事；且这些是典型「客户端本地态」操作（GPS、扫码时间戳） |

**纳入理由（组级）**：A 组回答「我得了多少」，B 组回答「我能参加什么、怎么参加」。二者是同一产品语义的两面，拆开都不成立。

---

## 二、C / D / E 三组的逐组判据

### 判据（先定规则，再逐组套）

在新口径下，一个接口「纳入本 effort」需**同时满足**：

- **判据 1（语义归属）**：它属于「学校二课」这个第三方平台的产品语义，而不是农屿自建能力的重复。
- **判据 2（A+B 依赖）**：不给它配方，A+B 闭环会断（缺参数、缺枚举、缺上下文）。
- **判据 3（无既有域可复用）**：本仓或已交付 effort 没有等价能力可复用。

三条全中 → 纳入；命中判据 3 不成立 → **复用既有域**（不重做）；判据 1、2 都不成立 → **不纳入**。

---

### C 组：部落 / 社群（37）→ **整体不纳入**

逐条归类：

| 子类 | 接口数 | 判定 | 依据 |
| --- | --- | --- | --- |
| 部落公告（`addBulletin`/`deleteBulletin`/`getBulletinList`） | 3 | 不纳入 | 判据 1 成立但判据 2 **不成立**：活动模型虽有 `gid`（活动挂靠部落），但「看活动」不需要「看部落公告」 |
| 部落成员管理（`joinGroup`/`exitGroup`/`inviteInGroup`/`deleteMember`/`updateAdmin`/成员列表×4） | 9 | 不纳入 | 这是**农屿自建社群能力**的重复（判据 1 不成立）；且 `joinGroup` 这类二课社群加入对 A+B 无依赖 |
| 部落相册（`saveAlbum`/`updateAlbum`/`removeAlbum`/`getAlbumList`/`getImageList`/`saveImage`/`removeImage`） | 7 | 不纳入 | 相册 = 文件存储 + 内容域，与 #43 的 `content/post` + `platform/objectstore` 功能重叠（判据 3 不成立） |
| 部落信息/检索/收藏（`getGroupDetail`/`getGroupInfo`/`getMyGroup`×2/`getSearchGroup`/`getStarGroup`/`getSelectCondition`/`saveGroupInfo`×2/`saveCollect`/`getGroupCollection`/`getOtherPeopleGroupList`/`removeAllCollectGroup`/`getUserByNameAndGid`） | 12 | 不纳入 | 判据 1 不成立——「部落」是 PU 的社群产品，不是「二课成绩」 |
| 部落名片/头衔（`updateMemberCard`/`updateMemberHeader`/`getMemberHeaderRecord`/`getGroupCardList`/`updateValidMember`） | 5 | 不纳入 | 同上 |
| （另 1 个） | 1 | 不纳入 | — |

**结论 C**：**37 个整体 out of scope**。理由一句话：**C 是 PU 平台的社群产品线，与「第二课堂成绩」不是同一产品语义；且它大面积与本仓 `content/`（帖子、相册、文件存储）功能重叠。**

**⚠️ 但留一个接口级例外待判**：活动模型含 `gid` 字段（活动挂靠部落），若 A+B 的**活动详情**返回里必须展示「主办部落名」，则可能需要一个极小的「按 gid 取部落名」能力。**这不在 C 组 37 个里**，属于 B 组活动详情的响应字段问题，归 [#61](https://github.com/NongyuSystem/nongyu-backend-plus/issues/61)/[#62](https://github.com/NongyuSystem/nongyu-backend-plus/issues/62) 处理，本票只记录风险不裁。

---

### D 组：用户 / 账号 / 设置（55）→ **拆三类：复用 / 不纳入 / 前置**

| 子类 | 接口数 | 判定 | 依据 |
| --- | --- | --- | --- |
| **登录/凭据（12）** — `snoLogin`/`mobileLogin`/`thirdPartLogin`/`webLoginFromApp`/`saveInitUser`×2/`getPhoneValidCode`×3/`validPhoneCode`/`getResetMobile`/`resetUserPasswd` | 12 | **前置事实，不是本 effort 的域模型** | 已有 [#68](https://github.com/NongyuSystem/nongyu-backend-plus/issues/68) 与 [#69](https://github.com/NongyuSystem/nongyu-backend-plus/issues/69) 收口：**客户端自己登 PU 拿 token，后端不参与**。故这 12 个**不配配方、不进 Spec 的业务章节** |
| **画像扩展（昵称/头像/性别/签名/籍贯/标签）** — `updateNickName`/`updateAvatar`/`updateSex`/`updateSignature`/`updateNativePlace`/`addTags`/`getTagLibList`/`getMyCenterUserInfo` | 8 | **复用身份域，不重做** | 判据 3 不成立：本仓 `internal/identity/user` 已有画像能力（`GLOSSARY.md`「用户画像」条）。二课的昵称/签名是 **PU 平台侧**的画像，与农屿画像不是同一份数据；**本 effort 不为它建域** |
| **隐私设置** — `enterPrivacySetting`/`updatePrivacySetting`（7 个 flag，含 `practiceHourValue`/`goodFaithValue`） | 2 | **不纳入，但要记一笔** | 这是 **PU 侧的可见性开关**（"不让 TA 看我的诚信值"）。它不影响 A+B 数据获取，只影响别的用户能否看。不做 |
| **第三方绑定** — `bindPu`/`bindThirdParty`/`unbindThirdParty`/`getThirdPartBindState` | 4 | **不纳入** | PU 账号体系内部事务；且 `bindPu` 要传学校账密，与 #69「后端不碰凭据」相冲 |
| **手机绑定/改密** — `bindPhone`/`updatePassword`/`getVerificationCode` | 3 | **不纳入** | 同上，PU 账号安全事务 |
| **关注/粉丝/访客** — `addAttentionMe`/`addMyAttention`/`cancle*`×2/`getAttentionMeList`/`getMyAttentionList`/`getMyVisitorList`/`getVisitorDetail`/`getOtherPeopleAttendActList`/`getOtherPeopleCreateActList` | 10 | **不纳入** | 判据 1 不成立：社交图谱，非二课成绩。**但 `getOtherPeopleAttendActList` 值得注意**——它是「看别人的活动记录」，属社交而非本人成绩 |
| **首页/运营位** — `getHomeBannerList`/`getOpenScreenAd`/`getHomeFloorsInfo`/`getHomePageApps`/`homePageSearch`/`getQrCode`/`getNewVersion`/`saveJPushRegistID` | 8 | **不纳入** | 运营位、版本检查、推送注册：与「二课成绩/活动」无关，且推送通道属后续 Spec |
| **反馈** — `feedbackProblem`/`getCommomProblemList`/`getFeedbackProblemList`/`getFeedbackProblemDetail` | 4 | **不纳入** | 反馈是通用运营能力，非二课特有 |
| **基础字典** — `getSchoolList`/`getCityList`/`getProvinceList`/`getCountryList`/`getCollegeInfoByUser` | 5 | **不纳入（除 `getCollegeInfoByUser`）** | 前四个是 PU 登录流程用的行政区/学校字典，客户端自足；`getCollegeInfoByUser` 是学院信息，**若活动详情需要展示学院名则可能要配方**，归 #61/#62 判 |

**结论 D**：**55 个里，0 个进入本 effort 的业务域**。其中 12 个登录凭据已由 #68/#69 收口为「客户端自理」；8 个画像与身份域重叠（复用不重做）；其余 35 个是 PU 平台的账号安全/社交/运营/反馈，与二课成绩无关。

---

### E 组：消息 / 通知 / 私信（19）→ **整体不纳入，复用 #43 消息域**

| 子类 | 接口数 | 判定 | 依据 |
| --- | --- | --- | --- |
| 系统通知（`getSystemNoticeList`/`getSystemNoticeDetail`/`delSystemNotice`/`cleanSystemNotice`） | 4 | 不纳入 | 与 #43 的 `internal/message`（通知/内部信，per-user 已读）功能重叠（判据 3 不成立） |
| 学校通知（`getSchoolNoticeList`/`getSchoolNoticeDetail`/`delSchoolNotice`） | 3 | 不纳入 | 同上；且这是学校层面的通知，不是二课特有 |
| PU 公告（`getPUNoticeList`/`getPUNoticeDetail`/`delPUNotice`） | 3 | 不纳入 | 判据 1 部分成立（PU 平台公告），但判据 2 不成立：不影响 A+B 闭环；且公告形态与 #43 公告域重叠 |
| 私信（`sendPrivateLetter`/`createPrivateLetterFolder`/`delPrivateLetterFolder`/`getPrivateLetterFolderList`/`getPrivateLetterList`） | 5 | 不纳入 | #43 的「内部信」已是本仓能力；PU 私信是其重复实现（判据 3 不成立） |
| 广播/资讯/未读数（`getSchoolBroadcastList`/`getUnreadCount`/`getRecommend`/`updateBrowseCount`） | 4 | 不纳入 | 广播与资讯推荐是内容/运营；`getUnreadCount` 属消息域；`updateBrowseCount` 是浏览数自增（写操作但不产生二课业务价值） |

**结论 E**：**19 个整体 out of scope**。理由：**E 组是「消息/通知/私信」这一整块能力，本仓已在 #43 用 `internal/message` + `internal/content/announcement` 建过一轮并已实施；为二课再做一遍是重复造域（判据 3 不成立）。**

**一处需要指认的边界**：若将来二课侧的「活动变更通知」（如"你报名的活动改时间了"）要推给用户，**应发到本仓消息域**（#43 的能力），而不是对接 PU 的 `messageSystemNotice`。这条边界必须写进 Spec，防止实施时误接 E 组接口。

---

## 三、范围汇总表

| 组 | 接口数 | 纳入 | 复用既有域 | 不纳入 | 理由摘要 |
| --- | --- | --- | --- | --- | --- |
| **A 二课学分/成绩单** | 13 | **13** | 0 | 0 | 「我得了多少」的完整回答链 |
| **B 活动** | 48 | **48** | 0 | 0 | 「能参加什么、怎么参加」，含维护者点名的报名/签到 |
| **C 部落/社群** | 37 | 0 | 0 | **37** | PU 社群产品线，非二课语义；与本仓 content/ 重叠 |
| **D 用户/账号/设置** | 55 | 0 | **8**（画像） | **47** | 12 个登录凭据已由 #68/#69 收口为客户端自理 |
| **E 消息/通知/私信** | 19 | 0 | **19**（#43 消息域） | 0 | #43 已建同类能力，不重复造域 |
| **合计** | **172** | **61** | 27 | 84 | — |

**一句话范围**：**本 effort = A+B 共 61 个接口的「请求配方 + 记录」契约**；C 不纳入、D 拆为「客户端自理 / 画像复用身份域 / 其余不做」、E 全部走 #43 既有消息域。

---

## 四、Out of scope 清单（逐条）

沿用 [#47](https://github.com/NongyuSystem/nongyu-backend-plus/issues/47) 的逐条摘录做法：

1. **C 组 37 个部落/社群接口**（判据 1、3 均不成立）。
2. **E 组 19 个消息/通知/私信接口**（#43 已建 `internal/message` + `content/announcement`，判据 3 不成立）。
3. **D 组 47 个**：登录凭据 12（客户端自理）、隐私设置 2、第三方绑定 4、手机绑定改密 3、社交图谱 10、首页运营位 8、反馈 4、基础字典 4。
4. **D 组 8 个画像接口**：**复用身份域，不为二课建画像域**（`GLOSSARY.md`「用户画像」已定义）。
5. **不重开四张已关 map 的 effort**（#1 WebVPN / #19 实施 / #30 教务查询 / #43 六类常见功能）。查阅其文档以便对齐不构成重开。
6. **不改现行 `docs/specs/prd-学生校外访问学校服务.md` 的范围段**（#47 口径：另立新 PRD）。
7. **不把「登录 / PU token 来源」作为本 effort 的域模型**——它是前置事实，已由 #68/#69 收口为「客户端自理」。
8. **不为 C/D/E 建任何端点、缓存或数据表。**
9. **不做 H5 页面的接口对接**（10 条 `?token=` 页面，其真实接口不在 172 清单产物内；若要用只能靠打开 URL，不属本 effort 的配方范围）。
10. **不做成绩单 PDF 的生成与存储**（`previewReportCard` 返回 `downloadUrl`/`viewUrl`，指向 PU 侧资源，本仓不代存）。
11. **不做活动附件的 multipart 上传中转**（`saveActInfo`/`updateActivity` 的 `@PartMap`+`@Part`）——客户端直传 PU，本仓不碰文件；与 #53 MinIO 无关。
12. **不做二课数据的服务端缓存**（Q1 后端零出站，无可缓存的代取结果）。
13. **不做客户端回执上报以补审计**（Q5 明确不做）。
14. **不做 PU 推送通道对接**（`saveJPushRegistID` 不纳入；推送通道属后续 Spec）。

---

## 五、横切依赖（本票结论被谁消费）

| 消费方 | 消费什么 |
| --- | --- |
| [决议：读类后端承载的端点/记录契约](https://github.com/NongyuSystem/nongyu-backend-plus/issues/61) | 「纳入 61 个」= 需要配方的接口全集；以及 C/D/E 不纳入意味着**不要为它们设计任何端点** |
| [决议：写类请求链路下发的契约形状](https://github.com/NongyuSystem/nongyu-backend-plus/issues/62) | B 组 13 个写操作是写类配方的全部样本；D/E 的写操作（反馈、私信、浏览数）不纳入 |
| [任务：撰写学校二课新 PRD](https://github.com/NongyuSystem/nongyu-backend-plus/issues/66) | §三 范围汇总表 + §四 Out of scope 14 条直接进 PRD 的「范围」与「不包含」两节 |
| [原型：internal/ 下二课域的形状](https://github.com/NongyuSystem/nongyu-backend-plus/issues/60) | 「只有配方表 + 记录」的形状由「61 个接口、零读代理、零凭据、零缓存」直接决定 |

---

## 六、证据等级

- **接口计数与分组**：来自 [调研-学校二课一手契约与新仓接入现状.md](<docs/tech/调研-学校二课一手契约与新仓接入现状.md>)（DEX 注解静态解析），本票撰写时**逐条复核了 C 组 37、D 组 55、E 组 19 的路径与参数**。
- **重叠判断依据**：#43 的 `internal/message`、`internal/content`、`platform/objectstore` 与身份域 `internal/identity/user` 均为**仓内一手代码**。
- **未做的验证**：C/D/E 的接口是否有 A+B 的隐性依赖，仅由「参数与返回 VO 的字段引用」静态判断；**未做真实调用验证**（离线样本，无动态抓包）。若实施时发现某接口被 A+B 依赖，须回来改本表而非默认为纳入。
