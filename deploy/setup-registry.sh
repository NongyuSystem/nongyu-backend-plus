#!/usr/bin/env bash
# 在服务器上初始化私有 Docker Registry（带 basic auth）。
#
# 用法（在服务器上）：
#   REG_USER=nongyu REG_PASS='<强密码>' bash deploy/setup-registry.sh
#
# 密码只走环境变量，不写进脚本、不进仓库、不进历史。
# 前提：registry:2 镜像已 docker load（服务器拉不到 Docker Hub）。
set -e
USER_NAME="${REG_USER:?需要 REG_USER}"
PASS_ENV="${REG_PASS:?需要 REG_PASS}"

mkdir -p /opt/nongyu/registry/auth /var/lib/nongyu/registry

python3 - "$USER_NAME" "$PASS_ENV" <<'PYEOF'
import sys, bcrypt
user, password = sys.argv[1], sys.argv[2]
h = bcrypt.hashpw(password.encode(), bcrypt.gensalt()).decode()
with open('/opt/nongyu/registry/auth/htpasswd', 'w') as f:
    f.write(f'{user}:{h}\n')
print('htpasswd 已生成')
PYEOF
chmod 600 /opt/nongyu/registry/auth/htpasswd

docker rm -f nongyu-registry 2>/dev/null || true
docker run -d --name nongyu-registry \
  --restart unless-stopped \
  -p 5000:5000 \
  -v /var/lib/nongyu/registry:/var/lib/registry \
  -v /opt/nongyu/registry/auth:/auth:ro \
  -e REGISTRY_AUTH=htpasswd \
  -e 'REGISTRY_AUTH_HTPASSWD_REALM=Registry Realm' \
  -e REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd \
  registry:2

sleep 5
echo "=== registry 状态 ==="
docker ps --filter name=nongyu-registry --format '{{.Status}} {{.Ports}}'
echo "=== 验证（未认证应 401，认证应 200）==="
curl -s -o /dev/null -w '未认证 code=%{http_code}\n' http://127.0.0.1:5000/v2/
curl -s -u "$USER_NAME:$PASS_ENV" -o /dev/null -w '认证   code=%{http_code}\n' http://127.0.0.1:5000/v2/
