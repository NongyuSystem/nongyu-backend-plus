# Kubernetes 探针与部署要点

本文件只说明**本服务的特殊性**，不是完整部署清单。通用部分（资源限制、
滚动更新策略等）不在此重复。

## 为什么镜像里没有 curl

`deploy/Dockerfile` 用 `distroless/static` 作为运行镜像：没有 shell、
没有包管理器、没有 curl。这是刻意的——它的攻击面比 `alpine` 小得多，
而本服务的运行期依赖（读写都在外部）不需要任何系统工具。

代价是**不能在容器内做 HTTP 探活**，必须由 K8s 的 `httpGet` 探针完成。

## 两个探针不要混用

```yaml
livenessProbe:
  httpGet:
    path: /healthz      # 只报「进程可达」，不探下游
    port: 8080
  periodSeconds: 10
  failureThreshold: 3

readinessProbe:
  httpGet:
    path: /status       # 如实报 Postgres / Redis / 对象存储的真实状态
    port: 8080
  periodSeconds: 10
  failureThreshold: 3
```

**关键区别**：
- `/healthz` **不检查任何下游依赖**。用它做 liveness 是安全的：
  数据库短暂抖动不会导致容器被杀重启（那只会放大故障）。
- `/status` 会真的 Ping 每个依赖。用它做 readiness：依赖不可用时把实例
  摘出负载均衡，但**不重启**。

两者都恒返回 200（真实状态在 body 里），所以 K8s 只能靠「连不上」判失败——
若要让 `/status` 的 body 状态参与判定，需要用自定义探针脚本，不建议。

## 启动顺序依赖（本服务是「连不上就拒绝启动」）

服务在启动时会做这些硬检查，任一失败即**拒绝启动**（这是刻意的：
带着坏依赖上线比直接失败更危险）：

1. `DATABASE_URL` 非空时：连接池必须能 Ping 通；
2. `DATABASE_URL` 非空时：自动执行 `migrations/*.sql`（幂等），失败即退出；
3. `REDIS_ADDR` 非空时：票据存储必须 Ping 通；
4. `MINIO_ENDPOINT` 非空时：必须能连上**且桶已存在**（不自动建桶）。

因此 K8s 里需要：
- 若数据库/缓存也在集群内，用 `initContainer` 等待它们就绪，或接受
  CrashLoopBackOff 让 K8s 自己重试（更简单，且天然适配依赖晚于服务启动的情况）；
- **桶要在部署前建好**（`mc mb` 或云厂商的 bucket 创建），否则服务起不来。

## 环境变量

不要用 ConfigMap 明文存密钥。`AUTH_KEY` / `TICKET_KEY` / `MINIO_SECRET_KEY` /
`SMTP_PASSWORD` / `DATABASE_URL`（含密码）都应来自 Secret：

```yaml
envFrom:
  - secretRef:
      name: nongyu-backend-plus-secrets
  - configMapRef:
      name: nongyu-backend-plus-config
```

非密钥项（`ADDR` / `RATE_LIMIT_PER_MINUTE` / `HTTP_*` / `CACHE_*`）放 ConfigMap。
完整清单见仓库根的 `.env.example`（37 个变量，逐条标了默认值与不配的后果）。

## 生产必配项（否则拒绝启动）

`APP_ENV=prod` 或 `staging` 时逐条强校验，任一不满足即退出：

1. `TICKET_KEY` ≥ 32 字节且不是开发占位值
2. `HTTP_ENFORCE` 不得为 false
3. `AUTH_KEY` 非空
4. `RATE_LIMIT_PER_MINUTE` 为正整数
5. `REDIS_ADDR` 非空（票据需外置持久）
6. `APP_SIGNING_KEYS` 非空

## 优雅关停

服务收到 SIGTERM 后会按顺序：等 HTTP 在途请求结束 → flush 通知派生队列 →
flush 埋点缓冲 → 关数据库连接池。这个过程受 `SHUTDOWN_TIMEOUT_SEC`（默认 10s）约束。

因此 `terminationGracePeriodSeconds` 应**大于**它（建议 30s），并配
`preStop: sleep 5` 让负载均衡先摘流，否则关停期间仍会有请求打进来。

## 进程无状态

本服务不写本地磁盘：附件进对象存储、数据进 Postgres、票据与缓存进 Redis。
因此可以任意水平扩副本，不需要持久卷。若发现容器里出现写入磁盘的行为，
那是 bug（应报给后端而不是加 PVC）。
