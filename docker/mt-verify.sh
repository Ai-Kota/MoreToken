#!/usr/bin/env bash
# mt-verify.sh —— MoreToken 容器部署验证矩阵（T-026）。可独立重跑（部署后任何时候怀疑就再跑）。
#
# 全部断言化：任一条 FAIL → 退出非零。覆盖 T-026 验证矩阵里**可自动化**的条目；
# 机器重启自愈（#12）与 dev-fleet guard（#10）是人工/跨任务项，见 README 与本脚本末尾提示。
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
HOST_BIN="$ROOT/bin/mt-host.exe"
TOKENS="$HERE/runtime/tokens.json"
BASE="http://127.0.0.1:8462"
PASS=0; FAIL=0

ok()   { printf '\033[32m  PASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '\033[31m  FAIL\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }
chk()  { [ "$1" = "$2" ] && ok "$3 ($2)" || bad "$3 (got $1, want $2)"; }
code() { curl -s -m "${4:-8}" -o /dev/null -w "%{http_code}" "$@" 2>/dev/null; }

[ -x "$HOST_BIN" ] || { echo "缺宿主 CLI $HOST_BIN，先跑 mt-deploy.sh"; exit 1; }

echo "== 容器状态 =="
STATE="$(docker inspect -f '{{.State.Status}}/{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' moretoken 2>/dev/null || echo absent)"
echo "  moretoken: $STATE"

echo "== 鉴权与豁免 =="
chk "$(code "$BASE/health")"                       200 "#1 /health 无 token 豁免"
chk "$(code "$BASE/v1/models")"                    401 "#2 /v1/models 无 token 拒绝"
chk "$(code "$BASE/decisions")"                    401 "#2b /decisions 无 token 拒绝"

# 发一个一次性 plain token（明文只进本脚本变量，不回显、用完吊销）。
echo "== 带 token 链路（发临时探针 token）=="
GEN_OUT="$("$HOST_BIN" -token-gen -token-name verify-probe -token-plain -token-ttl 10m -tokens-file "$TOKENS" 2>&1)"
TOK="$(printf '%s' "$GEN_OUT" | grep -oE 'mt_[A-Za-z0-9]+' | head -1)"
if [ -z "$TOK" ]; then bad "#3 临时 token 发放失败：$GEN_OUT"; else
  sleep 1  # 给容器 mtime 热加载一拍
  chk "$(code -H "Authorization: Bearer $TOK" "$BASE/v1/models")" 200 "#3 Bearer 带 token 放行"
  chk "$(code -H "x-api-key: $TOK" "$BASE/v1/models")"            200 "#3b x-api-key 带 token 放行"

  echo "== 容器内 uid 10001 可读挂载（#4）=="
  R="$(docker exec moretoken sh -c 'id -u; head -c1 /run/mt-tokens/tokens.json >/dev/null && echo tok-ok; head -c1 /run/secrets/nats/bridge.creds >/dev/null && echo creds-ok' 2>&1)"
  printf '%s\n' "$R" | grep -q '^10001$'  && ok "#4 运行用户 uid=10001"     || bad "#4 uid 非 10001：$R"
  printf '%s\n' "$R" | grep -q 'tok-ok'    && ok "#4 tokens.json 容器内可读"  || bad "#4 tokens 不可读：$R"
  printf '%s\n' "$R" | grep -q 'creds-ok'  && ok "#4 nats creds 容器内可读"   || bad "#4 creds 不可读：$R"

  echo "== 真实 chat completion（#5）=="
  CC="$(curl -s -m 60 -o /dev/null -w "%{http_code}" -X POST "$BASE/v1/chat/completions" \
        -H "Content-Type: application/json" -H "Authorization: Bearer $TOK" \
        -d '{"model":"auto:general","messages":[{"role":"user","content":"Reply exactly: T026-OK"}],"max_tokens":16}')"
  chk "$CC" 200 "#5 chat completion 经鉴权网关"
  AUTH="$(curl -s -m 8 -H "Authorization: Bearer $TOK" "$BASE/decisions?n=5" 2>/dev/null | grep -o '"auth":"verify-probe"' | head -1)"
  [ -n "$AUTH" ] && ok "#5b 决策日志记 auth=verify-probe（谁发的可答 I11）" || bad "#5b 决策日志缺 auth 字段"

  echo "== 吊销热传播（#6，缺陷 S1 的决定性证据）=="
  "$HOST_BIN" -token-revoke -token-name verify-probe -tokens-file "$TOKENS" >/dev/null 2>&1
  DEAD=""; for i in $(seq 1 10); do
    sleep 0.5
    [ "$(code -H "Authorization: Bearer $TOK" "$BASE/v1/models")" = 401 ] && { DEAD="${i}"; break; }
  done
  # 不用 bc（Git Bash 无）：DEAD 是 0.5s 轮询次数，纯 bash 算术报 tenths。
  [ -n "$DEAD" ] && ok "#6 吊销后 $((DEAD*5))/10 秒内容器侧 401（目录挂载 rename 传播 <5s）" \
                 || bad "#6 吊销 5s 内未生效——单文件挂载 inode 钉死？改目录挂载（评审 S1）"
fi

echo "== 自愈（#7 restart / #8 进程退出→restart:always 拉起）=="
docker restart moretoken >/dev/null 2>&1; sleep 4
chk "$(code "$BASE/health")" 200 "#7 docker restart 后健康"
# #8：触发进程**自然退出**（TERM PID1 → moretoken 优雅关停 → 容器自行 exit），这才是
# restart:always 覆盖的崩溃/退出路径。**刻意不用 docker kill**——那是"手动停止"，
# Docker 语义下手动停止不触发重启策略（要到 daemon 重启才应用 always），不是缺陷。
RC0="$(docker inspect -f '{{.RestartCount}}' moretoken 2>/dev/null)"
docker exec moretoken kill -TERM 1 >/dev/null 2>&1
REC=""; for i in $(seq 1 10); do
  sleep 2
  [ "$(docker inspect -f '{{.State.Status}}' moretoken 2>/dev/null)" = running ] && { REC="$i"; break; }
done
RC1="$(docker inspect -f '{{.RestartCount}}' moretoken 2>/dev/null)"
if [ -n "$REC" ] && [ "${RC1:-0}" -gt "${RC0:-0}" ]; then
  ok "#8 进程退出后 restart:always 自动拉起（RestartCount $RC0→$RC1，$((REC*2))s 内 running）"
else
  bad "#8 进程退出后未自动恢复（RestartCount $RC0→$RC1）"
fi
sleep 3; chk "$(code "$BASE/health")" 200 "#8b 自愈后健康"

echo "== NATS 发布（#9，best-effort：增强非依赖）=="
NATS_BIN="/e/AImlyForge/tools/bin/nats.exe"
if [ -x "$NATS_BIN" ]; then
  CREDS="/c/Users/aimly/.local/share/nats/nsc/keys/creds/AImlyCloud/AImlyCloud/bridge.creds"
  sleep 5  # 等 #8 重启后的发布器稳定
  # 窗口 70s：发布周期 30s，覆盖两个 tick，避开"窗口太短恰好错过"的假阴性。
  GOT="$(NATS_URL="nats://www.aimly.top:14222" NATS_CREDS="$CREDS" timeout 70 \
        "$NATS_BIN" sub aimly.system.freetier.status --count 1 2>/dev/null | head -c 160)"
  [ -n "$GOT" ] && ok "#9 订阅到容器发布的 status（${GOT:0:48}…）" || echo "  SKIP #9 70s 内未收到 status（NATS 是增强项，不阻塞；查容器日志 nats 行）"
else
  echo "  SKIP #9 宿主无 nats.exe，跳过 NATS 订阅验证"
fi

echo
printf '\033[1m验证小结：%d PASS / %d FAIL\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
echo "人工项：#10 dev-fleet guard/status 无违规（跑 ops/dev-fleet/dev-fleet.exe guard）；#12 机器重启后网关自动在跑（方便时验一次）"
