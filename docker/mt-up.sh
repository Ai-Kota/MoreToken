#!/usr/bin/env bash
# mt-up.sh —— generic 形态部署（T-029，对外默认路径）。
#
# 与 mt-deploy.sh（vault 形态）的区别：**没有宿主解析步骤**——config 里是 env:VAR
# 指针或字面量，key 经 docker/.env.keys 由 env_file 注入容器，网关启动时自行解析。
# 宿主零依赖：只要 docker，不要 Go 工具链、不要 vault。
#
# 用法： bash docker/mt-up.sh
# 可选环境变量：MT_CONTAINER（默认 moretoken）/ MT_PORT（默认 8462）/ MT_CONFIG /
#   MT_PROJECT（默认 moretoken）——多实例/旁路测试时**四个一起改**：
#   compose 同项目同服务名会 recreate 对方容器，项目名不隔离 = 互相顶掉（实测教训）。
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
cd "$ROOT"

CONTAINER="${MT_CONTAINER:-moretoken}"
PORT="${MT_PORT:-8462}"
CONFIG_HOST="${MT_CONFIG:-$ROOT/config/config.json}"
RUNTIME="$HERE/runtime"
TOKENS="$RUNTIME/tokens.json"
IMAGE="moretoken:latest"

say() { printf '\033[36m[mt-up]\033[0m %s\n' "$*"; }
die() { printf '\033[31m[mt-up] %s\033[0m\n' "$*" >&2; exit 1; }

# ---- ① pre-flight ----
say "① pre-flight"
docker info >/dev/null 2>&1 || die "docker 不可用（Docker Desktop 没起？PATH？）"
[ -f "$CONFIG_HOST" ] || die "缺 config：$CONFIG_HOST —— 先 cp config/config.example.json config/config.json 并填 base_url/模型/env:VAR"
if [ ! -f "$HERE/.env.keys" ] && grep -q '"env:' "$CONFIG_HOST" 2>/dev/null; then
  say "   ⚠️ config 里有 env: 指针但没有 docker/.env.keys —— 那些 key 会 unresolved（池 503）。"
  say "     cp docker/.env.keys.example docker/.env.keys 填入真实值后重跑本脚本。"
fi
mkdir -p "$RUNTIME"
if [ ! -f "$TOKENS" ]; then
  printf '{"tokens":[]}\n' > "$TOKENS"
  say "   ⚠️ 无 tokens.json → 建空名单：**网关当前不鉴权**（仅 127.0.0.1 可达，勉强可接受）。"
  say "     发第一个 token（部署完成后跑）：bash docker/mt-token.sh gen <名字>"
fi

# ---- ② 构建 + 启动 ----
say "② compose up（generic 形态：config 单文件 ro bind + env_file 注入）"
# COMPOSE_PROJECT_NAME 隔离：同名 project+service 的 up 会 recreate 对方容器
# （旁路测试顶掉生产实例的实测教训），多实例必须四参一起改（见文件头）。
COMPOSE_PROJECT_NAME="${MT_PROJECT:-moretoken}" \
MT_CONTAINER="$CONTAINER" MT_PORT="$PORT" MT_CONFIG="$CONFIG_HOST" \
  docker compose -f "$HERE/docker-compose.yml" up -d --build || die "compose up 失败"

# ---- ③ 轻量健康检查 ----
say "③ 健康检查（http://127.0.0.1:$PORT/health）"
H=""
for _ in 1 2 3 4 5 6; do
  H="$(curl -s -m 3 -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/health" 2>/dev/null || echo 000)"
  [ "$H" = 200 ] && break
  sleep 3
done
[ "$H" = 200 ] || {
  docker logs --tail 20 "$CONTAINER" 2>&1 || true
  die "/health=$H（容器起不来？上面是日志尾部）"
}

# ---- ④ unresolved 检查（env: 用户最常见的坑，必须响）----
UNRES="$(docker logs "$CONTAINER" 2>&1 | grep -c "unresolved key" || true)"
if [ "${UNRES:-0}" -gt 0 ]; then
  say "   ⚠️ 启动日志有 $UNRES 条 unresolved key —— .env.keys 缺变量或名字不匹配："
  docker logs "$CONTAINER" 2>&1 | grep "unresolved key" | tail -5 || true
fi

say "✅ generic 形态就绪：http://127.0.0.1:$PORT（容器 $CONTAINER）"
say "   下一步：bash docker/mt-token.sh gen my-first-agent   # 发 token（-plain 回显一次）"
say "   验证：  MT_CONTAINER=$CONTAINER MT_BASE=http://127.0.0.1:$PORT bash docker/mt-verify.sh"
