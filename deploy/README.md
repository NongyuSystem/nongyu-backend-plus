# 部署说明（生产）

后端是独立 Git 仓库（嵌套在 monorepo 的 `apps/` 下，由自身仓库管理）。
**一个仓库一个 CI**：CI 只做代码检查，部署走「本地 → 私有 registry → 服务器」。

## 为什么不用 GitHub Actions 直接部署

实测（2026-11-19）：GitHub runner 出口 IP（`20.168.125.110`、`3.42.157.55`，境外）
到服务器 `103.236.88.53` 的**所有端口全超时**；但 runner 到 `1.1.1.1`/`github.com`
正常，服务器安全组也全开放（`0.0.0.0/0`）。结论是**境外到该 IP 的国际路由不通**，
非配置问题，服务器侧无法解决。因此改为本地构建 + 推私有 registry。

（`.github/workflows/deploy.yml` 保留了远端 SSH 部署流程，等服务器有国际线路可重新启用；
目前 push 会在 scp 阶段因网络超时失败。本地推送请用 `deploy/push.sh`。）

## 架构

```
本地：go build → docker build -f deploy/Dockerfile.bin → push 到私有 registry
服务器：从 registry pull → docker compose up 只重建 server → 探活（失败回滚）
```

依赖（postgres18/redis7/minio）在服务器初始化时一次性备好，日常部署不碰。

## 服务器现状（103.236.88.53）

- `/opt/nongyu/`：`.env`（600）、`docker-compose.prod.yml`、`deploy.sh`、`setup-registry.sh`
- `/var/lib/nongyu/{postgres,redis,minio,registry}`：持久化数据
- 私有 registry：`nongyu-registry` 容器，监听 `5000`，basic auth（bcrypt）
- 四个容器 `nongyu-{postgres,redis,minio,server}` 均 healthy，`/healthz` 正常

## 服务器初始化（已完成，仅备查）

1. 依赖镜像离线导入：`postgres:18-alpine`、`redis:7-alpine`、`minio/minio:latest`
   （服务器任何镜像源都拉不到 blob，必须 `docker save` 传 tar 再 `docker load`）。
2. 私有 registry 镜像 `registry:2` 同样离线导入。
3. 起 registry（密码只走环境变量）：
   ```bash
   REG_USER=nongyu REG_PASS='<强密码>' bash deploy/setup-registry.sh
   ```
4. 应用容器：`docker compose -f docker-compose.prod.yml up -d --no-deps server`。

注意 Postgres 18 数据挂到 `/var/lib/postgresql`（不是 `.../data`）。
`.env` 里除应用配置外还要有 `DB_PASS`/`REDIS_PASS`/`MINIO_PASS` 供 compose 展开。
MinIO 桶 `nongyu` 需手动建一次（服务不自动建桶）：
```bash
docker exec -e MC_HOST_local="http://nongyu:<pass>@nongyu-minio:9000" nongyu-minio \
  mc mb --ignore-existing local/nongyu
```

## NAT（待你配置）

registry 在服务器内网 `5000`。你在 NAT 上把某端口（下称 `<NAT端口>`）转发到
`103.236.88.53:5000`，然后本地按下面方式推送。

## 本地推送（日常部署）

1. Docker Desktop → Settings → Docker Engine，把 registry 加入 insecure：
   ```json
   "insecure-registries": ["103.236.88.53:<NAT端口>"]
   ```
2. 一条命令（凭据不落盘）：
   ```bash
   REGISTRY=103.236.88.53:<NAT端口> \
   REG_USER=nongyu REG_PASS='<密码>' \
   SSH_HOST=103.236.88.53 SSH_PORT=44400 SSH_USER=root \
   bash deploy/push.sh
   ```
   脚本会：编译 → 打包 → push（`tag` + `latest`）→ SSH 到服务器 pull + 重建 +
   探活；失败自动回滚到上一镜像。

## 安全

- 凭据只在环境变量与服务器 `.env`/htpasswd；仓库、日志、审计不含明文。
- registry 强制 basic auth，未认证 401。
- `APP_ENV=prod` 强校验：`AUTH_KEY`/`TICKET_KEY` 非占位 ≥32 字节、`REDIS_ADDR` 必填、
  `APP_SIGNING_KEYS` 必填、`HTTP_ENFORCE` 不得关。
- 当前 `APP_SIGNING_KEYS` 是开发内置值，正式发版前必须换生产密钥并做 kid 轮换。
