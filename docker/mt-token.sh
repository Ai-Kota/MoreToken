#!/usr/bin/env bash
# mt-token.sh —— generic 形态的 token 管理（T-029）。
#
# 宿主零依赖：**借用镜像里的 linux moretoken 二进制**经 one-shot 容器操作
# docker/runtime/tokens.json（rw 挂载）。发的是 -token-plain（明文回显一次，
# 自担保管）——generic 形态没有 vault，这是与 vault 形态发放面的唯一区别；
# 校验面完全一致（tokens.json 只存 SHA-256 哈希）。
#
# 用法：
#   bash docker/mt-token.sh gen <name> [ttl]    # 发放（默认 720h=30天；0=永不过期）
#   bash docker/mt-token.sh list
#   bash docker/mt-token.sh revoke <name>       # 吊销（运行中容器 <5s 生效，无需重启）
#   bash docker/mt-token.sh rotate <name>       # 轮换（plain 条目无需 vault）
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNTIME="$HERE/runtime"
IMAGE="moretoken:latest"

die() { printf '\033[31m[mt-token] %s\033[0m\n' "$*" >&2; exit 1; }

CMD="${1:-}"
NAME="${2:-}"
TTL="${3:-720h}"
[ -n "$CMD" ] || die "用法: mt-token.sh gen <name> [ttl] | list | revoke <name> | rotate <name>"
docker image inspect "$IMAGE" >/dev/null 2>&1 || die "镜像 $IMAGE 不存在——先跑 bash docker/mt-up.sh"
mkdir -p "$RUNTIME"

# Linux 宿主：以当前用户身份写（bind mount 里的文件属主=宿主 uid，避免 10001 属主
# 让宿主后续没法编辑）。Docker Desktop（Win/mac）映射透明，不加。
USER_ARGS=()
if [ "$(uname -s)" = "Linux" ]; then
  USER_ARGS=(--user "$(id -u):$(id -g)")
fi

run() {
  # MSYS 路径转换双坑（实测教训，2026-09-29）：Git Bash 把 POSIX 参数转成 Windows
  # 路径再递给 docker.exe——`/rt/tokens.json` 会变成 `D:/Git/rt/tokens.json` 传进容器
  # （登记失败 mkdir D:）。对策：①挂载源显式转 Windows 形态（cygpath -m；Linux 无
  # cygpath 则原样，本就是 POSIX 路径）②MSYS_NO_PATHCONV=1 关掉其余参数的转换
  # （该变量只在 Git Bash 有意义，Linux 忽略）。
  local RT
  RT="$(cygpath -m "$RUNTIME" 2>/dev/null || echo "$RUNTIME")"
  MSYS_NO_PATHCONV=1 docker run --rm --entrypoint moretoken "${USER_ARGS[@]}" \
    -v "$RT:/rt" "$IMAGE" "$@"
}

case "$CMD" in
  gen)
    [ -n "$NAME" ] || die "gen 需要 <name>（每客户端独立身份，审计的根基）"
    run -token-gen -token-name "$NAME" -token-plain -token-ttl "$TTL" -tokens-file /rt/tokens.json
    echo "客户端接入：把上面的 TOKEN 填进 API Key 位（Authorization: Bearer <token> 或 x-api-key）"
    ;;
  list)
    run -token-list -tokens-file /rt/tokens.json
    ;;
  revoke)
    [ -n "$NAME" ] || die "revoke 需要 <name>"
    run -token-revoke -token-name "$NAME" -tokens-file /rt/tokens.json
    echo "运行中的容器经目录挂载热加载，下一请求即 401（无需重启）。"
    ;;
  rotate)
    [ -n "$NAME" ] || die "rotate 需要 <name>"
    # rotate 的意义是"新明文入墙、指针不变、客户端零动作"——**依赖 vault**。
    # plain 条目 rotate 会生成一个**无人知道明文**的新 token（哈希入库、明文即丢），
    # 等于自造废条目。诚实拒绝并指路，不做半吊子操作。
    die "generic 形态（plain token）不支持 rotate：新明文无处安放（没有 vault）。
  换新值请用两步：bash docker/mt-token.sh revoke $NAME && bash docker/mt-token.sh gen $NAME
  （vault 形态用宿主 CLI: bin/mt-host.exe -token-rotate -token-name $NAME …）"
    ;;
  *)
    die "未知子命令 $CMD（gen|list|revoke|rotate）"
    ;;
esac
