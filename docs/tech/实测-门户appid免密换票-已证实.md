# 实测：门户 appid 免密换票（已证实）

状态：**已证实（2026-11-08）**。本篇记录判据与复现口径，**不记录任何凭据值**（cookie / tgt / st 一律 `<REDACTED>`）。

## 结论

门户 appid `aea49a21-6b26-4e8f-bcfc-80e3de591c93` 的免密换票**成立**：

```
GET https://auth.sicau.edu.cn/api/v1/cas/login?appid=aea49a21-6b26-4e8f-bcfc-80e3de591c93
（带真实浏览器会话 cookie）
→ HTTP 200  {"code":200,"msg":"登录成功","data":"<加密>","requestId":"<略>"}
```

`data` 按平台算法解密后得到完整换票载荷，字段为：

```
callbackUrl, tgt, st, user, users, uuid
```

| 验收判据（Spec #10 第 8 节） | 结果 |
| --- | --- |
| `code == 200` | 通过 |
| 解密 `data` 含 `st` | 通过（st 长度 47） |
| `callbackUrl` 主机名为 `webvpn.sicau.edu.cn` | 通过 |
| 解密出用户标识 | 通过 |

## 关键事实（对实现的影响）

1. **必须带整套会话 cookie，单枚 `go-admin-tgt` 不够。**
   实测只用 `go-admin-tgt` 单独请求同一端点 → `code=500, msg=未登录`；改用真实浏览器会话（同域多枚 cookie 齐备）→ `code=200, 登录成功`。
   结论：后端若要复用「已登录态」，存的不该只是单枚 TGT，而是能复现登录态的那组 cookie（或让用户重新走主路径）。这直接约束 `TicketStore` 里存什么。

2. **响应体是加密信封。** `data` = `base64(IV(16B) + AES-128-CBC-NoPadding(ct))`，`key = MD5(requestId + ":sicau-cas")` 转 16 字节二进制，尾部自定义 padding（末字节 N 表示去掉 N 字节）。

3. **命令行 `curl`/`Invoke-WebRequest` 在本机 TLS 环境可能失败**，本次改用浏览器 + 页面内取 JSON，再在内存里解密，全程未落盘凭据。

## 复现步骤（不落凭据）

1. 浏览器登录 e 认证后，打开上述 `cas/login` 端点，页面直接返回 JSON。
2. 取 `requestId` 与 `data`，在内存中按平台算法解密（见知识库 `sicau-sso/川农-e-认证响应体-AES-解密` 笔记）。
3. 只核对上表的四项判据，**不要记录或提交解密出的 `st` / `tgt`**。

## 对 Spec 的处置

- Spec #10 第 8 节验收清单里「门户 appid 免密换票实测项 —— pending」**改为已通过**。
- `ACCEL_ENABLED` 的语义随之明确：能力成立，但**默认仍关闭**，因为第 1 条事实意味着要能复现登录态才谈得上自动续期；开关放开的时机由部署方决定。

## 未做 / 边界

- 未验证长期可用性（10 天窗口内是否稳定、会话被踢后表现），属运行期观测。
- 未验证该端点对账号与 IP 的限流维度（学校「15 秒 / 1 小时」限制按什么施加仍无一手证据）。
- 本结论只覆盖**门户 appid**，不跨 appid 外推。
