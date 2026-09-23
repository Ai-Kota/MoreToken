#!/usr/bin/env bash
# Build ai-guard unified binary
# Usage: ./build.sh [output_dir]
#
# Output:
#   bin/
#     └── ai-guard              # Unified CLI for all constraint tools

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="${1:-$SCRIPT_DIR/bin}"

# Detect OS
OS="$(uname -s)"
case "${OS}" in
    Linux*)     OS="linux";;
    Darwin*)    OS="darwin";;
    MINGW*|MSYS*|CYGWIN*)  OS="windows";;
    *)          echo "Unsupported OS: ${OS}"; exit 1;;
esac

# Detect architecture
ARCH="$(uname -m)"
case "${ARCH}" in
    x86_64|amd64)   ARCH="amd64";;
    aarch64|arm64)  ARCH="arm64";;
    *)              echo "Unsupported arch: ${ARCH}"; exit 1;;
esac

echo "Building ai-guard for ${OS}/${ARCH}..."
echo "Output: ${OUTPUT_DIR}/"
echo ""

# Create output directory
mkdir -p "${OUTPUT_DIR}"

cd "${SCRIPT_DIR}"

echo -n "  Building ai-guard... "
EXT=""
if [ "${OS}" = "windows" ]; then
    EXT=".exe"
fi
# Inject a reproducible version: release tag (from CHANGELOG) + git short hash.
# When neither is available (fresh project, no git), fall back to the source
# constant instead of emitting "dev-dev".
RELEASE="$(grep -m1 '^## \[v' "${SCRIPT_DIR}/../CHANGELOG.md" 2>/dev/null | sed -E 's/^## \[([^]]+)\].*/\1/; s/^v//' || echo 'dev')"
GIT_HASH="$(git rev-parse --short HEAD 2>/dev/null || echo 'dev')"
LDFLAGS=""
if [ "${RELEASE}" != "dev" ] || [ "${GIT_HASH}" != "dev" ]; then
    LDFLAGS="-X main.Version=${RELEASE}-${GIT_HASH}"
fi
if GOOS="${OS}" GOARCH="${ARCH}" go build -ldflags "${LDFLAGS}" -o "${OUTPUT_DIR}/ai-guard${EXT}" "./cmd/ai-guard"; then
    echo "✅"
else
    echo "❌"
    exit 1
fi

echo ""
echo "✅ Build successful!"
echo ""
ls -lh "${OUTPUT_DIR}/"
echo ""
echo "Quick start:"
echo "  ${OUTPUT_DIR}/ai-guard project-assess \"构建一个 REST API\""
echo "  ${OUTPUT_DIR}/ai-guard code-quality-gate"
echo "  ${OUTPUT_DIR}/ai-guard verify-task T-001"
