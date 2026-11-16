# 学校二课夹具（testdata/secondclass）

给「请求配方 + 记录」契约做回归用的**真实响应**夹具。

采集时间：**2026-11-16**
采集账号：四川农业大学真实学生账号（本人授权）
采集工具：`curl`，串行请求，间隔 2~3 秒

**脱敏口径**：结构、字段名、字段类型、null 位置、空数组**全部保留**；姓名/学号/手机号替换为占位符；**`x-access-token` 从未写入任何文件**。

---

## 目录

```
testdata/secondclass/
├── README.md              本文件
└── real-2026-11-16/
    ├── 1-学期学分.json     getSecondActivityCredit（单对象）
    ├── 2-学分明细.json     getCreditRecordDetail（数组）
    └── 3-未登录失败.json   无 token 调读接口
```

---

## 一、最重要的发现：响应信封与离线样本的描述不符

### 1.1 字段名是 `content`，不是 `data`

**全部实测响应都用 `content`**：

```json
{"code":"0","message":"成功","content":{...}}
{"code":"0","message":"成功","content":[...]}
{"code":6,"message":"用户未登录"}
```

离线 APK 的 VO 声明里是 `data`，**实际返回是 `content`**。

> 这直接影响 `#61` 定的「数据副本」——它的入参字段名、以及 schema 的顶层结构，都要按 `content` 写。

### 1.2 `code` 的类型**不稳定**（同一个平台内）

| 场景 | 实际返回 | 类型 |
| --- | --- | --- |
| 登录成功 | `{"code":"0"}` | **字符串** |
| 读接口成功 | `{"code":"0"}` | **字符串** |
| 未登录 | `{"code":6}` | **数字** |
| 用户不存在 | `{"code":"2"}` | 字符串 |
| 参数为空 | `{"code":"10"}` | 字符串 |
| 手机格式错误 | `{"code":"104"}` | 字符串 |

**结论**：客户端与任何消费方都必须把 `code` 当**「字符串或数字」双类型**处理。

> 离线 VO 里 `BaseResponse` 声明 `int code`、`RespVO` 声明 `String code`——**两个声明都是对的，也都不完整**。真实情况是同一接口可能给不同类型。

### 1.3 失败时 HTTP 仍是 200

所有失败（未登录、用户不存在、参数为空）**HTTP 状态码都是 200**，业务码在 body 里。

---

## 二、真实响应样例（结构逐字保留）

### 2.1 `getSecondActivityCredit` → 单对象

路径：`POST /act/credit/v1.0.0/getSecondActivityCredit`
参数：仅 header `x-access-token`

```json
{
  "code": "0", "message": "成功",
  "content": {
    "unitName": "二课学时",
    "totalTitle": "累计二课学时", "totalHours": "39.0",
    "thisTermTitle": "本学年二课学时", "thisTermHours": "0.0",
    "ratio": [
      {"parentValidCredit": 12.5, "parentTypeName": "美育", "parentTypeId": "843d...", "scale": "32.05%"},
      {"parentValidCredit": 10.5, "parentTypeName": "德育", "parentTypeId": "9ee4...", "scale": "26.92%"}
    ]
  }
}
```

**形状**：`content` 是**单对象**。`ratio` 是数组，元素里 `parentValidCredit` 是**数字**（12.5），`scale` 是**带百分号的字符串**（"32.05%"）。

### 2.2 `getCreditRecordDetail` → 数组（两层嵌套）

路径：`POST /act/integrate/v1.0.0/getCreditRecordDetail`

```json
{
  "code": "0", "message": "成功",
  "content": [
    {
      "firstTypeName": "智育", "firstTypeId": "94c0...", "totalScore": 11.0,
      "content": [
        {"creditRecordId": "58918212", "eventName": "【食】…活动", "validCredit": 0.5,
         "display": 0, "fistActTypeId": "94c0...", "firstActTypeName": "智育", "grantTime": "2025-11-18"}
      ]
    }
  ]
}
```

**形状**：`content` 是**数组**，每个元素里**又有一个 `content` 数组**——同名嵌套两层。

**⚠️ 注意拼写**：`fistActTypeId`（**不是** `firstActTypeId`，缺 `r`），而旁边字段又叫 `firstActTypeName`。**上游拼写错误必须原样保留**，schema 不能"修正"它。

**类型不一致**：`totalScore` 是**数字** 11.0，而 `validCredit` 是**数字** 0.5——但离线 VO 声明 `totalScore` 是 `String`。**以实测为准**。

### 2.3 未登录失败

```json
{"code": 6, "message": "用户未登录"}
```

**没有 `content` 字段**——失败时不返回 `content`。

---

## 三、登录接口（会话建立）

路径：**`POST /user/login/v1.0.0/snoLogin`**

> ⚠️ **与离线样本不符**：APK 记录的是 `login/v1.0.0/snoLogin`，**实际多了 `user/` 前缀**。

参数（`FormUrlEncoded`）：

| 参数 | 说明 | 示例 |
| --- | --- | --- |
| `sid` | **学校 UUID，不是数字编号** | `f1c97a0e81c24e98adb1ebdadca0699b`（四川农业大学） |
| `loginName` | 学号 | 占位符 |
| `password` | 明文密码 | 占位符 |

**成功响应**：

```json
{"code":"0","message":"成功","content":{
  "uid": 21418112, "token": "<32 位>", "loginName": "...", "password": "",
  "sid": "f1c9...", "schoolName": "四川农业大学", "cid": "...", "cname": "信息工程学院",
  "enrollmentYear": "2025", "name": "...", "realName": "...", "mobile": "...",
  "encryptMobile": "...", "desensitizeMobile": "...", "sex": "1", "userType": "1013"
}}
```

**要点**：
- `uid` 是**数字** 21418112
- `password` 字段**回显为空字符串**（不是 null，也不是省略）
- `nameZhSpelling` / `personalitySignature` / `photo` / `nativePlace` 等为 **null**

### 3.1 学校列表（拿 `sid` 用）

路径：`POST /user/login/v1.0.0/getSchoolList`（无需 token）

```json
{"code":"0","message":"成功","content":[
  {"id":"f1c97a0e81c24e98adb1ebdadca0699b","name":"四川农业大学","nameZhLetter":"SCNYDX","canRegister":"0"}
]}
```

`id` 就是 `snoLogin` 要的 `sid`。

---

## 四、必需的请求头（7 个）

离线样本只说「有 `pu-version` 等设备头」，实测**全部必需**——少任何一个，服务端会返回误导性错误（如把「手机格式错误」报成「用户不存在」）。

```
pu-version: 12.10.0.10010599
client-dev-brand: <品牌>
client-dev-type: <机型>
client-os-type: android
client-os-version: 29
client-long: 0
client-lat: 0
User-Agent: okhttp/3.12.0
```

来源：`dump/net_final/tiangong.com.pu.common.network.Api$1.txt`（拦截器 IL）

---

## 五、本夹具未覆盖（仍待真账号补）

| 项 | 状态 |
| --- | --- |
| 空列表时 `content` 是 `[]` 还是 `null` | **未测**（当前账号有数据） |
| `null` 在哪些字段出现 | 部分可见（登录响应有 5 个 null） |
| 分页响应的总数字段名 | **未测** |
| `code=5`（token 失效）的真实形态 | **未测**（试的是未登录，返回 6） |
| 写操作成功/重复提交 | **未测** |
| 活动详情含部落名/学院名 | 离线已知字段存在，**运行时未验证** |

---

## 六、复现方式

```bash
# 1) 拿 sid
curl -X POST 'https://jk.sicau.edu.cn:443/user/login/v1.0.0/getSchoolList' \
  -H 'pu-version: 12.10.0.10010599' [其余 6 个头]

# 2) 登录拿 token
curl -X POST 'https://jk.sicau.edu.cn:443/user/login/v1.0.0/snoLogin' \
  -H 'pu-version: 12.10.0.10010599' [其余 6 个头] \
  -d 'sid=<学校UUID>' -d 'loginName=<学号>' -d 'password=<密码>'

# 3) 用 token 读
curl -X POST 'https://jk.sicau.edu.cn:443/act/credit/v1.0.0/getSecondActivityCredit' \
  -H 'x-access-token: <token>' -H 'pu-version: ...' [其余 6 个头]
```

**纪律**：串行、间隔 ≥2 秒、不做压测、不批量爬取。
