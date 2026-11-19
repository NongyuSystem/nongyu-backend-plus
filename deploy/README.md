# 部署说明（生产）

后端是独立 Git 仓库（嵌套在 monorepo 的 `apps/` 下，由自身仓库管理）。
**一个仓库一个 CI**：`.github/workflows/deploy.yml` 只负责 backend 的构建与部署，
不碰 monorepo 父仓库，也不管依赖镜像。

## 架构

```
push main（本仓库）
  → GitHub Actions（境外 runner，只做代码相关）
      → make check（vet + 格式 + 全测试）
      → docker build -f deploy/Dockerfile
      → docker save → scp 到服务器
  → 服务器 /opt/nongyu/deploy.sh
      → docker load → 标记 latest → compose up 只重建 server
      → 轮询 /healthz → 失败自动回滚
```

**服务器不需要访问 GitHub / Docker Hub**（实测均超时），只需能被 SSH 连入。
依赖镜像在服务器初始化时一次性备好，CI 一律不碰。

## 服务器初始化（一次性，已完成于 103.236.88.53）

1. 依赖镜像：`postgres:18-alpine`、`redis:7-alpine`、`minio/minio:latest`
   由本地 `docker save` 成 tar → scp → 服务器 `docker load`。
   （任何镜像源在该服务器都拉不动 blob，必须离线导入。）
2. 目录：`/opt/nongyu`（配置与脚本）、`/var/lib/nongyu/{postgres,redis,minio}`（数据）。
3. 配置：`/opt/nongyu/.env`（600 权限，含密钥；**不入仓库**），
   `docker-compose.prod.yml` 由本文件同步。
   - Postgres 18 注意：数据挂到 `/var/lib/postgresql`，不是 `/data`（18+ 的布局要求）。
   - `.env` 里除了应用配置项，还要有 `DB_PASS` / `REDIS_PASS` / `MINIO_PASS`
     三个 compose 变量（docker compose 从同目录 `.env` 读取 `${...}`）。
4. 依赖容器：`docker compose -f docker-compose.prod.yml up -d postgres redis minio`，
   三者健康后再让 CI 部署应用。

## GitHub Secrets（本仓库）

| Secret | 说明 |
| --- | --- |
| `SERVER_HOST` / `SERVER_PORT` / `SERVER_USER` | SSH 目标（端口 44400、用户 root） |
| `SSH_PRIVATE_KEY` | 部署私钥（ed25519，公钥已装服务器 `authorized_keys`） |
| `AUTH_KEY` / `TICKET_KEY` | 生产密钥（当前仅备用，应用读服务器 `.env`） |

## 回滚

`deploy.sh` 会在重建前记录当前 server 容器的镜像 ID；
`/healthz` 90 秒内不通则把 `latest` 指回旧镜像并重新 up，自动回滚。
手动回滚：

```bash
cd /opt/nongyu
docker images nongyu-backend --format '{{.Tag}} {{.ID}}'   # 找上一版 sha
docker tag nongyu-backend:<上一版sha> nongyu-backend:latest
docker compose -f docker-compose.prod.yml up -d --no-deps --force-recreate server
```

## 日常

- 部署：push 到 `main`，或 Actions 页手动 `workflow_dispatch`。
- 看状态：`docker compose -f docker-compose.prod.yml ps`；`docker logs nongyu-server`。
- 应用监听 `:8080`，探测 `http://<host>:8080/healthz`。
- 迁移：服务启动自动跑 `migrations/*.sql`（幂等），无需手工执行。

## 安全不变量

- 凭据只在服务器 `.env` 与 GitHub Secrets；仓库、日志、审计都不含明文。
- `APP_ENV=prod` 下强校验：`AUTH_KEY`/`TICKET_KEY` 非占位且 ≥32 字节、
  `REDIS_ADDR` 必填、`APP_SIGNING_KEYS` 必填、`HTTP_ENFORCE` 不得关。
- 当前 `APP_SIGNING_KEYS` 用的是开发内置值（APK 里拿得到），
  正式 App 发行前必须换成生产密钥并做 kid 轮换。
