#!/usr/bin/env bash
# SPT 幂等引入脚本。
# 用法：在新项目目录下执行  bash <模板路径>/scripts/init.sh
#
# 行为：
#   - 首次引入：复制静态资产（CLAUDE.md / INDEX.md / scripts / settings / 约束 / 模板 / .gitignore），
#     编译 ai-guard，写入已引入标记 .spt-initialized。
#   - 重复引入：不覆盖任何已存在文件（保护用户定制），仅补齐缺失的静态模板件并重编译。
#   - 只复制"可复用资产"，绝不复制模板自身的 docs 历史（TASKS/SESSION-STATE/GOALS 等由 INIT.md 引导新建）。
set -euo pipefail

TEMPLATE="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$(pwd)"
MARKER="$DEST/.spt-initialized"

MODE="first"
if [ -f "$MARKER" ]; then
  MODE="upgrade"
fi

echo "=== SPT 引入（模式: $MODE） ==="
echo "  模板: $TEMPLATE"
echo "  目标: $DEST"
echo ""

# copy_if_missing <file-or-dir>...  — 目标已存在则跳过（绝不覆盖定制）
copy_if_missing() {
  for item in "$@"; do
    if [ -e "$DEST/$item" ]; then
      [ "$MODE" = "upgrade" ] && echo "  - 已存在，跳过: $item"
    else
      mkdir -p "$DEST/$(dirname "$item")"
      cp -r "$TEMPLATE/$item" "$DEST/$item"
      echo "  + 复制: $item"
    fi
  done
}

# 静态可复用资产
copy_if_missing CLAUDE.md INDEX.md .golangci.yml .claude/settings.json docs/constraints templates scripts

# upgrade：同步模板 scripts 源码（go.mod / cmd / internal / 构建脚本），
# 保留目标项目 scripts/ 下任何用户自定义文件（bin 由 build 重建）。
if [ "$MODE" = "upgrade" ] && [ -d "$DEST/scripts" ]; then
  for src in go.mod build.sh build.ps1 hook.sh init.sh test-all.sh template-gitignore; do
    if [ -f "$TEMPLATE/scripts/$src" ]; then
      cp "$TEMPLATE/scripts/$src" "$DEST/scripts/$src"
    fi
  done
  cp -r "$TEMPLATE/scripts/cmd/." "$DEST/scripts/cmd/" 2>/dev/null || true
  cp -r "$TEMPLATE/scripts/internal/." "$DEST/scripts/internal/" 2>/dev/null || true
fi

# 排除编译产物，交给 build.sh 重新生成。仅当 scripts 目录确认是模板引入时
# 清理，且只删模板产物 ai-guard.exe——绝不删除已有项目自建 scripts/bin 中的用户文件。
if grep -q "^module spt/scripts" "$DEST/scripts/go.mod" 2>/dev/null; then
  rm -f "$DEST/scripts/bin/ai-guard.exe" "$DEST/scripts/ai-guard.exe"
fi

# 根 .gitignore（仅目标缺失时生成）
if [ ! -f "$DEST/.gitignore" ]; then
  cp "$TEMPLATE/scripts/template-gitignore" "$DEST/.gitignore"
  echo "  + 生成: .gitignore"
fi

# 编译工具链
echo ""
echo "=== 编译 ai-guard ==="
( cd "$DEST/scripts" && bash ./build.sh )
echo "  ✅ $(cd "$DEST" && bash scripts/hook.sh version 2>/dev/null || echo 'ai-guard 编译失败')"

# 已引入标记
TMPL_VERSION="$(grep -m1 '^## \[v' "$TEMPLATE/CHANGELOG.md" 2>/dev/null | sed 's/^## //' || echo 'unknown')"
printf '%s\n' "$TMPL_VERSION" > "$MARKER"
echo "  + 标记: $MARKER"

echo ""
echo "✅ SPT 引入完成。"
echo "   下一步（新项目）: 读 INIT.md 建立 docs 骨架并 begin-task"
echo "   下一步（已有项目）: 读 INIT-EXISTING.md 评估成熟度并接入"
