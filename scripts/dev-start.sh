#!/usr/bin/env bash
# 本地启动脚本：读 .env → 起服务 → 探测端点 → （可选）常驻。
#
# 为什么要有它：仓库里 MANY 个能力由环境变量开关（DATABASE_URL / REDIS_ADDR /
# AUTH_KEY / APP_SIGNING_KEYS / SMTP_HOST …）。手工 export 一堆变量既容易漏，
# 也容易把「空壳启动」误当成「完整服务」。这个脚本把「怎么起」与
# 「起完该有哪些端点」写在一处，避免每次靠记忆。
#
# 用法：
#   ./scripts/dev-start.sh              # 起服务并探测，探测完不等（前台运行）
#   ./scripts/dev-start.sh --probe-only # 只探测已运行的服务
#   ./scripts/dev-start.sh --stop       # 停掉本脚本起的服务（按 PID 文件）
#
# 约定：
#   - 优先读仓库根的 .env（已在 .gitignore，禁提交真实值）；无 .env 时用环境变量。
#   - **不打印任何密钥值**，只打印「已设置 / 未设置」。
#   - 探测用 curl.exe / curl，逐 URL 独立取状态码（不靠响应体猜）。

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PID_FILE="$ROOT/.dev-server.pid"
LOG_FILE="$ROOT/.dev-server.log"
ADDR="${ADDR:-:8080}"
PORT="${ADDR##*:}"
BASE="http://127.0.0.1:${PORT}"

# ---------- 读 .env（不覆盖已存在的环境变量）----------
if [[ -f "$ROOT/.env" ]]; then
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -z "$line" || "$line" == \#* ]] && continue
    key="${line%%=*}"
    val="${line#*=}"
    # 去掉可能的引号
    val="${val%\"}"; val="${val#\"}"
    if [[ -z "${!key:-}" ]]; then
      export "$key=$val"
    fi
  done < "$ROOT/.env"
  echo "[env] 已读取 .env"
else
  echo "[env] 未找到 .env（用当前环境变量；完整能力需要 DATABASE_URL / REDIS_ADDR / AUTH_KEY）"
fi

# ---------- 报告开关状态（只报「已设置/未设置」，绝不打印值）----------
report_env() {
  for k in APP_ENV ADDR AUTH_KEY APP_SIGNING_KEYS DATABASE_URL REDIS_ADDR REDIS_PASSWORD SMTP_HOST TICKET_KEY; do
    eval "v=\${$k:-}"
    if [[ -n "$v" ]]; then
      printf '  %-20s 已设置\n' "$k"
    else
      printf '  %-20s 未设置\n' "$k"
    fi
  done
}

# ---------- 探测 ----------
probe() {
  local tool=""
  if command -v curl >/dev/null 2>&1; then tool="curl"
  elif command -v curl.exe >/dev/null 2>&1; then tool="curl.exe"
  fi

  echo
  echo "=== 端点探测 ($BASE) ==="
  local -a paths=(/healthz /status /announcements /announcements/latest /posts /posts/1 /messages /messages/unread-count /track/events/summary /admin/announcements)
  for p in "${paths[@]}"; do
    if [[ "$tool" == "curl" || "$tool" == "curl.exe" ]]; then
      code="$("$tool" -sS -o /dev/null -w '%{http_code}' --max-time 5 "$BASE$p" 2>/dev/null)"
      [[ -z "$code" ]] && code="---"
    else
      code="(无 curl)"
    fi
    printf '  %-28s %s\n' "$p" "$code"
  done
  echo
  echo "状态码含义：200=可用；401=需登录（中间件已生效）；403=权限门槛拒；404=该域尚未实现/未挂载。"
}

# ---------- 动作 ----------
case "${1:-}" in
  --probe-only)
    probe
    exit 0
    ;;
  --stop)
    if [[ -f "$PID_FILE" ]]; then
      pid="$(cat "$PID_FILE")"
      if kill -0 "$pid" 2>/dev/null; then
        kill "$pid" 2>/dev/null || true
        echo "[stop] 已发送 TERM 给 pid=$pid（优雅关停会让 notify/track 缓冲 flush）"
      else
        echo "[stop] pid=$pid 已不在运行"
      fi
      rm -f "$PID_FILE"
    else
      echo "[stop] 无 PID 文件"
    fi
    exit 0
    ;;
esac

if [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
  echo "[start] 检测到已有实例 pid=$(cat "$PID_FILE")，先 --stop 再启动"
  exit 1
fi

echo "=== 环境开关 ==="
report_env

echo
echo "[start] 编译并启动（日志：$LOG_FILE）"
( go run ./cmd/server > "$LOG_FILE" 2>&1 & echo $! > "$PID_FILE" )

# 等端口就绪（最多 30 秒）——go run 首次编译可能较慢。
for i in $(seq 1 60); do
  if [[ -n "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 1 "$BASE/healthz" 2>/dev/null)" ]]; then
    break
  fi
  sleep 0.5
done

echo "[start] 装配摘要（来自日志）："
grep -o '"msg":"装配完成"[^}]*}' "$LOG_FILE" 2>/dev/null | head -1 || echo "  (未取到装配完成行，请查看日志)"
echo
echo "[start] 未启用能力告警："
grep -o '"level":"WARN","msg":"[^"]*"' "$LOG_FILE" 2>/dev/null | sed 's/.*"msg":"/  - /; s/"$//' | sort -u || true

probe

echo
echo "[start] 服务仍在后台运行（pid=$(cat "$PID_FILE" 2>/dev/null)）。"
echo "        查看日志：tail -f $LOG_FILE"
echo "        停止服务：$0 --stop"
