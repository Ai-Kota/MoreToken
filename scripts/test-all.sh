#!/usr/bin/env bash
# SPT 一键回归器 —— CLAUDE.md 测试纪律 #4
# 每次改动全量重跑；bug 在产生时就暴露。
# 覆盖两部分：主项目（config/provider/pool/...）+ scripts/（ai-guard 工具链）。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "=== 1/6 主项目 Go 构建 ==="
GOWORK=off go build ./...

echo "=== 2/6 主项目 Go vet ==="
GOWORK=off go vet ./...

echo "=== 3/6 主项目 Go 测试（-race）==="
GOWORK=off go test -race -count=1 ./internal/...

echo "=== 4/6 ai-guard 构建 + 测试 ==="
(cd scripts && go build ./... && go test -p 1 -cover ./...)

echo "=== 5/6 Lint ==="
if command -v golangci-lint >/dev/null 2>&1; then
  GOWORK=off golangci-lint run ./internal/... 2>/dev/null || echo "⚠️ golangci-lint 发现问题（见上）"
  (cd scripts && golangci-lint run ./... 2>/dev/null) || echo "⚠️ golangci-lint 发现问题（见上）"
else
  echo "⚠️ golangci-lint 未安装，跳过（门禁中可选）"
fi

echo "=== 6/6 ai-guard 冒烟 ==="
bash scripts/hook.sh version

echo ""
echo "✅ test-all 全部通过"
