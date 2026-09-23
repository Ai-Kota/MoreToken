#!/usr/bin/env bash
# Hook wrapper for ai-guard. Soft-fails when the binary is missing (e.g. a fresh
# project before the first build), so a missing toolchain never locks the
# project: PreToolUse hooks exit 0 (allow) instead of 127 (block).
#
# Usage (from .claude/settings.json): bash scripts/hook.sh <ai-guard-args...>
set -euo pipefail

# Resolve repo root = parent of scripts/.
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

BIN="scripts/bin/ai-guard"
if [ ! -x "$BIN" ]; then
  echo "[spt] 警告: ai-guard 缺失（$BIN），钩子已软跳过——约束未生效。请运行 bash scripts/build.sh" >&2
  exit 0
fi

exec "$BIN" "$@"
