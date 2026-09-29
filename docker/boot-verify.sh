#!/usr/bin/env bash
# boot-verify.sh —— #12 开机链带外验证器（T-030）。
#
# 登录时由 Startup 的 moretoken-boot-verify.vbs 隐藏拉起，也可手动跑。
# 存在的意义：重启会杀掉会话——**结果不靠任何人的记忆，全靠本日志**
# （docker/runtime/boot-verify.log，append 带时间戳）。判据全部机器可查，
# FAIL 附恢复命令。这就是 T-016"带外记录"纪律在开机验证上的落地。
set -u

cd /e/AImlyForge/tools/agent/moretoken 2>/dev/null \
  || cd "E:/AImlyForge/tools/agent/moretoken" 2>/dev/null \
  || exit 1
export PATH="/usr/bin:/bin:$PATH"

LOG="docker/runtime/boot-verify.log"
mkdir -p docker/runtime
CONTAINER="moretoken"
BASE="http://127.0.0.1:8462"
VAULT="E:/AImlyForge/tools/PUBLIC/vault/bin/vault.exe"

line() { echo "$*" >> "$LOG"; }
code() { curl -s -m 5 -o /dev/null -w '%{http_code}' "$@" 2>/dev/null || echo 000; }

line "===== $(date '+%F %T') boot-verify 开始 ====="

# 0) 系统启动时刻——"容器是开机后自己起来的"的时间基准（与下面 StartedAt 对照）。
BOOT="$(powershell.exe -NoProfile -Command "(Get-CimInstance Win32_OperatingSystem).LastBootUpTime.ToString('yyyy-MM-dd HH:mm:ss')" 2>/dev/null | tr -d '\r')"
line "系统启动时刻: ${BOOT:-未知}"

# 1) 等 docker daemon（AutoStart 是登录项，引擎就绪通常 30-90s；给足 300s）。
READY=""
for i in $(seq 1 60); do
  docker info >/dev/null 2>&1 && { READY="$i"; break; }
  sleep 5
done
if [ -n "$READY" ]; then
  line "docker daemon 就绪: 约 $((READY*5))s"
else
  line "FAIL #12a: docker daemon 300s 未就绪——Docker Desktop 没自启？恢复: 手动启动 Docker Desktop 后 bash docker/mt-deploy.sh"
  line "===== $(date '+%F %T') boot-verify 结束 ====="
  exit 1
fi

# 2) 等容器 running/healthy（restart:always 应在 daemon 就绪后自动拉起）。
ST="absent"
for _ in $(seq 1 36); do
  ST="$(docker inspect -f '{{.State.Status}}/{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$CONTAINER" 2>/dev/null || echo absent)"
  [ "$ST" = "running/healthy" ] && break
  sleep 5
done
STARTED="$(docker inspect -f '{{.State.StartedAt}}' "$CONTAINER" 2>/dev/null || echo none)"
line "容器状态: $ST   StartedAt(UTC): $STARTED"
if [ "$ST" = "running/healthy" ]; then
  line "PASS #12a: 容器开机自动拉起且 healthy（StartedAt 应晚于系统启动时刻）"
else
  line "FAIL #12a: 容器状态 $ST——恢复: bash docker/mt-deploy.sh；查: docker logs moretoken"
fi

# 3) 端点语义：豁免与鉴权开机后完好。
H="$(code "$BASE/health")"
M="$(code "$BASE/v1/models")"
line "端点: /health=$H (want 200)   /v1/models无token=$M (want 401)"
if [ "$H" = 200 ] && [ "$M" = 401 ]; then
  line "PASS #12b: 豁免+鉴权语义完好"
else
  line "FAIL #12b"
fi

# 4) cc-ft 链路等价验证：vault 指针 → token → 网关 200。
TOK="$("$VAULT" get moretoken/tokens/laptop-cc 2>/dev/null | tr -d '[:space:]')"
if [ -n "$TOK" ]; then
  V="$(code -H "Authorization: Bearer $TOK" "$BASE/v1/models")"
  if [ "$V" = 200 ]; then
    line "PASS #12c: vault→token→网关 200（cc-ft 可用）"
  else
    line "FAIL #12c: 带 token=$V"
  fi
else
  line "FAIL #12c: vault 取 token 失败（vault 服务没起？dev-fleet 管着它）"
fi

# 5) 旁证：freellm 群也回来了（Docker 自启面整体健康，不是 moretoken 独活）。
FL="$(docker ps --format '{{.Names}}' 2>/dev/null | grep -c '^freellm' || true)"
line "旁证: freellm 容器在跑 ${FL:-0} 个"

line "===== $(date '+%F %T') boot-verify 结束 ====="
