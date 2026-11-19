#!/usr/bin/env bash
# 本地 → 私有 registry → 服务器部署（一条龙）。
#
# 背景：GitHub Actions（境外）连不上这台境内服务器（实测 runner 出口 IP
# 到目标 IP 全端口超时，安全组全开放、密钥正常，是国际路由问题）。
# 因此改为本地构建、推到服务器上的私有 registry，服务器从 registry 拉取。
#
# 用法（本地，需本机有 go + docker）：
#   REGISTRY=103.236.88.53:61971 \
#   REG_USER=nongyu REG_PASS='<强密码>' \
#   SSH_HOST=103.236.88.53 SSH_PORT=44400 SSH_USER=root \
#   bash deploy/push.sh
#
# 前置：
#   1. 服务器上私有 registry 已跑（deploy/setup-registry.sh），NAT 已把
#      103.236.88.53:61971 转发到服务器 5000。
#   2. 本地 Docker 把 REGISTRY 加入 insecure registries（HTTP registry 必须）：
#      ~/.docker/daemon.json → "insecure-registries": ["103.236.88.53:61971"]
#      改后重启 Docker Desktop。
#
# 步骤：编译静态二进制 → 用 Dockerfile.bin 打包 → push 到 registry
#       → SSH 到服务器 pull + 标 latest + 重建 server + 探活（失败回滚由 deploy.sh 负责）。
set -Eeuo pipefail

REGISTRY="${REGISTRY:?需要 REGISTRY（host:port，NAT 后的地址）}"
REG_USER="${REG_USER:?需要 REG_USER}"
REG_PASS="${REG_PASS:?需要 REG_PASS}"
SSH_HOST="${SSH_HOST:-103.236.88.53}"
SSH_PORT="${SSH_PORT:-44400}"
SSH_USER="${SSH_USER:-root}"
TAG="${TAG:-$(git rev-parse --short HEAD)}"
IMAGE="${REGISTRY}/nongyu-backend:${TAG}"
LATEST="${REGISTRY}/nongyu-backend:latest"

echo "[push] 编译静态二进制（tag=${TAG}）..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -o bin/server ./cmd/server

echo "[push] 打包镜像..."
docker build -f deploy/Dockerfile.bin -t "$IMAGE" .
docker tag "$IMAGE" "$LATEST"

echo "[push] 登录 registry..."
printf '%s' "$REG_PASS" | docker login "$REGISTRY" -u "$REG_USER" --password-stdin

echo "[push] 推送镜像..."
docker push "$IMAGE"
docker push "$LATEST"

echo "[push] 远端拉取并部署..."
# 关键：服务器从「自己的 localhost:5000」拉，不要绕公网 NAT（服务器连自己公网 IP 不通）。
# 本地 push 走公网 REGISTRY，服务器 pull 走 localhost:5000，两者是同一个 registry。
REMOTE_REGISTRY="localhost:5000"
ssh -p "$SSH_PORT" -o StrictHostKeyChecking=accept-new "${SSH_USER}@${SSH_HOST}" bash -s <<REMOTE
set -e
cd /opt/nongyu
echo "$REG_PASS" | docker login "$REMOTE_REGISTRY" -u "$REG_USER" --password-stdin >/dev/null 2>&1
docker pull "$REMOTE_REGISTRY/nongyu-backend:${TAG}"
docker tag "$REMOTE_REGISTRY/nongyu-backend:${TAG}" nongyu-backend:latest
OLD=\$(docker inspect -f '{{.Image}}' nongyu-server 2>/dev/null || true)
docker compose -f docker-compose.prod.yml up -d --no-deps --force-recreate server
echo "[push] 等待健康..."
OK=0
for _ in \$(seq 1 90); do
  if curl -fsS --max-time 2 http://127.0.0.1:3000/healthz >/dev/null 2>&1; then OK=1; break; fi
  sleep 1
done
if [ "\$OK" -ne 1 ]; then
  echo "[push] 探活失败，回滚" >&2
  [ -n "\$OLD" ] && docker tag "\$OLD" nongyu-backend:latest
  docker compose -f docker-compose.prod.yml up -d --no-deps --force-recreate server
  exit 1
fi
docker compose -f docker-compose.prod.yml ps --format 'table {{.Name}}\t{{.Status}}'
REMOTE

echo "[push] 完成: ${TAG}"
