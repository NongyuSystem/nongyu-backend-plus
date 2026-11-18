# nongyu-backend-plus

新农屿后端的 Go 服务。**独立于旧农屿（`nongyu-system` monorepo）的全新仓库**，不复用旧后端代码、表结构与部署链路。本仓承载**多个业务 effort**（WebVPN 能力重写、身份域/平台底座、六类常见功能），每个 effort 各自有 PRD / Spec，见 [`docs/specs/`](docs/specs/)。

## 状态

已承载身份、内容、消息、埋点、皮肤、教务、二课、校内状态等多个业务域，
各域自带 handler / service / 仓储接口；`migrations/` 下的 SQL 是建表形状的**唯一权威源**。
`GET /healthz` 只报进程存活，依赖状态看 `GET /status`。

## 技术选型

| 项 | 值 |
| --- | --- |
| 语言 | Go 1.26 |
| HTTP 框架 | Gin |
| 架构 | **模块化单体**（一个仓库 / 一个 module / 一个进程，内部按业务域分包 + 域内 `store/` 分层） |
| 配置 | 环境变量（见 `.env.example`） |
| 存储 | 按 `DATABASE_URL` / `REDIS_ADDR` 等配置切换：配了走 Postgres / Redis 实现，未配退化为进程内存（**显式告警**，不静默降级）；切换点集中在 `internal/app/repos.go` |

## 目录结构

```
nongyu-backend-plus/
├── cmd/server/            # 唯一入口：装配 + 启动 + 优雅关停
├── internal/
│   ├── app/               # 装配层（composition root）：依赖注入只在这里
│   │   └── notify/        # 跨域编排单元（通知派生）
│   ├── config/            # 环境变量读取与校验（见 .env.example，37 变量）
│   ├── server/            # Gin 适配层：路由、中间件、/healthz、/status
│   ├── architecture/      # import 方向守卫测试（防止分层再次倒置）
│   ├── platform/          # 最底层通用能力，零业务域依赖
│   │   ├── model/         # 零依赖叶子包：共享值类型（User/Session/TicketRef）+ 哨兵错误
│   │   ├── pg/            # Postgres：迁移、测试 schema、身份仓储、DDL 工具、学工档案落库
│   │   ├── redis/         # Redis：票据存储、会话热副本、连接选项
│   │   ├── cache/         # 通用缓存：穿透/雪崩/击穿/降级 + 熔断 + 幂等锁
│   │   ├── mail/          # 邮件投递、验证码存储、审计落库
│   │   ├── objectstore/   # 对象存储（MinIO）实现
│   │   └── （根）         # 契约接口、类型别名转发、AES/时钟/HTTPDoer/Mock 替身
│   └── <业务域>/          # content/{post,announcement}・message・track・identity・school
│       └── store/         # 该域的 Postgres / 内存仓储实现（域内固定分层）
├── migrations/            # schema 变更（单向有序；启动时幂等执行）
│   └── migrations.go      # //go:embed *.sql，权威 DDL 的唯一来源
├── deploy/                # 部署示例（Dockerfile / systemd / compose）
├── docs/
│   ├── specs/             # 规格文档（SDD：PRD → Spec → 编码）
│   ├── tech/              # 技术方案与决策记录
│   └── plans/             # 实施计划（需要时）
├── go.mod / go.sum
├── Makefile / scripts/dev-start.sh   # 常用命令；本机启动脚本（读 .env + 探测）
├── .env.example           # 配置模板（37 变量全量，真实值不入库）
└── README.md
```

> 目录分层判据（2026-11 结构整改后）：
> - **域根**：实体 + 仓储接口 + 业务规则，不含 SQL、不含 `gin`；
> - **`<域>/store/`**：该域的 Postgres / 内存实现，可 import 域根，域根不能反向 import 它；
> - **`app/`**：只做「构造具体实现 → 注入接口」的接线，不写业务判断。

## 架构约束（长期不可破）

1. **业务域之间不互相 import**，协作统一走 `internal/app` 装配层编排。
2. **业务域只依赖 `internal/platform` 定义的接口**，不直接 import 数据库驱动、S3 SDK 等具体实现。
3. **进程无状态**：状态全部外置（DB / 缓存 / 对象存储），以便水平扩副本。
4. `internal/<业务域>` 内部自带 handler / service / repo 接口；**存储实现放该域的 `store/` 子包**，
   不得放在 `internal/platform` 下——platform 是最底层，不能反向依赖业务域。
   （历史上曾放 `platform/pgstore`，已按域拆走；`internal/architecture/` 有守卫测试锁住这条。）
5. 不提前建 `pkg/utils`、`common` 这类垃圾场包；确有跨项目复用需求时再开 `pkg/`。

## 本地运行

```bash
# 依赖
go mod tidy

# 启动（默认 :8080）
make run
# 或
go run ./cmd/server

# 验证
curl http://127.0.0.1:8080/healthz
# 期望：{"status":"ok"}
```

## 常用命令

```bash
make build   # 编译到 bin/server
make test    # 单元测试
make vet     # 静态检查
make fmt     # gofmt
make tidy    # 整理依赖
make clean   # 清理产物
```

## 开发流程（SDD）

本仓库遵循规格驱动开发：**PRD → Spec → 技术方案（按需）→ 实施计划 → 编码 → 人工 Review**。
骨架期只有 README 作为依据；业务接口开发前须先补 PRD 与 `docs/specs/` 下的规格，禁止无规格直接写业务代码。`docs/specs/` 下按 effort 分文件；本仓当前并行承载多个 effort，各自有 PRD。

## 与旧农屿的关系

新农屿基本不复用以前的后端实现。`apps/nongyu-go-track-server` 是旧农屿已下线的 Go 埋点服务，仅作历史对照，**不是本仓库的现行源码或依赖来源**。
