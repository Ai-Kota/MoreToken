#!/usr/bin/env bash
# mt-deploy.sh —— MoreToken 容器化部署编排（T-026）。幂等、可重跑。
#
# 职责：宿主解析 vault → 投递命名卷 → compose 起容器 → 验证。
# 核心不变量：**明文 key 只经管道进 Docker 命名卷，不在 Windows 文件系统留文件**
#   （命名卷物理落 Docker VM 的 vhdx，读取需 docker 权限、不进 git——见 T-026 安全姿态）。
#
# 用法： bash docker/mt-deploy.sh          （在仓库任意位置跑，脚本自己定位根）
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
cd "$ROOT"

VOLUME="moretoken_moretoken-run"          # 与 docker-compose.yml 的 external volume name 逐字一致
RUNTIME="$HERE/runtime"
TOKENS="$RUNTIME/tokens.json"
HOST_CONFIG="$ROOT/config/config.json"
CREDS="/c/Users/aimly/.local/share/nats/nsc/keys/creds/AImlyCloud/AImlyCloud/bridge.creds"
HOST_BIN="$ROOT/bin/mt-host.exe"          # 宿主侧 CLI（materialize/token 管理）；不覆盖运行中的 moretoken.exe
ALPINE="alpine:3.21"

say()  { printf '\033[36m[deploy]\033[0m %s\n' "$*"; }
die()  { printf '\033[31m[deploy] %s\033[0m\n' "$*" >&2; exit 1; }

# ---- ① pre-flight：bind 源必须存在且是文件（防 Docker 把缺失源自动建成目录）----
say "① pre-flight 检查 bind 源与端口"
[ -f "$HOST_CONFIG" ] || die "缺宿主 config：$HOST_CONFIG"
[ -f "$CREDS" ]       || die "缺 NATS 凭据：$CREDS（容器要 ro 挂载；缺失会被 Docker 建成目录）"
mkdir -p "$RUNTIME"
if [ ! -f "$TOKENS" ]; then
  # 首次部署：从既有 config/tokens.json 迁移（含 laptop-cc）；再没有则建空名单（= 不鉴权，仅 127.0.0.1 可接受）。
  if [ -f "$ROOT/config/tokens.json" ]; then
    cp "$ROOT/config/tokens.json" "$TOKENS"
    say "   tokens.json 从 config/ 迁移到 docker/runtime/（宿主 CLI 唯一写者）"
  else
    printf '{"tokens":[]}\n' > "$TOKENS"
    say "   ⚠️ 无既有 tokens.json → 建空名单（网关将**不鉴权**，仅回环可达）。发 token：见 README"
  fi
fi
[ -f "$TOKENS" ] || die "tokens.json 仍不存在：$TOKENS"
# 端口占用探测：容器只绑 127.0.0.1:8462，若已被占（手动实例/dev-fleet 旧服务）compose 会 bind 失败。
if curl -s -m 2 -o /dev/null "http://127.0.0.1:8462/health" 2>/dev/null; then
  say "   ⚠️ :8462 已有响应——若是旧的手动实例/dev-fleet free-tier-aggregator，先停掉再起容器（见 README 迁移节）"
fi

# ---- ② 宿主构建 CLI ----
say "② 构建宿主 moretoken CLI（materialize/token 管理用）"
GOWORK=off go build -o "$HOST_BIN" . || die "宿主构建失败"

# ---- ③ 供给面闸：需求面 ⊆ 供给面，有致命缺口不部署 ----
say "③ 供给面断言（-check）"
"$HOST_BIN" -check -config "$HOST_CONFIG" || die "供给面检查不通过，拒绝部署"

# ---- ④ 命名卷（幂等）----
say "④ 确保命名卷 $VOLUME"
docker volume create "$VOLUME" >/dev/null

# ---- ⑤ 宿主解析 vault → 管道直投卷（.new + 非空校验 + cmp 变更检测 + 卷内原子 mv）----
# 明文流向：moretoken.exe 内存 → 管道 → one-shot alpine → 卷。不落 Windows 文件。
# one-shot 用 sh -ec + .new + [ -s ] + mv：管道中断只留 .new、绝不毁现役 config.json。
# 变更检测（T-028）：与现役 config **逐字节相同** → 不替换、回显 SAME（每日定时任务
# 零停机）；不同（纳新/换 key/改配置）→ 原子换入、回显 CHANGED（⑥b 据此重启）。
say "⑤ 解析 vault 指针并投递命名卷（明文不落宿主磁盘；cmp 变更检测）"
MARKER="$(GOWORK=off "$HOST_BIN" -config "$HOST_CONFIG" -materialize-config - \
| docker run --rm -i -v "$VOLUME:/out" "$ALPINE" \
    sh -ec 'umask 077; cat > /out/config.json.new; [ -s /out/config.json.new ]; chown 10001:10001 /out/config.json.new; if [ -f /out/config.json ] && cmp -s /out/config.json.new /out/config.json; then rm -f /out/config.json.new; echo SAME; else mv -f /out/config.json.new /out/config.json; echo CHANGED; fi')" \
|| die "materialize→卷投递失败（未解析的 key？管道中断？现役 config 未受影响）"
say "   投递结果: ${MARKER:-<空>}"

# ---- ⑥ compose 起容器 ----
# 先记"容器已在跑吗"+启动时刻：CHANGED 只对**已运行**的容器补 restart（新建的天然读新 config）；
# StartedAt 前后对比判"compose 是否 recreate 了容器"（镜像变了才会）。
WAS_RUNNING=0
docker ps --format '{{.Names}}' | grep -qx moretoken && WAS_RUNNING=1
BEFORE_START="$(docker inspect -f '{{.State.StartedAt}}' moretoken 2>/dev/null || echo none)"
say "⑥ 构建镜像并启动容器"
# vault 形态专用 compose（T-029 起 docker-compose.yml 是 generic 形态，两文件不通用）。
docker compose -f "$HERE/docker-compose.vault.yml" up -d --build || die "compose up 失败"

# ---- ⑥b config 变了且容器是续跑的 → restart 才生效（config 只在启动时 Load）----
if [ "$MARKER" = "CHANGED" ] && [ "$WAS_RUNNING" = 1 ]; then
  say "⑥b 卷内 config 已变更 → 重启容器使其生效"
  docker restart moretoken >/dev/null || die "restart 失败"
  for _ in $(seq 1 15); do
    sleep 2
    [ "$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' moretoken 2>/dev/null)" = healthy ] && break
  done
  [ "$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' moretoken 2>/dev/null)" = healthy ] \
    || die "重启后 30s 未 healthy——看 docker logs moretoken"
fi

# ---- ⑦ 验证：只有"真的变了"才跑全量矩阵（T-028）----
# mt-verify 的 #7/#8 是破坏性自愈测试（restart + TERM 容器）——每日定时任务若跑它，
# 等于天天白断两次网关。判据：config SAME **且** 容器启动时刻未变（compose 没 recreate、
# ⑥b 没 restart）→ 只做轻量健康检查；任何真实变更（首部署/镜像重建/config 变更）→ 全量。
AFTER_START="$(docker inspect -f '{{.State.StartedAt}}' moretoken 2>/dev/null || echo none)"
if [ "$MARKER" = "SAME" ] && [ "$BEFORE_START" = "$AFTER_START" ]; then
  say "⑦ 零变更部署（config SAME + 容器未动）→ 轻量健康检查"
  H=""
  for _ in 1 2 3; do
    H="$(curl -s -m 3 -o /dev/null -w '%{http_code}' http://127.0.0.1:8462/health 2>/dev/null || echo 000)"
    [ "$H" = 200 ] && break
    sleep 2
  done
  [ "$H" = 200 ] || die "轻量健康检查失败（/health=$H）——看：docker logs moretoken"
  say "✅ 零变更、零停机完成（容器全程未动）"
else
  say "⑦ 运行验证矩阵（有真实变更，全量断言）"
  bash "$HERE/mt-verify.sh" || die "验证未全过——容器可能在 crash loop，看：docker logs moretoken"
  say "✅ 部署完成。开机自启链：登录 → Docker Desktop(AutoStart) → restart:always 拉起容器 → 卷内 config 就位（无需 vault）。"
fi
