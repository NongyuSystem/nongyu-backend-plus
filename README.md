# nongyu-backend-plus

新农屿后端的 Go 服务。**独立于旧农屿（`nongyu-system` monorepo）的全新仓库**，不复用旧后端代码、表结构与部署链路。本仓承载**多个业务 effort**（WebVPN 能力重写、身份域/平台底座、六类常见功能），每个 effort 各自有 PRD / Spec，见 [`docs/specs/`](docs/specs/)。

## 状态

骨架阶段。仅提供可编译、可启动、可优雅关停的最小工程结构 + `GET /healthz`，**不含任何业务逻辑、不连接数据库**。

## 技术选型

| 项 | 值 |
| --- | --- |
| 语言 | Go 1.26 |
| HTTP 框架 | Gin |
| 架构 | **模块化单体**（一个仓库 / 一个 module / 一个进程，内部按业务能力强分包） |
| 配置 | 环境变量（见 `.env.example`） |
| 存储 | 骨架期用 `internal/platform.MockStore`；数据库层就绪后替换实现，业务域零改动 |

## 目录结构

```
nongyu-backend-plus/
├── cmd/server/            # 唯一入口：装配 + 启动 + 优雅关停
├── internal/
│   ├── app/               # 装配层（composition root）：依赖注入只发生在这里
│   ├── config/            # 环境变量读取与校验
│   ├── server/            # Gin 适配层：路由、中间件、/healthz
│   ├── platform/          # 外部能力接缝：Store 接口 + Mock 实现
│   └── <业务域>/          # 待 PRD/Spec 到位后按业务能力新增（如 skin/、config/、auth/）
├── migrations/            # schema 变更（单向有序）
├── deploy/                # 部署示例（Dockerfile / systemd / compose）
├── docs/
│   ├── specs/             # 规格文档（SDD：PRD → Spec → 编码）
│   ├── tech/              # 技术方案（需要时）
│   └── plans/             # 实施计划（需要时）
├── go.mod / go.sum
├── Makefile
├── .env.example           # 配置模板（真实值不入库）
└── README.md
```

## 架构约束（长期不可破）

1. **业务域之间不互相 import**，协作统一走 `internal/app` 装配层编排。
2. **业务域只依赖 `internal/platform` 定义的接口**，不直接 import 数据库驱动、S3 SDK 等具体实现。
3. **进程无状态**：状态全部外置（DB / 缓存 / 对象存储），以便水平扩副本。
4. `internal/<业务域>` 内部自带 handler / service / repo 接口；存储实现放 `internal/platform` 或其子包。
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
