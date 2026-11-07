# 架构落点草图：学校业务域（school/）与 WebVPN 能力的形状（prototype · 已按 2026-11-07 决议修订）

> **这是原型，不是实现。** 只有 Go 接口签名与包依赖关系，不含函数体、不编译。目的是让维护者看出「形状对不对」。
> 依据：[ADR-0001](../adr/0001-服务端代请求边界由新后端重写.md)（含 2026-11-07 路径一修订）、[#3 范围切分决议](https://github.com/NongyuSystem/nongyu-backend-plus/issues/3)、[#5 四条形状决议](https://github.com/NongyuSystem/nongyu-backend-plus/issues/5)、[README.md](../../README.md):42-48 五条架构约束。

## 一、落点决定（2026-11-07 维护者拍板）

1. **域名取宽泛的「学校」层级**：`internal/school/` 为父概念，教务为其中一个子域。后续可能接后勤、二课等，届时新增 `school/<子域>`，**不**再新开顶层业务包。
2. **`platform` 只新增 4 个接口**：`TicketStore` / `Cipher` / `HTTPDoer` / `Clock`，全是外部能力、不含业务语义。
3. **路由挂载点不变**：`server.New()` 仍只管 `/healthz`，由 `app.New()` 编排注册。
4. **票下发给客户端**（ADR 路径一），**且后端要定时错峰换票避免过期**——见第六节，此处有一个未决的技术前提。
5. **`Address` 与 `Fetch` 分立为两个入口**：谁执行 HTTP 一目了然。
6. **探测结果外置到 Store**；客户端每次进入上报一次，网络状态切换时再 `update` 一次；后端侧「校内/校外」启动时读配置即可。

## 二、包依赖图

```
                    cmd/server/main.go
                            │
                            ▼
                   internal/app  (composition root)
        ┌───────────────┬───────────────┬────────────────┬──────────────┐
        │               │               │                │              │
        ▼               ▼               ▼                ▼              ▼
  internal/config  internal/server  internal/school  internal/platform
                                    （学校业务域）      （接口定义）
                                         │                 ▲
                                    ┌────┴────┐            │
                                    ▼         ▼            │
                                jiaowu/   （将来）        │
                                （教务）   houlv/erke/     │
                                    │                    │
                                    └────依赖接口─────────┘
```

**关键变化**：`school` 是父包，`school/jiaowu` 是子域。父包只放**跨子域共享的东西**（如 `Ticket`、`Target`、错误码），子域之间**仍不互相 import**（README 第 1 条）。

**约束核对**：`school/*` → `platform` 单向；`school/*` 不 import `server`/`app`；`server` 不 import `school`。

## 三、`internal/platform` 新增接口（4 个）

```go
type Store interface {                                   // 已有
    Get(ctx context.Context, key string) ([]byte, error)
}

type TicketStore interface {                             // 新增 1
    Put(ctx context.Context, userID string, sealed []byte, expiresAt time.Time) error
    Get(ctx context.Context, userID string) (sealed []byte, expiresAt time.Time, err error)
    Delete(ctx context.Context, userID string) error
    // ListExpiringSoon 供「定时错峰换票」调度器挑选候选（见第六节）
    ListExpiringSoon(ctx context.Context, before time.Time, limit int) ([]TicketRef, error)
}

type Cipher interface {                                  // 新增 2
    Seal(plaintext []byte) ([]byte, error)
    Open(sealed []byte) ([]byte, error)
}

type HTTPDoer interface {                                // 新增 3
    // 唯一出站闸口：白名单校验、超时、不跟随真站 302 都在实现层
    Do(ctx context.Context, req *http.Request) (*http.Response, error)
}

type Clock interface { Now() time.Time }                 // 新增 4
```

> `ListExpiringSoon` 是本次决议新增的方法，专为「错峰换票」服务——没有它，调度器只能全表扫。

## 四、`internal/school` 业务域结构

```go
package school   // 父包：只放跨子域共享的类型，不放逻辑

// 逻辑目标名：调用方只能传它，不能传 URL（ADR 第 1 条的结构性保证）
type TargetName string

// 一份已经校验过的目标描述
type Target struct {
    Name   TargetName
    Host   string   // jiaowu.sicau.edu.cn / auth.sicau.edu.cn / webvpn.sicau.edu.cn
    Direct *url.URL // 直连形态
    Vpn    *url.URL // 经 WebVPN 网关的形态
}

// 错误码：跨子域统一，避免每个子域自造一套
type Code string
const (
    CodeTicketExpired     Code = "ticket_expired"      // 302 → webvpn/login
    CodeSessionExpired    Code = "session_expired"     // 正文「登录超时」
    CodeCampusUnreachable Code = "campus_unreachable"  // 直连探测失败
    CodeBlockedRedirect   Code = "blocked_redirect"    // 302 指向真站，已按 ADR 停止
    CodeTargetDenied      Code = "target_denied"       // 目标名不在白名单
)

type Error struct{ Code Code; msg string }
```

```go
package jiaowu   // 子域：教务

func Register(r gin.IRouter, svc *Service, auth AuthMiddleware)

type Service struct {
    targets  TargetResolver      // school 包里的契约实现，只认逻辑名
    codec    EncodeCodec         // 纯函数：AES-128-CFB 编码
    doer     platform.HTTPDoer
    tickets  platform.TicketStore
    cipher   platform.Cipher
    clock    platform.Clock
    presence Presence            // 校内/校外状态（见第五节）
}

// 取验证码（后端去学校取，返回图给 App 展示）
func (s *Service) Captcha(ctx context.Context, userID string) (Captcha, error)

// 换票：由后端调 cas/login 完成；成功后票据留存并**下发给本人**（ADR 第 3 条路径一）
func (s *Service) Login(ctx context.Context, userID string, in LoginInput) (LoginResult, error)
// type LoginResult struct { Ticket string; ExpiresAt time.Time; NeedJiaowuCaptcha bool }

// 客户端直连用：只返回地址（+ 可选票据由 Login 单独给过）
func (s *Service) Address(ctx context.Context, userID string, t TargetName, q Query) (string, error)

// 后端代取：换票、续期、失效判定、任何需后端执行的请求
func (s *Service) Fetch(ctx context.Context, userID string, t TargetName, q Query) (Payload, error)

type EncodeCodec interface { Encode(rawURL string) (string, error) }
```

> `Address` 与 `Fetch` **分立**（本次决议）。`Address` 内部仍走同一套 `TargetResolver + EncodeCodec`，不会绕过白名单。

## 五、校内/校外：两条来源，一个模型

```go
package school

// 后端视角：启动时从配置读，进程内不变
type BackendPresence struct{ Mode Mode } // ModeDirect | ModeVPN

// 客户端视角：每次进入上报、网络切换再 update 一次
type ClientPresence struct {
    UserID string
    Mode   Mode      // 用户当前在校园网内 / 外
    At     time.Time // 上报时间，用于判断陈旧
}

// Presence 是二者合并后的判定入口
type Presence interface {
    // 依据「客户端上报 + 后端配置」决定该用户这一次该走直连还是 WebVPN
    Decide(ctx context.Context, userID string) (Mode, error)
    Report(ctx context.Context, p ClientPresence) error   // 客户端上报入口
}
```

**实现要点（写进后续 Spec，不在本原型）**：

- 后端侧模式**启动时读配置**，不做周期性自探测（省一次出站）。
- 客户端上报存 `platform.Store`（本次决议：外置）。
- 客户端每次进入上报一次；网络状态切换时 `POST /school/presence` 更新。**陈旧判定**：超过某时长未更新则视为不可信、退回后端配置。

## 六、定时错峰换票：形状给了，但有个硬前提没解决

```go
// 调度器属于 jiaowu 子域，由 app 启动时拉起
type RenewScheduler struct {
    tickets platform.TicketStore
    clock   platform.Clock
    svc     *Service
}

// 每轮挑「即将过期」的票，按用户散列打散到不同时刻，避免同一秒集中换票
func (r *RenewScheduler) Tick(ctx context.Context) error
func (r *RenewScheduler) ListCandidates(ctx context.Context) ([]TicketRef, error)
```

**但这里有一个必须先解决的问题，我不替你定**：

- 「即将过期」需要**知道 TTL**，而仓内一手材料明确：VPN ticket 的 TTL 与空闲超时**从未被公布**（[Go后端承载WebVPN的硬约束.md](Go后端承载WebVPN的硬约束.md) §5.1），旧规格还写着「不做保活轮询」（[WebVPN传输适配.md:24](D:/IT/program/web-program/nongyu-system/docs/nongyu-rn-app/specs/WebVPN传输适配.md#L24)）。
- 所以「定时」目前只能有两种解释，**需要你二选一**（已在 map 上记为 fog）：
  - **(i) 观测驱动**：不猜 TTL，改为「记下每次换票成功的时刻 + 首次失败的时刻」，由失败反馈反推保守周期。不违背「不探测未公开超时」，但前期会有一段时间摸黑。
  - **(ii) 经验固定值**：先用一个保守的固定周期（如远小于已知的最短会话寿命），接受「可能偏保守、多换几次」的风险。实现简单，但频繁换票本身可能触发风控。
- **不论哪种**，都要先确认「换票是否要重新输图形验证码」——要的话，定时换票会**打断用户**，整个方案的前提就变了。这是本原型最需要你先回答的技术问题。

## 七、错误码如何表达「不跟随真站」

`CodeBlockedRedirect` 由 `platform.HTTPDoer` 实现层在发现 `Location` 指向真站时立即返回；业务域只负责转述。**全系统只有一个强制点**。

## 八、`app` 装配层示意（仍是这几行）

```go
func New(cfg config.Config, logger *slog.Logger) (*App, error) {
    store    := platform.NewMockStore()
    tickets  := platform.NewMockTicketStore()
    cipher   := platform.NewAESGCM(cfg.TicketKey)
    doer     := platform.NewGuardedDoer(cfg.Allowlist, cfg.HTTPTimeout)
    clock    := platform.NewClock()

    presence := school.NewPresence(cfg.BackendMode, store)   // 后端配置 + 客户端上报
    svc      := jiaowu.New(/* 注入 targets/codec/doer/tickets/cipher/clock/presence */)
    engine   := server.New()
    jiaowu.Register(engine, svc, authMiddleware)

    renew    := jiaowu.NewRenewScheduler(tickets, clock, svc)
    go renew.Run(ctx)      // 生命周期交给 app 管
}
```

## 九、本原型未做 / 待你继续判断

1. **换票周期怎么定**——见第六节，这是**阻塞 Spec 收口**的前置问题。
2. **定时换票是否需要重新输验证码**——若需要，方案前提变化。
3. `school/` 父包里除了 `Target` / `Code` / `Presence`，是否还要放别的东西——我倾向于「先放这三样，不够再加」，避免父包变垃圾场（README 第 5 条）。
4. `TicketStore.ListExpiringSoon` 的扫描成本：票量到万级时是否需要索引，留待 Store 实现票决定。

## 十、未验证项

- 签名未经编译。
- 「错峰」的散列策略（按 userID 还是按到期时间分桶）未定。
- 后勤/二课等未来子域的接口形状**不在本原型范围**，不预判。
