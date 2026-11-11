# 教务夹具（testdata/jiaowu）

给**前端解析器**做回归用的 HTML 夹具。**Go 后端不消费这里的 HTML**——新架构是「前端解析、后端只校验回传 JSON」，所以 Go 侧只关心 JSON 样例（见 #41），不关心 HTML。

来源与口径由 [#38](https://github.com/NongyuSystem/nongyu-backend-plus/issues/38) 决议。

---

## 目录

```
testdata/jiaowu/
├── real-2026-11-11/     真实页面（脱敏后），6 个
│   ├── 1-课表.html
│   ├── 2-成绩.html
│   ├── 3-排名.html
│   ├── 4-考试.html
│   ├── 5-进度.html
│   └── 6-学籍.html
└── synthetic/           手工构造，非真实页面
    ├── notices.html         教学通知 / 竞赛通知区块
    ├── public-search.html   教室搜索+教室课表 / 教师搜索+教师课表+教师信息
    └── bad-shapes.html      坏样例：6 种失败形态
```

## 坏样例（`synthetic/bad-shapes.html`）

里面用 `<section>`-style 注释分成 6 种形态，**每种都对应一类「必须被判失败」的上游异常**：

| 形态 | 内容 | 期望行为 |
| --- | --- | --- |
| A | 登录超时页（200 但正文是 `alert('登录超时')`） | 判定 session 失效，绝不产出数据 |
| B | 空壳课表（有表头零数据行） | 产出空数组 + 明确「无数据」，不把提示文字当课程名 |
| C | 表头全被改掉 | 识别不出列 → 判定失败，绝不错位产出「看起来有值」的行 |
| D | 红字提示混在课表行里 | **不得**把「特别提示：…」当课程名（旧仓假阳性经典形态） |
| E | 成绩表有课名、成绩列全空 | 按 `courseName && score` 规则丢弃该行，产出空数组 |
| F | HTML 中途截断 | 不崩、判定失败 |

这份的用途是钉死「这几种页必须被判失败」——因为旧仓的 `success` 布尔已被实测证伪（红字会被当课程名、空壳页 `success:true`）。

## 哪些是真实页面，哪些是手工构造

| 文件 | 类型 | 采集/构造时间 | 证据 |
| --- | --- | --- | --- |
| `real-2026-11-11/*.html`（6 个） | **真实页面** | 2026-11-11，真账号登录后拓下 | [实测-教务6页真实HTML与表头.md](../../docs/tech/实测-教务6页真实HTML与表头.md) |
| `synthetic/notices.html` | **手工构造** | 2026-11-11 | 结构依 `docs/tech/课程表与成绩查询可重写契约清单.md` |
| `synthetic/public-search.html` | **手工构造** | 2026-11-11 | 结构依 `docs/tech/教务学生端功能与查询参数清单.md` §1.2 |

**用真实页面测「上游形态没变」；用手工夹具测「逻辑分支」（空表、缺列、多行、异常值）。**

## 脱敏口径

**结构保留、身份占位**。已执行：

| 字段 | 替换为 | 范围 |
| --- | --- | --- |
| 学号 | `20230XXXXX` | 6 个真实页面 |
| 姓名 | `张XX` | 6 个真实页面 |
| 手机号 | `1XXXXXXXXXX` | 学籍页 |
| 身份证号 | `<ID_REDACTED>` | 学籍页 |
| 籍贯城市 | `<CITY_REDACTED>` | 学籍页 |

**故意保留不改**：表头措辞、列序、行数、单元格空格、空行、内联 JS、数字 id、页码参数。
理由：夹具的价值在**结构与措辞**——改多了就测不出「表头一变会不会静默取空串」，而那是这类解析器最典型的失败方式。

## 编码：为什么这里是 UTF-8 而不是 GBK

**决策（#38 Q2）**：只保留 UTF-8，不额外存 GBK 原始字节。

**代价（已接受）**：前端若在解码环节写错（把 GBK 当 UTF-8 读），这份夹具**测不出来**——它已经是 UTF-8 了。

**补偿做法**：需要测 GBK 解码时，按下面「如何重采」重新抓原始字节，或用 `iconv -f UTF-8 -t GBK` 把现有夹具转回 GBK 作为输入（能测解码路径的存在性，但测不出上游编码是否变过）。

```bash
# 把现有 UTF-8 夹具转成 GBK 字节，用于测解码分支
iconv -f UTF-8 -t GBK testdata/jiaowu/real-2026-11-11/1-课表.html > /tmp/course-gbk.html
```

## 如何重采（上游改版后）

**要点：不要手写 curl 走教务三跳**，用仓库现成能力。完整踩坑记录见 [实测-教务6页真实HTML与表头.md](../../docs/tech/实测-教务6页真实HTML与表头.md)。

1. 取验证码：`GET https://auth.sicau.edu.cn/api/v1/captcha?appid=aea49a21-6b26-4e8f-bcfc-80e3de591c93`
   → 用 `schoolauth.DecryptYWTB(requestId, data)` 解密。**产物是 JSON 引号包裹的 data URI**（`"data:image/png;base64,..."`），要先剥引号再 base64 解码才是 PNG。
2. 人工识图验证码（**一次性**，重新取码会让旧码作废）。
3. `POST /api/v1/cas/login`，JSON `{appid, code, number, password, uuid}` → 拿 `go-admin-tgt`。
   **必须保留整套 cookie**，只带 tgt 换票会失败。
4. 用 `schoolauth.NewJiaowuSource(...).FetchPage(ctx, Credential{RawCookie: 整套cookie}, path)` 逐页取。
   它内部完成「换票 → 三跳建会话 → 取页面」。
   探针的 `platform.NewRealHTTPDoer` 必须显式给 `AllowedHosts: []string{"auth.sicau.edu.cn","jiaowu.sicau.edu.cn"}`。
5. 落盘后**先脱敏再提交**（见上表）。脱敏前不要 `git add`。

6 页路径：

| 页面 | 路径 |
| --- | --- |
| 课表 | `/xuesheng/gongxuan/gongxuan/kbbanji.asp?title_id1=4` |
| 成绩 | `/xuesheng/chengji/chengji/sear_ch_all.asp` |
| 排名 | `/xuesheng/chengji/chengji/zytongbf.asp` |
| 考试 | `/xuesheng/kao/kao/xuesheng.asp?title_id1=01` |
| 进度 | `/xuesheng/chengji/xdjd/xuefen_2023.asp?title_id1=1` |
| 学籍 | `/xuesheng/dangan/banji/bjiben.asp` |

## 已知的真实页面特征（写夹具断言时用）

来自 2026-11-11 实测，详见 [实测-教务6页真实HTML与表头.md](../../docs/tech/实测-教务6页真实HTML与表头.md)：

- **课表表头是 `任课教师`**（不是「任课老师」）。
- **课表页没有学期下拉框**，`?term=` 参数不存在；学期是纯文本「当前学期状态：2026-2027-1」。
- **成绩页没有分页**，一次返回全部。
- **成绩表头是 `最终成绩`**，且泛化的 `成绩` 出现次数更多 → 解析器关键字优先级必须把 `最终成绩` 放在 `成绩` 之前。
- **考试页有 `考场` / `座位` 两列**，超出旧契约，前端若不做映射会丢字段。

## 缺哪几类

真实页面只采了 6 类（需登录的那批）。**免登录的教室/教师搜索页本次未采真实版**，用 `synthetic/public-search.html` 代替。若将来需要真实版，按上面「如何重采」抓 `jshi_new.asp?bj=` 与 `jiaoshi_new.asp?xm=`（后者姓名需 GBK URL 编码）。
