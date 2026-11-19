#!/usr/bin/env bash
# 服务器端部署脚本：由 GitHub Actions 通过 SSH 调用。
#
# 用法：
#   ./deploy.sh <镜像tar路径> <镜像sha标签>
#
# 流程：
#   1. docker load 新镜像
#   2. 记下当前 latest 的镜像 ID 作为回滚点
#   3. 把新镜像标成 latest，compose up 只重建 server（依赖容器不动）
#   4. 轮询 /healthz，成功即完成
#   5. 失败：把 latest 指回旧镜像并重新 up，自动回滚
#
# 幂等：重复执行安全；回滚点不存在时（首次部署）失败直接报错退出。
set -Eeuo pipefail

TAR="${1:?需要镜像 tar 路径}"
SHA="${2:?需要镜像 sha 标签}"
COMPOSE_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$COMPOSE_DIR"

NEW_IMAGE="nongyu-backend:${SHA}"
LATEST="nongyu-backend:latest"
HEALTH_URL="http://127.0.0.1:8080/healthz"
MAX_WAIT=90

echo "[deploy] 加载镜像 ${SHA} ..."
docker load -i "$TAR"

echo "[deploy] 标记 ${NEW_IMAGE} -> ${LATEST} ..."
docker tag "$NEW_IMAGE" "$LATEST"

# 记录回滚点：当前正在运行的 server 容器用的是哪个镜像 ID。
ROLLBACK_ID="$(docker inspect -f '{{.Image}}' nongyu-server 2>/dev/null || true)"
if [ -n "$ROLLBACK_ID" ]; then
  echo "[deploy] 回滚点: ${ROLLBACK_ID:0:12}"
fi

rollback() {
  echo "[deploy] 部署失败，开始回滚 ..." >&2
  if [ -n "${ROLLBACK_ID:-}" ]; then
    docker tag "$ROLLBACK_ID" "$LATEST" || true
  fi
  docker compose -f docker-compose.prod.yml up -d --no-deps --force-recreate server || true
  echo "[deploy] 已回滚到上一版本" >&2
}
trap 'rollback' ERR

echo "[deploy] 重建 server 容器 ..."
docker compose -f docker-compose.prod.yml up -d --no-deps --force-recreate server

echo "[deploy] 等待健康 ${HEALTH_URL} (最多 ${MAX_WAIT}s) ..."
ok=0
for _ in $(seq 1 "$MAX_WAIT"); do
  if curl -fsS --max-time 2 "$HEALTH_URL" >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 1
done

if [ "$ok" -ne 1 ]; then
  echo "[deploy] 健康检查超时" >&2
  docker logs --tail 30 nongyu-server >&2 || true
  return 1 2>/dev/null || exit 1
fi

trap - ERR
echo "[deploy] 部署成功: ${SHA}"
docker compose -f docker-compose.prod.yml ps --format 'table {{.Name}}\t{{.Status}}'
