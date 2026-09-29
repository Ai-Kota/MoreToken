#!/bin/sh
# MoreToken 容器入口（T-026）。职责单一：**缺文件就响亮地死，绝不静默裸奔**。
#
# 为什么必须挡在 moretoken 之前：-tokens-file 显式给出而文件缺失时，旧行为是
# 空名单→不鉴权→healthcheck 全绿的裸奔网关（评审 S3，最坏失败模式是安静的）。
# main.go 已加 fail-closed 兜底，这里是第一道 + 人话指路。
set -eu

CONFIG=/run/moretoken/config.json
TOKENS=/run/mt-tokens/tokens.json

fail() {
    printf '\033[31m[moretoken-entrypoint] %s\033[0m\n' "$1" >&2
    printf '[moretoken-entrypoint] 先在宿主跑 docker/mt-deploy.sh（解析 vault → 投递命名卷），再 compose up。\n' >&2
    exit 1
}

[ -f "$CONFIG" ] || fail "缺少已解析 config：$CONFIG（命名卷未投递）"
[ -r "$CONFIG" ] || fail "config 不可读（uid $(id -u)）：$CONFIG —— 检查卷内文件 0600/uid 10001"
[ -f "$TOKENS" ] || fail "缺少 tokens.json：$TOKENS（目录挂载源 docker/runtime/tokens.json 不存在？）"
[ -r "$TOKENS" ] || fail "tokens.json 不可读（uid $(id -u)）：$TOKENS"

exec "$@"
