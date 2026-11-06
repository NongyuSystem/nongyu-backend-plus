# 新农屿后端（nongyu-backend-plus）

本仓是独立于旧农屿 monorepo 的 Go 后端骨架。本表只收「把旧农屿 WebVPN 能力功能重写进新后端」这一 effort 里容易混淆的领域词；实现细节（编码算法、appid、Cookie 名等）一律留在 `docs/tech/` 的底稿，不进本表。

## Language

### WebVPN

**WebVPN**:
农屿客户端侧把校内目标的绝对地址改写为 WebVPN 编码地址、并在直连与代理传输之间切换的能力。本仓裸用「WebVPN」即指此义。
_Avoid_: WebVPN 传输适配、传输适配层

**WebVPN 网关**:
学校侧由网瑞达 wengine 提供的 HTTPS 反向代理，把 WebVPN 编码地址还原为校内目标请求；不属于农屿代码。
_Avoid_: WebVPN、网关

### 认证

**CAS 换票**:
川农 e 认证下以图形验证码换取目标应用登录票据的过程；分 WebVPN 门户与教务两路，两路的 appid 与 service 不可混用。
_Avoid_: SSO、单点登录、登录

**服务端代请求**:
新农屿后端进程代替客户端向校内目标发起的 HTTP 请求。
_Avoid_: 任意 URL 代理、开放代理
