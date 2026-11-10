# 川农 CAS 真实链路验收：单凭 go-admin-tgt 可换票并直接带回身份

> 状态：**已实测证实**（2026-11-09）。ES 后端不可达，本条目暂存仓外文件，待知识库恢复后补录。
> **本文件不含任何凭据值**（验证码 / tgt / st / cookie 全部省略）。

## 结论

**`go-admin-tgt` 是万能凭据**：客户端在学校统一身份认证登录后，后端只拿这一张 cookie 就能自己换任何 appid 的票、取到教务数据。**不需要客户端再交 ywtb Bearer token。**

## 实测链路（每一步都是真站真实请求）

| 步骤 | 请求 | 结果 |
| --- | --- | --- |
| 1 取验证码 | `GET https://auth.sicau.edu.cn/api/v1/captcha?appid=<appid>` | 200，`{code, data(加密), id(uuid), requestId}` |
| 2 解密验证码 | 仓库 `DecryptYWTB(requestId, data)` | 240×80 PNG，正常可读 |
| 3 提交登录 | `POST /api/v1/cas/login`，JSON `{appid, code, number, password, uuid}` | **`{"code":200,"msg":"登录成功"}`** |
| 4 拿 tgt | 响应 `Set-Cookie: go-admin-tgt=...` | 57 字符 |
| 5 换票 | `GET /api/v1/cas/login?appid=<教务appid>`，Cookie **只带 go-admin-tgt** | **`code=200`** |
| 6 解密换票响应 | `DecryptYWTB` | `{callbackUrl, tgt, st, user, users, uuid}` |
| 7 教务三跳建会话 | 仓库 `JiaowuSource.establishSession` | 收到 `ASPSESSIONIDCETSSCTS` |
| 8 取学籍页 | `GET /jiaoshi/.../xuesheng/dangan/banji/bjiben.asp` | 解析成功 |

第 8 步解析出的真实字段：

```
Major     = "数据科学与大数据技术"
ClassName = "大数据202503"
Grade     = "2025"
```

换票响应的 `user` 里还直接带：

```
number = 学号
name   = 姓名
phone  = 手机
group[].alias = "本科生"
```

## 关键认知

1. **一次登录全站可达成立**。tgt 换票由后端完成，客户端不需要参与后续任何域的登录。
2. **取身份不必调 ywtb `getUserInfo`**。换票响应本身已含学号/姓名/手机/组别，一次请求两用（st 给教务 + user 给身份）。
3. **学院与身份证不在换票响应里**，在教务档案页（`bjiben.asp` 或相邻页），需另行确认字段与表头。

## 复用要点（照做即可）

- 验证码靠**人识别**：把图交给用户，用 `ask_user_question` 让他答，**不要自己猜**（我第一次读错，直接 500「验证码错误」）。
- 取码**不绑定 cookie**（响应无 Set-Cookie），只靠 `uuid` 关联 → 取码与登录可以是两次独立请求。
- 教务三跳**不要手写 curl**：必须每跳都带上已收到的 cookie，漏了就 302 到底。直接用仓库 `JiaowuSource.FetchAcademic`。
- 临时探针放 `tools/`（`internal/` 下非 `_test.go` 会污染生产包编译），用完删除。
- PowerShell 的 `Out-File -Encoding utf8` 在 PS5 加 BOM，会让 Go 的 `json.Unmarshal` 静默失败；用 `[System.IO.File]::WriteAllText(path, text, UTF8Encoding($false))`。

## 由本次验收发现的缺陷

`internal/school/schoolauth/ywtb.go` 的 `YWTBSource.FetchIdentity` 第一行硬要求 `cred.BearerToken`，而真实链路里客户端只有 tgt → `POST /identity/login` 必然 `bad_credentials` / 401。

已开 issue #28 承接（改为「用 tgt 换票、从换票响应读 user」，并把 Bearer 降级为可选）。
