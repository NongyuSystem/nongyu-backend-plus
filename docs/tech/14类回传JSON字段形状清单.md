# 14 类回传 JSON 字段形状清单（一手事实核查）

> 用途：为 GitHub issue #41「决议：后端 schema/形状校验规则（14 类回传 JSON）」提供精确的「14 类 JSON 形状清单」输入。本文件是 `/research` 产物（AFK 子任务）：**只查一手源码与仓内既有笔记，不做设计决策、不改业务代码、不建 issue、不执行 git 命令、不改动任何既有笔记**。
>
> 引用格式：`[来源: @OLD/路径:行号]`（`@OLD/` = `D:\IT\program\web-program\nongyu-system\`）、`[来源: @NEW/路径:行号]`（`@NEW/` = `D:\IT\program\web-program\nongyu-system\apps\nongyu-backend-plus\`）；行号为实际文件行号。查不到一律写「仓内未找到」，**不推测、不给设计建议**。`[来源: 本会话实测 …]` 一律转引自既有笔记记录的实测结论，本任务未重新跑码、未联网、未抓页面。

## 选址理由

按仓内习惯，技术调研 / 硬约束 / 契约类笔记统一放 `docs/tech/`：`README.md:34` 约定 `docs/tech/` 放「技术方案（需要时）」；该目录已有同性质的 `/research` 底稿（`课程表与成绩查询可重写契约清单.md`、`解析器施工级补充-4个待定解析器.md`、`回传端点契约-现有能力底座.md` 等，`glob docs/tech/*.md` 实测 20 篇）。本文件是**新文件**，命名沿用「主题-用途」形式，不改动 `docs/tech/` 下任何既有笔记。[来源: @NEW/README.md:32-35；@NEW/docs/tech/回传端点契约-现有能力底座.md:6-8]

既有笔记不适用的一句话原因（原样）：

`课程表与成绩查询可重写契约清单.md` 与 `解析器施工级补充-4个待定解析器.md` 给的是「TS 解析器逐字段契约」，面向的是**从 HTML 解析**；本任务需要的是「前端回传后、后端要校验的 JSON 形状」，两者的「顶层是数组还是对象」「哪些字段可空」「success 是否可信」尚未被集中整理成一张表，故循仓内习惯新查一次并集中落盘。

### 实际读取的一手文件清单

| 类别 | 文件（`@OLD/` = 旧仓，`@NEW/` = 新仓） | 读取范围 | 本次用途 |
| --- | --- | --- | --- |
| 解析器接口与逻辑 | `@OLD/packages/nongyu-tool-jiaowu/core/extractor/courseInfoExtractor.ts` | 1–148 全文 | `CourseItem` 字段、行有效性、`success` |
| 解析器接口与逻辑 | `@OLD/packages/nongyu-tool-jiaowu/core/extractor/scoreInfoExtractor.ts` | 1–165 全文 | `ScoreItem` 字段、`courseName && score` 门、无条件 `success:true` |
| 解析器接口与逻辑 | `@OLD/packages/nongyu-tool-jiaowu/core/extractor/examInfoExtractor.ts` | 1–140 全文 | `ExamItem` 五可选字段、散文 `examTime`、无条件 `success:true` |
| 解析器接口与逻辑 | `@OLD/packages/nongyu-tool-jiaowu/core/extractor/progressInfoExtractor.ts` | 1–120 全文 | `ProgressItem` 六必填字段、行宽启发式、无条件 `success:true` |
| 解析器接口与逻辑 | `@OLD/packages/nongyu-tool-jiaowu/core/extractor/rankInfoExtractor.ts` | 1–165 全文 | `RankItem` 单对象、早退、返回 `null` |
| 解析器接口与逻辑 | `@OLD/packages/nongyu-tool-jiaowu/core/extractor/planInfoExtractor.ts` | 1–179 全文 | `PlanInfo{title,courses}`、`PlanCourse`、`weeklyHours` 定长 10 |
| 解析器接口与逻辑 | `@OLD/packages/nongyu-tool-jiaowu/core/extractor/noticeInfoExtractor.ts` | 1–156 全文 | `NoticeItem`、上限 50、无条件 `success:true` |
| 解析器接口与逻辑 | `@OLD/packages/nongyu-tool-jiaowu/core/extractor/competitionInfoExtractor.ts` | 1–35 全文 | 竞赛复用 `NoticeItem`、上限 50、无条件 `success:true` |
| 解析器接口与逻辑 | `@OLD/packages/nongyu-tool-jiaowu/core/extractor/personalInfoExtractor.ts` | 1–251 全文 | `PersonalInfo` 16 可选字段、映射缺字段即 `undefined` |
| 解析器接口与逻辑 | `@OLD/packages/nongyu-tool-jiaowu/core/extractor/teacherInfoExtractor.ts` | 1–181 全文 | `TeacherInfo` 字段、`success = Object.keys(result).length>0` |
| 解析器接口与逻辑 | `@OLD/packages/nongyu-tool-jiaowu/core/extractor/classroomInfoExtractor.ts` | 1–109 全文 | `ClassroomInfo[]`、序号纯数字门 |
| 解析器接口与逻辑 | `@OLD/packages/nongyu-tool-jiaowu/core/extractor/classroomCourseInfoExtractor.ts` | 1–291 全文 | `{classroomName,semester,slots}`、`dayOfWeek:number`、格内字段 |
| 解析器接口与逻辑 | `@OLD/packages/nongyu-tool-jiaowu/core/extractor/teacherCourseInfoExtractor.ts` | 1–366 全文 | `{teacherName,semester,slots}`、`boolean` 字段、`TeacherSearchItem` |
| 解析器接口与逻辑 | `@OLD/packages/nongyu-tool-jiaowu/core/extractor/kaikeInfoExtractor.ts` | 1–231 全文 | `{semester,courses}`、`KaikeItem`、`COL` 列映射 |
| 解析器导出面 | `@OLD/packages/nongyu-tool-jiaowu/core/extractor/index.ts` | 1–46 全文 | 14 个 `export *` |
| 业务模块 / `@example` | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/{courseInfo,scoreInfo,examInfo,progressInfo,rankInfo,planInfo,noticeInfo,competitionInfo,personalInfo,teacherInfo,classroomInfo,classroomCourseInfo,teacherCourseInfo,kaikeInfo}.ts` | 全文（分别 104/77/78/80/66/147/97/76/69/31/32/157/152/50 行） | `get*` 入口、URL、`@example` 样本条数与字段形态 |
| 业务导出面 | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/index.ts` | 1–32 全文 | 14 个导出函数、`getKaikeInfo` 被注释 |
| 包根导出面 | `@OLD/packages/nongyu-tool-jiaowu/index.ts` | 1–68 全文 | 根导出 12 个业务函数 |
| 失败信封 | `@OLD/packages/nongyu-tool-jiaowu/core/utils/errors.ts` | 1–85 全文 | `jiaowuFailResult` 多出 `message` 字段 |
| 仓规 | `@OLD/packages/nongyu-tool-jiaowu/TECH.md` | 38–46 | 旁证（本文件不据此推论） |
| 既有底稿 | `@NEW/docs/tech/课程表与成绩查询可重写契约清单.md` | 1–441 全文 | §1.2 导出面、实测假阳性、数量级实测记录 |
| 既有底稿 | `@NEW/docs/tech/解析器施工级补充-4个待定解析器.md` | 1–494 全文 | plan/progress/exam/rank 施工级形态、构造样本实测 |
| 既有底稿 | `@NEW/docs/tech/回传端点契约-现有能力底座.md` | 1–202 全文 | 「无回传端点 / 无前端实现」的一手结论（本任务不重查端点） |
| 仓规 | `@NEW/README.md` | 25–44 | `docs/tech/` 选址 |

---

## 计数对账：为什么清单有 15 个名字而票面叫「14 类」

| # | 事实 | 来源 |
| --- | --- | --- |
| 1 | 底稿 §1.2 表共 **16 行**（含 `getClassroomCourseInfo` 按 id 直查 与 `getClassroomCourseInfoByName` 两行，二者 `result` 形状相同） | `@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:54-71`；`@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/classroomCourseInfo.ts:47-61,136-157` |
| 2 | 底稿调研范围写「业务查询模块（15 个）」「解析器（15 个）」，但枚举里各含 1 个 `index.ts`，故**实际业务模块 14 个** | `@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:19-20` |
| 3 | `core/jiaowu/index.ts` 实际导出 **14 个**业务函数（含 `getClassroomInfo` / `getTeacherInfo`）；`getKaikeInfo` 在第 9–10 行被注释「尚未完善，暂不导出」 | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/index.ts:6-32` |
| 4 | 包根 `index.ts` 只导出其中 **12 个**（未收 `getClassroomInfo` / `getTeacherInfo` / `getKaikeInfo`） | `@OLD/packages/nongyu-tool-jiaowu/index.ts:55-68` |
| 5 | 故「14 个导出函数」= 根 12 + `getClassroomInfo` + `getTeacherInfo`；`kaike` 是**第 15 个模块、零导出** | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/index.ts:9-10,30,32`；`@OLD/packages/nongyu-tool-jiaowu/index.ts:55-68` |
| 6 | 任务「14 类清单」实际列出 **15 个名字**，其中 `notice` 与 `teachingNotice` 是同一模块 `noticeInfo.ts` 的两个导出函数、`result` 形状相同（均为 `NoticeItem[]`） | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/noticeInfo.ts:63-80,89-97`；`@OLD/packages/nongyu-tool-jiaowu/core/extractor/noticeInfoExtractor.ts:20-23` |

**事实层结论**：按「对外有导出的函数」计是 14 类（根 12 + core 2）；`kaike` 为第 15 个模块且未导出。本文按任务清单**原样覆盖全部 15 个名字**逐类列出，`kaike` 单独标注「未导出」，以免漏掉票面点名的对象。

> **贯穿全部 15 类的信封事实**：每个 `get*` 成功/解析器路径返回 `{ result, success }`；一旦进 `catch`，经 `jiaowuFailResult(result, error)` 返回 **`{ success: false, result, message: string }`**，即**多一个 `message` 字段**，且 `result` 被替换成该类的空值（`[]` / `null`）。`message` 由 `resolveJiaowuErrorMessage` 归一，超时类固定为 `"教务网迁入内网，可尝试连接校园网使用农屿"`。[来源: @OLD/packages/nongyu-tool-jiaowu/core/utils/errors.ts:55-67,72-85；@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/courseInfo.ts:96-103]

---

## 顶层形状分类表

「字段名」列写的是 `result` 的精确字段名；包装对象列到第二层字段名。

| # | 类 | 对应导出函数 | `result` 顶层形状 | 精确字段名 | 来源 |
| --- | --- | --- | --- | --- | --- |
| 1 | course | `getCourseInfo`（根导出） | **数组** | `CourseItem[]` | `@OLD/.../extractor/courseInfoExtractor.ts:31-34` |
| 2 | score | `getScoreInfo`（根导出） | **数组** | `ScoreItem[]` | `@OLD/.../extractor/scoreInfoExtractor.ts:25-28` |
| 3 | exam | `getExamInfo`（根导出） | **数组** | `ExamItem[]` | `@OLD/.../extractor/examInfoExtractor.ts:22-25` |
| 4 | progress | `getProgressInfo`（根导出） | **数组** | `ProgressItem[]` | `@OLD/.../extractor/progressInfoExtractor.ts:23-26` |
| 5 | rank | `getRankInfo`（根导出） | **单对象** | `RankItem \| null`，非数组 | `@OLD/.../extractor/rankInfoExtractor.ts:28-31` |
| 6 | plan | `getPlanInfo`（根导出） | **包装对象** | `{ title, courses }`（`courses: PlanCourse[]`），`result: PlanInfo \| null` | `@OLD/.../extractor/planInfoExtractor.ts:43-48,53-56` |
| 7 | personal | `getPersonalInfo`（根导出） | **单对象** | `PersonalInfo \| null`，非数组 | `@OLD/.../extractor/personalInfoExtractor.ts:33-36` |
| 8 | notice | `getNoticeInfo`（根导出） | **数组** | `NoticeItem[]`（教学 ∪ 竞赛两段拼接） | `@OLD/.../extractor/noticeInfoExtractor.ts:20-23`；`@OLD/.../core/jiaowu/noticeInfo.ts:63-80` |
| 9 | teachingNotice | `getTeachingNoticeInfo`（根导出） | **数组** | `NoticeItem[]`（仅教学通知） | `@OLD/.../core/jiaowu/noticeInfo.ts:89-97`；`@OLD/.../extractor/noticeInfoExtractor.ts:20-23` |
| 10 | competition | `getCompetitionInfo`（根导出） | **数组** | `NoticeItem[]`（仅竞赛） | `@OLD/.../extractor/competitionInfoExtractor.ts:13,34` |
| 11 | classroomInfo | `getClassroomInfo`（仅 core 导出） | **数组** | `ClassroomInfo[] \| null` | `@OLD/.../extractor/classroomInfoExtractor.ts:29-32` |
| 12 | teacherInfo | `getTeacherInfo`（仅 core 导出） | **单对象** | `TeacherInfo \| null`，非数组 | `@OLD/.../extractor/teacherInfoExtractor.ts:41-44` |
| 13 | teacherCourse | `getTeacherCourseInfoByName`（根导出） | **包装对象** | `{ teacherName, semester, slots }`（`slots: TeacherCourseSlot[]`），`result: TeacherCourseResult \| null` | `@OLD/.../extractor/teacherCourseInfoExtractor.ts:55-67` |
| 14 | classroomCourse | `getClassroomCourseInfoByName`（根导出） | **包装对象** | `{ classroomName, semester, slots }`（`slots: ClassroomCourseSlot[]`），`result: ClassroomCourseResult \| null` | `@OLD/.../extractor/classroomCourseInfoExtractor.ts:43-55` |
| 15 | kaike | `getKaikeInfo`（**未导出**） | **包装对象** | `{ semester, courses }`（`courses: KaikeItem[]`），`result: KaikeResult \| null` | `@OLD/.../extractor/kaikeInfoExtractor.ts:65-75`；`@OLD/.../core/jiaowu/index.ts:9-10` |

**不是数组的 7 类**：`rank`、`personal`、`teacherInfo`（3 个单对象）；`plan`、`teacherCourse`、`classroomCourse`、`kaike`（4 个包装对象）。其余 8 类是数组：`course`、`score`、`exam`、`progress`、`notice`、`teachingNotice`、`competition`、`classroomInfo`。

---

## 逐类字段表

> 「类型」列按 TS 接口原样记录：全 `string` 的类占多数；**出现非 string 的只有 3 处**——`plan.weeklyHours: string[]`、`teacherCourse`/`classroomCourse` 的 `dayOfWeek: number`、`teacherCourse` 课程项的 `isExperiment` / `isContinuous: boolean`。「可空」列区分「TS 声明 `?` 可选」与「运行时实际表现」：多数 extractor 用 `pickCell` / `?.trim() || ""` 把缺列兜成 `""`（键恒在、值可空），少数用 `find()` 映射的（rank / personal / teacherInfo）缺字段时**整个键消失**（`undefined`），`JSON.stringify` 会丢键。

### 1. course — `CourseItem[]`

| 字段 | 类型 | 可空 | 已知形状 | 来源 |
| --- | --- | --- | --- | --- |
| `campus` | `string?` | 声明可选；运行时 `pickCell` 缺列 → `""` | `雅安` / `成都` / `都江堰` | `@OLD/.../extractor/courseInfoExtractor.ts:12,81,145-148`；`@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:136` |
| `courseName` | `string?` | 同上；**唯一必需字段，为空整行静默丢弃** | 课程中文名 | `courseInfoExtractor.ts:13,97-99` |
| `courseId` | `string?` | 同上 | 纯数字串 `300513911` | `courseInfoExtractor.ts:14`；`@OLD/.../core/jiaowu/courseInfo.ts:36` |
| `weeks` | `string?` | 同上 | `1-16`；也见单周 `8`；`1-14` / `1-4` | `courseInfoExtractor.ts:15`（注释「如：1-16」）；`courseInfo.ts:37,53,69` |
| `classroom` | `string?` | 同上；可 `""`（空心课） | **多行**，`\n` 分隔：`10-A309\n10-A309` | `courseInfoExtractor.ts:16`；`courseInfo.ts:38,54`；`@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:140,148` |
| `scheduleTime` | `string?` | 同上 | `3-3,3-4`、`2-9,2-10(单)`、`3-1,3-2`；**多行** `\n`；也可能是不可落格散文 `自行在线学习` | `courseInfoExtractor.ts:17`；`courseInfo.ts:39,55,71`；`@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:141,150` |
| `credit` | `string?` | 同上 | 数字串 `2` / `2.5` / `0.5` | `courseInfoExtractor.ts:18`；`courseInfo.ts:40,56,72` |
| `hours` | `string?` | 同上 | 数字串 `32` / `40` / `8` | `courseInfoExtractor.ts:19`；`courseInfo.ts:41,57,73` |
| `weeklyHours` | `string?` | 同上 | 数字串，可 `0` | `courseInfoExtractor.ts:20`；`courseInfo.ts:42,58,74` |
| `labHours` | `string?` | 同上 | 数字串，可 `0` | `courseInfoExtractor.ts:21`；`courseInfo.ts:43` |
| `assessmentMethod` | `string?` | 同上 | `卷面考核` / `其它` | `courseInfoExtractor.ts:22`；`courseInfo.ts:44,60` |
| `teacher` | `string?` | 同上；**可为空**（纯线上课） | `黄晖` / `廖鹏` / `""` | `courseInfoExtractor.ts:23`；`courseInfo.ts:45,61,77`；`@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:144` |
| `enrollmentType` | `string?` | 同上 | `初修` | `courseInfoExtractor.ts:24`；`courseInfo.ts:46` |
| `blendedTeaching` | `string?` | 同上 | 长文本，可含 URL 与换行（`1.网址：https://… 2.课表调整：…`） | `courseInfoExtractor.ts:25`；`courseInfo.ts:63`；`@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:146` |

共 **14 个字段，全部 `string`**（接口 14 个 `?: string`；extractor 一次性把 14 个键全写出）。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/courseInfoExtractor.ts:11-26,80-95,145-148]

### 2. score — `ScoreItem[]`

| 字段 | 类型 | 可空 | 已知形状 | 来源 |
| --- | --- | --- | --- | --- |
| `courseName` | `string?` | 声明可选；`pickCell` → `""` | 中文名，可含全角括注 | `scoreInfoExtractor.ts:12`；`scoreInfo.ts:35,45` |
| `score` | `string?` | 同上 | 数字串 `90` / `93`；**也可能是 `优秀`/`合格` 等级制文本**（源码注释明写） | `scoreInfoExtractor.ts:13`；`scoreInfo.ts:36,46` |
| `credit` | `string?` | 同上 | `0.5` | `scoreInfoExtractor.ts:14`；`scoreInfo.ts:37,47` |
| `gradePoint` | `string?` | 同上 | `4` / `4.3` | `scoreInfoExtractor.ts:15`；`scoreInfo.ts:38,48` |
| `term` | `string?` | 同上 | **`2023-2024-1`** | `scoreInfoExtractor.ts:16`（注释「如：2023-2024-1」）；`scoreInfo.ts:39,49` |
| `courseType` | `string?` | 同上 | `必修` / `选修` | `scoreInfoExtractor.ts:17`；`scoreInfo.ts:40,50` |
| `source` | `string?` | 同上 | `初修` / `补考` | `scoreInfoExtractor.ts:18`；`scoreInfo.ts:41,51` |
| `note` | `string?` | 同上；**常为 `""`** | `""` | `scoreInfoExtractor.ts:19`；`scoreInfo.ts:42,52` |

共 **8 个字段，全部 `string`**。`score` 与 `courseName` 同时非空才是有效行。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/scoreInfoExtractor.ts:11-20,72-86]

### 3. exam — `ExamItem[]`

| 字段 | 类型 | 可空 | 已知形状 | 来源 |
| --- | --- | --- | --- | --- |
| `courseName` | `string?` | 声明可选；`pickCell` → `""` | 课程中文名 | `examInfoExtractor.ts:12`；`examInfo.ts:35` |
| `examTime` | `string?` | 同上 | **整段散文**：`【考试时间：2026-7-2】 第18周-星期4-上午(09:00—11:00)   请08:45到考场`；日期不补零、`星期` 后阿拉伯数字、时段 `上午/下午/晚上`、区间分隔 **U+2014 `—`**、`请HH:MM到考场` 前置多个空格；**extractor 不做任何清洗** | `examInfoExtractor.ts:13,66-78,137-140`；`examInfo.ts:36,43,50`；`@NEW/docs/tech/解析器施工级补充-4个待定解析器.md:241-258,260-262` |
| `examRoom` | `string?` | 同上 | `雅安校区:10-A303` | `examInfoExtractor.ts:14`；`examInfo.ts:37` |
| `seatNumber` | `string?` | 同上 | `17` / `95` | `examInfoExtractor.ts:15`；`examInfo.ts:38,52` |
| `assessmentMethod` | `string?` | 同上 | `卷面考核` | `examInfoExtractor.ts:16`；`examInfo.ts:39` |

共 **5 个字段，全部 `string`、全部可选**（接口 `ExamItem` 五字段皆 `?: string`）。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/examInfoExtractor.ts:11-17；@NEW/docs/tech/解析器施工级补充-4个待定解析器.md:264]

### 4. progress — `ProgressItem[]`

| 字段 | 类型 | 可空 | 已知形状 | 来源 |
| --- | --- | --- | --- | --- |
| `type` | `string`（**必填，无 `?`**） | 运行时可含 `\n`（`必修\n（含专业基础与专业核心）`）；`row[offset]` 越界时 `undefined` → JSON 丢键 | `必修` / `通识选修` / `合 计` | `progressInfoExtractor.ts:11-18,65`；`progressInfo.ts:34,42,50`；`@NEW/docs/tech/解析器施工级补充-4个待定解析器.md:176,215` |
| `required` | `string`（必填） | 同左；可含 `\n`，无 `?.` 兜底时 `undefined` | `105` / `8` / `170` | `progressInfoExtractor.ts:13,66`；`progressInfo.ts:35,43,51` |
| `earned` | `string`（必填） | 同左；`90.5`、`13\n[其中 自然科学类:2]` | `progressInfoExtractor.ts:14,67`；`progressInfo.ts:36,44,52` |
| `diff` | `string`（必填） | 同左；可负 `-14.5` / `-40.5` | `progressInfoExtractor.ts:15,68`；`progressInfo.ts:37,53` |
| `transfer` | `string`（必填） | 同左；可 `/` | `progressInfoExtractor.ts:16,69`；`progressInfo.ts:38,46,54` |
| `progress` | `string`（必填） | **5 列行时 `row[offset+5]` 为 `undefined`，键从 JSON 消失**（下游 `parseProgressPercent` 会 TypeError） | 百分比 `86.19%` / `162.5%` / `76.18%`；真实文本形态仓内无样本 | `progressInfoExtractor.ts:17,70`；`progressInfo.ts:39,47,55`；`@NEW/docs/tech/解析器施工级补充-4个待定解析器.md:176,199-202,493` |

共 **6 个字段，全部 `string` 且接口层必填**；但运行时无任何 `?? ""` 兜底，缺列即 `undefined`。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/progressInfoExtractor.ts:11-18,61-73；@NEW/docs/tech/解析器施工级补充-4个待定解析器.md:176]

### 5. rank — `RankItem`（单对象）

| 字段 | 类型 | 可空 | 已知形状 | 来源 |
| --- | --- | --- | --- | --- |
| `index` | `string?` | 全可选；`item = {}` 起始，**只 push 非空值** → 缺字段整键消失 | `1` | `rankInfoExtractor.ts:12,87-98`；`rankInfo.ts:32` |
| `campus` | `string?` | 同上 | `雅安` | `rankInfoExtractor.ts:13`；`rankInfo.ts:33` |
| `college` | `string?` | 同上 | `信息工程学院` | `rankInfoExtractor.ts:14`；`rankInfo.ts:34` |
| `major` | `string?` | 同上 | `物联网工程` | `rankInfoExtractor.ts:15`；`rankInfo.ts:35` |
| `grade` | `string?` | 同上 | `2023` | `rankInfoExtractor.ts:16`；`rankInfo.ts:36` |
| `studentId` | `string?` | 同上 | `202308596` | `rankInfoExtractor.ts:17`；`rankInfo.ts:37` |
| `name` | `string?` | 同上 | `唐磊` | `rankInfoExtractor.ts:18`；`rankInfo.ts:38` |
| `className` | `string?` | 同上 | `物联网202301` | `rankInfoExtractor.ts:19`；`rankInfo.ts:39` |
| `weightedScore` | `string?` | 同上 | `81.71` | `rankInfoExtractor.ts:20`；`rankInfo.ts:40` |
| `majorRank` | `string?` | 同上 | **纯名次 `114`**；注释写「如 `1/120`」与 `@example` 矛盾，下游按「第 N 名」消费 | `rankInfoExtractor.ts:21`；`rankInfo.ts:41`；`@NEW/docs/tech/解析器施工级补充-4个待定解析器.md:387-393` |
| `status` | `string?` | 同上 | `在读` | `rankInfoExtractor.ts:22`；`rankInfo.ts:42` |

共 **11 个字段，全部 `string`、全部可选**。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/rankInfoExtractor.ts:11-23,87-98]

### 6. plan — `{ title, courses: PlanCourse[] }`（包装对象）

顶层：`title: string`、`courses: PlanCourse[]`。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/planInfoExtractor.ts:43-48]

| 字段 | 类型 | 可空 | 已知形状 | 来源 |
| --- | --- | --- | --- | --- |
| `title` | `string` | 取不到 `class=g_title` 给 `""` | `物联网工程2023` | `planInfoExtractor.ts:44-45,85-88`；`planInfo.ts:55` |
| `courses[].courseCode` | `string` | `?.trim() \|\| ""` 兜底；**为空整行丢弃**（`mapRowToCourse` 返回 `null`） | `1210004000` | `planInfoExtractor.ts:13,134-135`；`planInfo.ts:58` |
| `courses[].courseName` | `string` | 同上 | `C语言程序设计` | `planInfoExtractor.ts:15,146`；`planInfo.ts:59` |
| `courses[].englishName` | `string` | 同上 | `C Language Programming` | `planInfoExtractor.ts:17,147`；`planInfo.ts:60` |
| `courses[].courseType` | `string` | 同上 | `必修` / `实践教学` | `planInfoExtractor.ts:19,148`；`planInfo.ts:61,91` |
| `courses[].courseSystem` | `string` | 同上 | `专业基础课` / `通识实践` | `planInfoExtractor.ts:21,149`；`planInfo.ts:62,92` |
| `courses[].credits` | `string` | 同上 | `2.5` / `0.5` | `planInfoExtractor.ts:23,150`；`planInfo.ts:63` |
| `courses[].totalHours` | `string` | 同上 | `40` / `0` | `planInfoExtractor.ts:25,151`；`planInfo.ts:64` |
| `courses[].lectureHours` | `string` | 同上 | `40` / `0` | `planInfoExtractor.ts:27,152` |
| `courses[].labHours` | `string` | 同上 | `0` / `24` | `planInfoExtractor.ts:29,153` |
| `courses[].practiceHours` | `string` | 同上 | `0` / `0.5` | `planInfoExtractor.ts:31,154` |
| `courses[].selfStudyHours` | `string` | 同上 | `0` | `planInfoExtractor.ts:33,155` |
| `courses[].weeklyHours` | **`string[]`** | **定长 10**（`for i<10`），缺项补 `""`；索引 0 = 第一学期 | `['40','','','','','','','','','']` / 全空数组 | `planInfoExtractor.ts:34-35,138-142,156`；`planInfo.ts:69,84,99` |
| `courses[].execSemester` | `string` | 同上 | `1` | `planInfoExtractor.ts:37,157`；`planInfo.ts:70` |

`PlanCourse` 源码声明 **13 个字段**（12 个 `string` + 1 个 `string[]`）。⚠️ 底稿写「`PlanCourse` 16 字段」，`planInfoExtractor.ts:126-129` 注释里的「24 列」是**页面列数**（列 12–21 是十个学期周学时，被收进 `weeklyHours` 一个数组字段），不是 `result` 字段数。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/planInfoExtractor.ts:11-38,126-129；@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:203]

其它形状线索：`getPlanInfo` **并行抓 2 页**（`y=1`/`y=2`）后按 `courseCode` 去重合并；固定参数串里 `p=80`（页尺寸）。[来源: @OLD/packages/nongyu-tool-jiaowu/core/jiaowu/planInfo.ts:19-20,25-27,120-142]

### 7. personal — `PersonalInfo`（单对象）

| 字段 | 类型 | 可空 | 已知形状 | 来源 |
| --- | --- | --- | --- | --- |
| `name` | `string?` | 全可选；`mapToStandardFields` 用 `find()`，**缺字段即 `undefined` → JSON 丢键** | `张三` | `personalInfoExtractor.ts:12,227-251`；`personalInfo.ts:31` |
| `studentId` | `string?` | 同上 | `20210001` | `personalInfoExtractor.ts:13`；`personalInfo.ts:32` |
| `gender` | `string?` | 同上 | `男` | `personalInfoExtractor.ts:14`；`personalInfo.ts:33` |
| `college` | `string?` | 同上 | `农学院` | `personalInfoExtractor.ts:15`；`personalInfo.ts:34` |
| `major` | `string?` | 同上 | `农学` | `personalInfoExtractor.ts:16`；`personalInfo.ts:35` |
| `grade` | `string?` | 同上 | `2021` | `personalInfoExtractor.ts:17`；`personalInfo.ts:36` |
| `className` | `string?` | 同上 | `农学202101` | `personalInfoExtractor.ts:18`；`personalInfo.ts:37` |
| `identity` | `string?` | 同上 | `本科` | `personalInfoExtractor.ts:19`；`personalInfo.ts:38` |
| `studentStatus` | `string?` | 同上 | `在读` | `personalInfoExtractor.ts:20`；`personalInfo.ts:39` |
| `enrollmentDate` | `string?` | 同上 | `2021-09-01` | `personalInfoExtractor.ts:21`；`personalInfo.ts:40` |
| `ethnicity` | `string?` | 同上 | `汉族` | `personalInfoExtractor.ts:22`；`personalInfo.ts:41` |
| `politicalStatus` | `string?` | 同上 | `共青团员` | `personalInfoExtractor.ts:23`；`personalInfo.ts:42` |
| `phone` | `string?` | 同上 | `138****0000` | `personalInfoExtractor.ts:24`；`personalInfo.ts:43` |
| `examId` | `string?` | 同上 | `202101010001` | `personalInfoExtractor.ts:25`；`personalInfo.ts:44` |
| `homeAddress` | `string?` | 同上 | `四川省成都市温江区` | `personalInfoExtractor.ts:26`；`personalInfo.ts:45` |
| `campus` | `string?` | 同上 | `成都校区` | `personalInfoExtractor.ts:27`；`personalInfo.ts:46` |

共 **16 个字段，全部 `string`、全部可选**。映射前先 `cleanRawValues` 去掉全角/半角括注。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/personalInfoExtractor.ts:11-28,209-222,227-251]

### 8 / 9 / 10. notice / teachingNotice / competition — `NoticeItem[]`

三类共用同一 `NoticeItem` 形状。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/noticeInfoExtractor.ts:11-15；competitionInfoExtractor.ts:13]

| 字段 | 类型 | 可空 | 已知形状 | 来源 |
| --- | --- | --- | --- | --- |
| `title` | `string`（**必填**） | `title && url` 同时为真才 push；`cleanTitle` 压空白、解 `&nbsp;` | 中文标题（可含空格） | `noticeInfoExtractor.ts:12,83,87,89-91,151-156`；`noticeInfo.ts:34,39,44` |
| `url` | `string`（**必填**） | 同上；相对路径补全到 `https://jiaowu.sicau.edu.cn/web/web/web/`；`javascript:` 前缀原样保留 | `https://jwnews.sicau.edu.cn/jwnews/view.asp?id=1396` 或 `https://jiaowu.sicau.edu.cn/web/web/web/#` | `noticeInfoExtractor.ts:13,28,86,143-149`；`noticeInfo.ts:35,40,45` |
| `date` | `string?`（**可选**） | 正则第 4 组 `match[4]?.trim()`，可为 `undefined` | `06-16` / `06-15` / `06-12`（月-日，无年份） | `noticeInfoExtractor.ts:14,34-35,84,90`；`noticeInfo.ts:36,41,46` |

**源码硬上限 50 条**（`parseNoticeListItems(section, 50)` / `MAX_ITEMS = 50`），不是 `@example` 样本条数。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/noticeInfoExtractor.ts:46,63,74-80；competitionInfoExtractor.ts:21,32]

`getNoticeInfo` 是教学 ∪ 竞赛两段拼接 `[...teachingResult.result, ...competitionResult.result]`。[来源: @OLD/packages/nongyu-tool-jiaowu/core/jiaowu/noticeInfo.ts:63-75]

### 11. classroomInfo — `ClassroomInfo[]`

| 字段 | 类型 | 可空 | 已知形状 | 来源 |
| --- | --- | --- | --- | --- |
| `index` | `string`（必填） | `?.trim() \|\| ""`；**必须 `^\d+$` 才 push** | 纯数字序号 | `classroomInfoExtractor.ts:13,87,102-104` |
| `campus` | `string`（必填） | 同上 | 校区简称 | `classroomInfoExtractor.ts:15,95` |
| `location` | `string`（必填） | 同上 | `10-A104` | `classroomInfoExtractor.ts:17,96`；`@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:220` |
| `capacity` | `string`（必填） | 同上 | `188` | `classroomInfoExtractor.ts:19,97`；`@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:220` |
| `type` | `string`（必填） | 同上 | 教室类别 | `classroomInfoExtractor.ts:21,98` |
| `classroomId` | `string`（必填） | 从末列链接 `bianhao=` 抠，取不到给 `""`；实测为 32 位串 | 32 位不透明串 | `classroomInfoExtractor.ts:23,90-91,99`；`@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:220` |

共 **6 个字段，全部 `string` 且接口层必填**；`result` 类型声明为 `ClassroomInfo[] | null`。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/classroomInfoExtractor.ts:11-24,29-32]

### 12. teacherInfo — `TeacherInfo`（单对象）

| 字段 | 类型 | 可空 | 已知形状 | 来源 |
| --- | --- | --- | --- | --- |
| `name`,`gender`,`campus`,`department`,`joinDate`,`ethnicity`,`education`,`degree`,`title`,`partyPosition`,`teachingUnit`,`section`,`graduateSchool`,`graduateMajor`,`officePhone`,`mobilePhone`,`officeAddress`,`qq`,`email`,`mainCourses`,`researchDirection`,`achievements`,`teacherMessage`,`updateDate` | 全部 `string?` | 全可选；`mapToTeacherFields` 用 `find()`，**缺字段即 `undefined` → 键消失**；`updateDate` 从「(最近更新日期：…)」单独抽 | 中文简历文本；仓内无 `@example` 字段值样本 | `teacherInfoExtractor.ts:11-36,104-118,149-181`；`teacherInfo.ts:16-20` |

源码 `TeacherInfo` 声明 **24 个 `?: string` 字段**。⚠️ 底稿写「`TeacherInfo` 23 字段」，与源码不符，以源码为准。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/teacherInfoExtractor.ts:11-36；@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:208]

### 13. teacherCourse — `{ teacherName, semester, slots }`（包装对象）

顶层：`teacherName: string`、`semester: string`、`slots: TeacherCourseSlot[]`。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/teacherCourseInfoExtractor.ts:55-62]

`slots[]` 槽位：[来源: 同文件 `:41-50`]

| 字段 | 类型 | 可空 | 已知形状 | 来源 |
| --- | --- | --- | --- | --- |
| `dayOfWeek` | **`number`** | 必填 | `1-7`（星期一~星期日）；由 `d+1` 生成 | `teacherCourseInfoExtractor.ts:42-43,150-156`；`teacherCourseInfo.ts:66,84` |
| `period` | `string` | 必填；取不到给 `""` | `上午` / `下午` / `晚上` | `teacherCourseInfoExtractor.ts:45,124-136`；`teacherCourseInfo.ts:67,85` |
| `slot` | `string` | 必填 | `12节` / `34节` / `56节` / `78节` / `910节` | `teacherCourseInfoExtractor.ts:47`；`teacherCourseInfo.ts:68,86` |
| `courses` | `TeacherCourseItem[]` | 必填 | 见下 | `teacherCourseInfoExtractor.ts:49` |

`courses[]` 课程项（**9 个字段，含 `boolean`**）：[来源: 同文件 `:17-36,280-290`]

| 字段 | 类型 | 可空 | 已知形状 | 来源 |
| --- | --- | --- | --- | --- |
| `courseName` | `string` | 必填；可含 `_性质班号` 后缀未拆净的长串 | `数据结构实验` / `数据结构41520240701020202` | `teacherCourseInfoExtractor.ts:19,211-250`；`teacherCourseInfo.ts:71,89,100` |
| `courseType` | `string` | 必填；可为 `""` | `必修` / `""` | `teacherCourseInfoExtractor.ts:21`；`teacherCourseInfo.ts:72,90` |
| `classId` | `string` | 必填；可为 `""` | `4152024_1` | `teacherCourseInfoExtractor.ts:23`；`teacherCourseInfo.ts:73` |
| `location` | `string` | 必填；可为 `""` | `雅安校区：10-B302室` | `teacherCourseInfoExtractor.ts:25`；`teacherCourseInfo.ts:74` |
| `weekRange` | `string` | 必填；可为 `""` | `3-14` / `1-14` | `teacherCourseInfoExtractor.ts:27,267-268`；`teacherCourseInfo.ts:75,93` |
| `weekPattern` | `string` | 必填；**`""`=全周、`单`、`双`** | `''` / `单` / `双` | `teacherCourseInfoExtractor.ts:29,269-273`；`teacherCourseInfo.ts:76,94,105` |
| `isExperiment` | **`boolean`** | 必填 | `true` / `false` | `teacherCourseInfoExtractor.ts:31,263-264`；`teacherCourseInfo.ts:77,95` |
| `isContinuous` | **`boolean`** | 必填 | `true` / `false` | `teacherCourseInfoExtractor.ts:33,273`；`teacherCourseInfo.ts:78,96` |
| `hoursUnit` | `string` | 必填；可为 `""` | `2学时` | `teacherCourseInfoExtractor.ts:35,274-276`；`teacherCourseInfo.ts:79` |

`semester` 形态 `2025-2026-2`，取不到给 `""`（标题正则回落）。[来源: teacherCourseInfoExtractor.ts:96-105；teacherCourseInfo.ts:63]

附带（非回传主形状）：`extractTeacherSearchResults` 另出 `TeacherSearchItem[]`（`index/campus/department/name/bianhao`，全 `string`），供按姓名查教师时中转。[来源: teacherCourseInfoExtractor.ts:299-310,317-365]

### 14. classroomCourse — `{ classroomName, semester, slots }`（包装对象）

顶层：`classroomName: string`、`semester: string`、`slots: ClassroomCourseSlot[]`。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/classroomCourseInfoExtractor.ts:43-50]

| 层级 | 字段 | 类型 | 可空 | 已知形状 | 来源 |
| --- | --- | --- | --- | --- | --- |
| slot | `dayOfWeek` | **`number`** | 必填 | `1-7`；`d+1` | `classroomCourseInfoExtractor.ts:31,150-162`；`classroomCourseInfo.ts:91,106` |
| slot | `period` | `string` | 必填；可 `""` | `上午` | `classroomCourseInfoExtractor.ts:33,125-137`；`classroomCourseInfo.ts:92` |
| slot | `slot` | `string` | 必填 | `34节` / `56节` / `12节` | `classroomCourseInfoExtractor.ts:35,142-145`；`classroomCourseInfo.ts:93,107` |
| slot | `courses` | `ClassroomCourseItem[]` | 必填 | 见下 | `classroomCourseInfoExtractor.ts:37` |
| course | `courseName` | `string` | 必填 | `射频识别原理与应用` | `classroomCourseInfoExtractor.ts:13,213,261-262`；`classroomCourseInfo.ts:96` |
| course | `teacher` | `string` | 必填；可 `""` | `王乾丰`；可含 `(单)/(双)` 后缀被剥离 | `classroomCourseInfoExtractor.ts:15,219-232`；`classroomCourseInfo.ts:97` |
| course | `weekRange` | `string` | 必填；可 `""` | `1-12` / `1-14` | `classroomCourseInfoExtractor.ts:17,197,242-243`；`classroomCourseInfo.ts:98` |
| course | `studentCount` | `string` | 必填；可 `""` | `62` / `161` | `classroomCourseInfoExtractor.ts:19,198,244-245`；`classroomCourseInfo.ts:99` |
| course | `courseType` | `string` | 必填 | `必修` / `选修` / `限选` / `任选` / `公选`；**找不到 courseType 整门课跳过** | `classroomCourseInfoExtractor.ts:21,199-200,254-259`；`classroomCourseInfo.ts:100` |
| course | `weekPattern` | `string` | 必填；**`""`=全周、`单`、`双`** | `''` / `单` / `双` | `classroomCourseInfoExtractor.ts:23,217-223`；`classroomCourseInfo.ts:101,116` |

`semester` 形态 `2025-2026-2`（`@example`），但**实测真实页面取不到、为 `""`**；`classroomName` 形态 `10-A104`。[来源: classroomCourseInfoExtractor.ts:92-99；classroomCourseInfo.ts:87-88；@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:219,436]

### 15. kaike — `{ semester, courses }`（包装对象，**未导出**）

顶层：`semester: string`、`courses: KaikeItem[]`。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/kaikeInfoExtractor.ts:65-70]

`KaikeItem` **24 个 `string` 字段**（源码接口 `:11-60`）：`index`、`courseCode`、`department`、`courseIdWithName`、`courseName`、`courseType`、`classroom`、`scheduleTime`、`weekRange`、`credits`、`lectureHours`、`labHours`、`practiceHours`、`totalHours`、`teacher`、`plannedCount`、`selectedCount`、`remainingCount`、`className`、`isLocked`、`campus`、`courseCategory`、`courseSystem`、`scheduleType`。[来源: 同文件 `:11-60,183-208`]

| 字段 | 类型 | 可空 | 已知形状 | 来源 |
| --- | --- | --- | --- | --- |
| `scheduleTime` | `string` | **无 `\|\| ""` 兜底**，`values[COL.X]` 可能 `undefined`；`cleanScheduleTime` 去 `\n` | `1-5,1-6` | `kaikeInfoExtractor.ts:26-27,191,227-231` |
| `weekRange` | `string` | 同上 | `10-13` | `kaikeInfoExtractor.ts:28-29,192` |
| `courseIdWithName` | `string` | 同上 | `形势与政策Ⅲ4222025_1` | `kaikeInfoExtractor.ts:18-19,187` |
| `index` | `string` | 同上；**必须 `^\d+$` 才 push** | 纯数字 | `kaikeInfoExtractor.ts:12-13,184,210-212` |
| `semester`（顶层） | `string` | 从 `g_title` 里 `(\d{4}-\d{4}-\d)` 抽，取不到 `""` | `2025-2026-2` | `kaikeInfoExtractor.ts:66-67,149-159` |

⚠️ 底稿写「`KaikeItem` 26 字段」：源码接口是 **24** 个；`kaikeInfoExtractor.ts:80-81` 注释的「前 26 列为数据」是**页面列数**，`COL` 常量只映射 24 列（列 21 空跳过），行宽 `< 25` 直接 skip。[来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/kaikeInfoExtractor.ts:80-109,180-181；@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:204]

导出差异（源码事实）：`getKaikeInfo` 在 `core/jiaowu/index.ts:9-10` 被注释「尚未完善，暂不导出」；`kaikeInfo.ts:20-23` 的 `KAIKE_PARAMS` 从未拼进 URL（`:44` 只用裸 `KAIKE_URL`）；`kaikeInfoExtractor.ts:114` 的 `KAIKE_URL`（带 `title_id1=2`）是未被引用的死常量。[来源: @OLD/packages/nongyu-tool-jiaowu/core/jiaowu/index.ts:9-10；@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/kaikeInfo.ts:15,20-23,42-45；@OLD/packages/nongyu-tool-jiaowu/core/extractor/kaikeInfoExtractor.ts:114；@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:66,204,439]

---

## success 与空值语义

### 逐类一行

| 类 | `success` 判定条件 | 找到表即 `success:true` 哪怕空数组？ | 无条件 `success:true`？ | 行有效性 / 静默丢弃 | `result: null` 出现在哪 | 来源 |
| --- | --- | --- | --- | --- | --- | --- |
| course | `items.length > 0`（第一个能产出非空 items 的表直接返回 `success:true`）；空/非字符串 → `false` | **否**（无数据即 `false`） | 否 | `if (item.courseName)` 才 push，**缺 `courseName` 静默丢弃**；列缺失 `pickCell`→`""` | 否（失败为 `[]`） | `courseInfoExtractor.ts:53-57,97-107,145-148` |
| score | **找到表（`tableHit && grid.length>=2`）即无条件 `return {result:items, success:true}`** | **是** | 否（需先找到表） | `if (item.courseName && item.score)` 同时非空，**缺任一静默丢弃** | 否（失败为 `[]`） | `scoreInfoExtractor.ts:55-58,83-92` |
| exam | **找到表即无条件 `success:true`** | **是** | 否（需先找到表） | `if (item.courseName \|\| item.examTime)` 任一非空 | 否（失败为 `[]`） | `examInfoExtractor.ts:50-54,77-85` |
| progress | **找到表即无条件 `success:true`** | **是** | 否（需先找到表） | 过滤 `row.length<5`、含「课程性质」/「应修」的表头行、空行 `type && (required\|\|earned)` | 否（失败为 `[]`） | `progressInfoExtractor.ts:45-55,59-60,73-78` |
| rank | 命中第一行「有数据」（`studentId`/`name`/`weightedScore`/`majorRank` 任一非空）即 `return {result:item, success:true}`（**循环体内早退**）；无表/无有效行 → `{result:null, success:false}` | 否 | 否 | 空值字段不写入 `item`；**命中第一行后其余行静默丢弃** | **是**：无表/表<2 行/无有效行/登录超时；`catch` → `jiaowuFailResult(null)` | `rankInfoExtractor.ts:50-59,85-105`；`rankInfo.ts:64` |
| plan | `success = courses.length > 0`；`getPlanInfo` 合并两页后 `success: allCourses.length > 0` | 否 | 否 | `courseCode` 为空的行 `mapRowToCourse` 返回 `null` 丢弃；标题取不到给 `""` | **是**：登录超时（`extractPlanInfo`）/ `catch`；正常路径即便 `courses:[]` 也返回 `{title:"",courses:[]}` 非 `null` | `planInfoExtractor.ts:70-79,98-121,134-135`；`planInfo.ts:136-145` |
| personal | 主路径或 legacy 映射出任意非空 `rawData` 即 `success:true`；含「登录超时」或两套都空 → `false` | 否 | 否 | 映射层 `find()`，缺字段即 `undefined` | **是**：登录超时 / 两套都空 / `catch` | `personalInfoExtractor.ts:54-72,227-251`；`personalInfo.ts:67` |
| notice | **入参非空即无条件 `success:true`**（一条没抽到也 `true`） | 不适用（无「找表」步骤） | **是** | `title && url` 才 push | 否（空为 `[]`） | `noticeInfoExtractor.ts:40-52,89-91` |
| teachingNotice | 同上 | 不适用 | **是** | 同上 | 否 | `noticeInfoExtractor.ts:40-52,89-91`；`noticeInfo.ts:89-97` |
| competition | **入参非空即无条件 `success:true`** | 不适用 | **是** | 同上（复用 `parseNoticeListItems`） | 否 | `competitionInfoExtractor.ts:26-34`；`noticeInfoExtractor.ts:74-95` |
| classroomInfo | `success = rows.length > 0`；没有 `id=grid` → 返回 `[]` 且 `success:false`（不报错） | 否 | 否 | 行 `values<5` 跳过、`index` 非 `^\d+$` 跳过 | **是**：登录超时 / `catch`；正常「找不到 grid」是 `[]` 非 `null` | `classroomInfoExtractor.ts:50-56,62-65,85,102-104`；`classroomInfo.ts:30` |
| teacherInfo | `success = Object.keys(result).length > 0`（**只要映射到任何字段就成功**） | 否 | 否 | `find()` 缺字段即 `undefined` | **是**：登录超时 / `catch`；正常路径 `{}`（空对象）非 `null`，但 `success:false` | `teacherInfoExtractor.ts:63-71,149-181`；`teacherInfo.ts:29` |
| teacherCourse | `success = slots.length > 0` | 否 | 否 | 槽位文本须含「节」；格内多课按 `--------------------` 切分 | **是**：找不到教师 / 登录超时 / `catch` | `teacherCourseInfoExtractor.ts:78-90,141,191-193`；`teacherCourseInfo.ts:138-142,150` |
| classroomCourse | `success = slots.length > 0`（找不到课表表格时 `slots:[]` → `false`，与「有表但空格」同归失败） | 否 | 否 | 槽位文本须含「节」；格内**找不到 `courseType` 整门课跳过**（防死循环 `if (!courseType) { i++; continue; }`） | **是**：找不到教室 / 登录超时 / `catch` | `classroomCourseInfoExtractor.ts:74-86,145,254-259`；`classroomCourseInfo.ts:144-147,155` |
| kaike | `success = courses.length > 0` | 否 | 否 | 行宽 `< 25` 直接 skip；`index` 非 `^\d+$` 跳过 | **是**：登录超时 / `catch` | `kaikeInfoExtractor.ts:133-145,180-181,210-212`；`kaikeInfo.ts:48` |

补充三条跨类语义：

1. **`success:true` 有两种完全不同的成因**：「网络/会话失败走 `catch`」（带 `message`）与「抓到了但页面里没数据」（extractor 自己返回 `success:false`，**不带 `message`**）。[来源: @NEW/docs/tech/课程表与成绩查询可重写契约清单.md:48-50；@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/courseInfo.ts:96-103]
2. **`getNoticeInfo` 合并版** `success: teachingResult.success || competitionResult.success`，由于两个子解析器都无条件 `true`，只要不抛错即恒 `true`。[来源: @OLD/packages/nongyu-tool-jiaowu/core/jiaowu/noticeInfo.ts:70-75]
3. **「登录超时」守卫不是全类都有**：`plan`/`personal`/`teacherInfo`/`classroomInfo`/`classroomCourse`/`teacherCourse`/`kaike` 的 extractor 显式含 `html.includes("登录超时")` → `success:false`；`notice`/`teachingNotice`/`competition` **没有该守卫**，登录超时页仍返回 `success:true + []`。[来源: planInfoExtractor.ts:71；personalInfoExtractor.ts:55；teacherInfoExtractor.ts:64；classroomInfoExtractor.ts:51；classroomCourseInfoExtractor.ts:75；teacherCourseInfoExtractor.ts:79；kaikeInfoExtractor.ts:134；noticeInfoExtractor.ts:40-52；competitionInfoExtractor.ts:26-34；@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:225]

### 已证实的假阳性

**A. 真实页面实测（底稿记录，本会话未重跑、样本已清理不可复跑）** [来源: @NEW/docs/tech/课程表与成绩查询可重写契约清单.md:36-38,227-232]

| # | 假阳性 | `success` | 来源 |
| --- | --- | --- | --- |
| A1 | 教师课表空壳页（`jiaoshikb_new.asp?bianhao=1`，正文「（加密验证未通过）」+ 空课表 + 红字「特别提示」）喂 `extractCourseInfo`，**「特别提示」那段散文被认成课程名**，返回 `result` 非空 | `true` | `@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:229` |
| A2 | 同一空壳页喂 `extractExamInfo`，**同样把该段散文当 `courseName`** | `true` | 同上 `:229` |
| A3 | 教室搜索页喂 `extractPersonalInfo` → `{identity:"查看"}` | `true` | `@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:230` |
| A4 | 教师搜索页喂 `extractPersonalInfo` → `{college:"张洁", identity:"查看", campus:"张洁1"}` | `true` | 同上 `:230` |
| A5 | 登录超时页（`alert('系统提示：登录超时，请重新登录！')`，GBK）喂全部 11 个解析器：9 个走「含登录超时」分支 `success:false`，**`extractTeachingNotices` / `extractCompetitionInfo` 返回 `success:true + []`** | `true`（通知/竞赛） | 同上 `:225` |

底稿结论原文：「**`success` 这个布尔在课表/考试/个人信息三处都不足以判「取到了真数据」**……这一条对「缓存」尤其致命——脏数据一旦 `success:true` 就会被缓存住。」[来源: @NEW/docs/tech/课程表与成绩查询可重写契约清单.md:232]

**B. 构造 HTML 样本实测（验证旧解析器代码逻辑，不是教务真实结构）** [来源: @NEW/docs/tech/解析器施工级补充-4个待定解析器.md:43-47]

| # | 假阳性 | `success` | 来源 |
| --- | --- | --- | --- |
| B1 | `rank` 表头上方多一行说明 → 返回 `{major:"序号", majorRank:"序号"}`（把列头文字当排名） | `true` | `@NEW/docs/tech/解析器施工级补充-4个待定解析器.md:350-367` |
| B2 | `rank` 的 `专业排名` 列排在 `专业` 前 → `major` 被写成排名数字 `114`（与 `majorRank` 变成同一个值） | `true` | 同上 `:369-379` |
| B3 | `progress` 教务新增一列（7→8 列）→ 整行左移、`type` 变成学分数字 `160`、**每位都错但没有一位是空的**，`type && (required\|\|earned)` 照样放行 | `true` | 同上 `:182-197` |
| B4 | `exam` 表头「课程性质」在「课程名称」前 → `courseName` 取到「必修」 | `true` | 同上 `:467` |

**C. 源码层已被指出的假阳性风险（非实测）**：`extractPlanInfo` 的成功判据只有「存在 `class=tablebody` 的 table 且扫到 `g_body_[12]` 行」，**任何符合 class 的表都能让 `success:true`**。[来源: @NEW/docs/tech/解析器施工级补充-4个待定解析器.md:122；@OLD/packages/nongyu-tool-jiaowu/core/extractor/planInfoExtractor.ts:70-79,98-121]

---

## 数量级线索

> ⚠️ **以下全部是旧仓 `@example` 文档里手工写的样本条数，不是真实页面抓取结果；样本被截断过（例如培养方案只列 3 门、通知只列 3 条），不能当作任何合理数量范围的上下限依据。** 源码里另有硬上限/分页参数，见本节末尾注。

| 类 | `@example` 样本条数 | 来源 |
| --- | --- | --- |
| course（课表） | **3 门课**（3 个 `CourseItem`） | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/courseInfo.ts:32-81`（对象起点 `:33,:49,:65`） |
| score（成绩） | **2 条**（2 个 `ScoreItem`） | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/scoreInfo.ts:33-53`（`:34,:44`） |
| notice（教务通知） | **3 条** | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/noticeInfo.ts:32-48`（`:33,:38,:43`） |
| teachingNotice（教学通知） | **仓内未找到**（`getTeachingNoticeInfo` 无 `@example 成功示例`） | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/noticeInfo.ts:89-97` |
| competition（竞赛） | **3 条** | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/competitionInfo.ts:37-53`（`:38,:43,:48`） |
| exam（考试） | **3 条** | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/examInfo.ts:33-55`（`:34,:41,:48`） |
| progress（进度） | **3 条**（含一条 `type:'合 计'` 合计行） | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/progressInfo.ts:32-57`（`:33,:41,:49`） |
| plan（培养方案） | **3 门课**（`courses` 数组 3 项） | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/planInfo.ts:56-102`（`:57,:72,:88`） |
| rank（排名） | **1 个单对象**（非数组） | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/rankInfo.ts:31-43` |
| personal（学籍） | **1 个单对象**（非数组） | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/personalInfo.ts:30-47` |
| teacherCourse（教师课表） | **2 个 slots / 3 门课** | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/teacherCourseInfo.ts:64-112` |
| classroomCourse（教室课表） | **2 个 slots / 2 门课** | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/classroomCourseInfo.ts:89-120` |
| classroomInfo（教室列表） | **仓内未找到**（`@example` 只有基本调用，无数组条数） | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/classroomInfo.ts:17-21` |
| teacherInfo（教师信息） | **仓内未找到**（单对象，`@example` 只有基本调用） | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/teacherInfo.ts:16-20` |
| kaike（开课目录） | **仓内未找到**（`@example` 用 `.slice(0,3)`，无数量字面量） | `@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/kaikeInfo.ts:33-40` |

**注（源码里的硬上限/分页参数，非 `@example`，仅作定位）**：教学通知/竞赛 `parseNoticeListItems(..., 50)` 上限 50 条 [来源: @OLD/packages/nongyu-tool-jiaowu/core/extractor/noticeInfoExtractor.ts:46；competitionInfoExtractor.ts:21]；培养方案并行抓 2 页、固定参数串含 `p=80` [来源: @OLD/packages/nongyu-tool-jiaowu/core/jiaowu/planInfo.ts:19-20,121-124]。

**（非 `@example`，另注）**：底稿另记录一次性真实页面实测条数——教务首页教学通知 13 条、竞赛 13 条、教室搜索页 1 条、教师搜索页 3 条、教室课表详情页 `slots:7`；这些是一次性真实页面实测，样本已于 13:58 前清理、**不可复跑**，且**不是 `@example`**。[来源: @NEW/docs/tech/课程表与成绩查询可重写契约清单.md:36-38,219-223]

---

## 尚未查清

| # | 未查清项 | 依据 |
| --- | --- | --- |
| 1 | **真实页面表头文本 / 列数 / 行数**（`kbbanji.asp?title_id1=4`、`sear_ch_all.asp`、`jjihua.asp?title_id1=1`、`xuefen_2023.asp?title_id1=1`、`xuesheng.asp?title_id1=01`、`zytongbf.asp` 等）全部没有留存，需票 #33 真账号抓页面落 `testdata/` | `@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:433,437-440`；`@NEW/docs/tech/解析器施工级补充-4个待定解析器.md:487` |
| 2 | **前端回传 JSON 是否就是旧仓 `result` 原样**：新仓目前**没有任何接收前端回传 JSON 的上传端点**（G1），也没有前端解析实现；因此「回传体 = `{result, success, message?}` 还是只等于 `result`」「是否带 `success`」「字段名是否重命名」仓内均无任何代码可依据 | `@NEW/docs/tech/回传端点契约-现有能力底座.md:188,190,196`；`@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:36-38` |
| 3 | 本会话底稿里用于验证解析器的真实页面样本（`_temp_*`）**已清理、无法复跑**，故 A1–A5 假阳性结论是一次性、不可复跑的 | `@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:36-38` |
| 4 | 课表页是否「只返回当前学期」、`sear_ch_all.asp` 是否分页（若分页现实现会静默只取第一页） | `@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:434-435` |
| 5 | `rankInfo` 页面是「单人单行」还是可能多行；`progressInfo` 真实页面的列数与 `rowspan`/`colspan` 实际形态；`examTime` 是否真实存在「缺成分」样本 | `@NEW/docs/tech/解析器施工级补充-4个待定解析器.md:395-405,491-494` |
| 6 | `progress.progress` 字段**真实文本形态**仓内无样本（`parseProgressPercent` 假设是带一个数字的百分比/小数） | `@NEW/docs/tech/解析器施工级补充-4个待定解析器.md:493` |
| 7 | `classroomCourse` 的 `semester` 在真实页面取不到（实测 `""`），`@example` 里的 `2025-2026-2` 与现实页面不符，原因未查清 | `@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:219,436` |
| 8 | `teacherCourse` 的 `courseType`/`classId` 拆分规则只有源码注释与 `@example` 支撑，**教师课表详情页（`jiaoshikb_new.asp?bianhao=<id>`）的真实格内文本未跑过** | `@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:437` |
| 9 | `getPlanInfo` 是否曾成功过：`test.ts:146` 从首次提交起就是注释，**仓内无真实输出、无夹具、无断言**；`PLAN_FIXED_PARAMS` 各隐藏域在登录后页面上的具体 HTML 形态未知 | `@NEW/docs/tech/解析器施工级补充-4个待定解析器.md:117-120,139-141` |
| 10 | `getKaikeInfo` 的 URL 二义性（模块用 `kai.asp` 无 `title_id1`，extractor 内 `KAIKE_URL` 带 `title_id1=2` 且未被引用），且函数未导出，**仓内未找到它跑通的证据** | `@NEW/docs/tech/课程表与成绩查询可重写契约清单.md:439`；`@OLD/packages/nongyu-tool-jiaowu/core/jiaowu/kaikeInfo.ts:15`；`@OLD/packages/nongyu-tool-jiaowu/core/extractor/kaikeInfoExtractor.ts:114` |
| 11 | **各类数组的真实数量上下限**：仓内只有上表 `@example` 的手工片段与一次性实测，没有真实页面统计，不能据此定「合理数量范围」 | 本节「数量级线索」全表 |

---

（核查时点：2026-11-10；全部结论来自上表所列一手源码逐行读取与仓内既有笔记转引；未联网、未跑码、未抓页面、未执行 git 命令、未修改任何既有笔记、未改业务代码、未建 issue、未做任何设计决策。）
