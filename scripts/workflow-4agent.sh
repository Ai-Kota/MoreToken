#!/usr/bin/env bash
# 4-Agent Pipeline Workflow
# Implements the 4-stage pipeline: Requirements → Coding → Refactoring → Review
#
# Usage: ./workflow-4agent.sh "项目需求描述" [--stage <1-4>]
#
# Stages:
#   1. Requirements normalization (Agent 1)
#   2. Code generation (Agent 2)
#   3. Refactoring (Agent 3)
#   4. Architecture review + testing (Agent 4)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DIR="${SCRIPT_DIR}/bin"
AI_GUARD="${BIN_DIR}/ai-guard"

# Parse arguments
REQUIREMENT=""
START_STAGE=1

while [[ $# -gt 0 ]]; do
    case $1 in
        --stage)
            START_STAGE="$2"
            shift 2
            ;;
        *)
            REQUIREMENT="$1"
            shift
            ;;
    esac
done

if [[ -z "$REQUIREMENT" ]]; then
    echo "Usage: $0 \"项目需求描述\" [--stage <1-4>]"
    echo ""
    echo "Examples:"
    echo "  $0 \"构建一个 REST API 用户管理系统\""
    echo "  $0 \"添加用户认证模块\" --stage 2"
    exit 1
fi

echo "=================================================="
echo "🚀 4-Agent 流水线"
echo "=================================================="
echo ""
echo "需求: $REQUIREMENT"
echo "起始阶段: $START_STAGE"
echo ""

# Stage 1: Requirements Normalization
if [[ $START_STAGE -le 1 ]]; then
    echo "=================================================="
    echo "📋 阶段 1: 需求规范化 (Agent 1)"
    echo "=================================================="
    echo ""
    echo "Agent 1 职责: 将模糊需求转为死规矩，杜绝逻辑漏洞"
    echo ""

    # Run project assessment
    echo "1.1 项目评估..."
    ${AI_GUARD} project-assess "${REQUIREMENT}"
    echo ""

    # Create requirements document
    echo "1.2 生成需求文档..."
    mkdir -p docs/requirements
    cat > docs/requirements/REQ-001.md << EOF
# 需求 REQ-001: ${REQUIREMENT}

## 状态
进行中

## 需求描述
${REQUIREMENT}

## 验收标准
- [ ] 功能完整实现
- [ ] 单元测试通过
- [ ] 代码质量达标（复杂度 ≤ 10，重复率 ≤ 5%）
- [ ] 文档完整

## 技术约束
- 使用项目现有技术栈
- 遵循项目编码规范
- 不引入未审批的依赖

## 优先级
P0

## 创建日期
$(date +%Y-%m-%d)
EOF

    echo "   ✅ 需求文档已生成: docs/requirements/REQ-001.md"
    echo ""

    # Generate test cases from requirements
    echo "1.3 生成测试用例..."
    ${AI_GUARD} test-designer docs/requirements/REQ-001.md
    echo ""

    echo "✅ 阶段 1 完成"
    echo ""
    echo "下一步: 确认需求后，运行 $0 \"$REQUIREMENT\" --stage 2"
    exit 0
fi

# Stage 2: Code Generation
if [[ $START_STAGE -le 2 ]]; then
    echo "=================================================="
    echo "💻 阶段 2: 代码生成 (Agent 2)"
    echo "=================================================="
    echo ""
    echo "Agent 2 职责: 根据需求写基础代码"
    echo ""

    # Check if requirements exist
    if [[ ! -f "docs/requirements/REQ-001.md" ]]; then
        echo "❌ 需求文档不存在，请先运行阶段 1"
        exit 1
    fi

    echo "2.1 读取需求文档..."
    cat docs/requirements/REQ-001.md
    echo ""

    echo "2.2 执行 pre-task-check..."
    # Create task if not exists
    if [[ ! -f "docs/tasks/TASKS.md" ]]; then
        mkdir -p docs/tasks
        cat > docs/tasks/TASKS.md << 'EOF'
# 任务清单

| ID | 任务 | 优先级 | 状态 | 依赖 |
|:--:|------|:------:|:----:|:----:|
| T-001 | REQ-001 实现 | P0 | 🔄 | — |
EOF
    fi

    echo "   ✅ 任务已创建"
    echo ""

    echo "2.3 开始编码..."
    echo "   (AI 自主完成编码工作)"
    echo ""

    echo "✅ 阶段 2 启动完成"
    echo ""
    echo "下一步: 编码完成后，运行 $0 \"$REQUIREMENT\" --stage 3"
    exit 0
fi

# Stage 3: Refactoring
if [[ $START_STAGE -le 3 ]]; then
    echo "=================================================="
    echo "🔧 阶段 3: 重构优化 (Agent 3)"
    echo "=================================================="
    echo ""
    echo "Agent 3 职责: 优化代码质量（复杂度、重复、命名）"
    echo ""

    echo "3.1 运行代码质量检查..."
    ${AI_GUARD} code-quality-gate
    QUALITY_EXIT=$?

    if [[ $QUALITY_EXIT -ne 0 ]]; then
        echo ""
        echo "⚠️  代码质量未达标，需要重构"
        echo "   (AI 自动优化代码质量)"
    else
        echo ""
        echo "   ✅ 代码质量达标"
    fi

    echo ""

    echo "✅ 阶段 3 完成"
    echo ""
    echo "下一步: 运行 $0 \"$REQUIREMENT\" --stage 4"
    exit 0
fi

# Stage 4: Architecture Review + Testing
if [[ $START_STAGE -le 4 ]]; then
    echo "=================================================="
    echo "🔍 阶段 4: 架构审查 + 测试验证 (Agent 4)"
    echo "=================================================="
    echo ""
    echo "Agent 4 职责: 架构合规检查 + 运行测试"
    echo ""

    echo "4.1 运行测试..."
    ${AI_GUARD} verify-task T-001
    TEST_EXIT=$?

    echo ""

    echo "4.2 运行代码质量门禁..."
    ${AI_GUARD} code-quality-gate
    GATE_EXIT=$?

    echo ""

    echo "4.3 运行变异测试..."
    ${AI_GUARD} mutation-test
    MUTATION_EXIT=$?

    echo ""

    # Final result
    echo "=================================================="
    echo "📊 最终结果"
    echo "=================================================="
    echo ""

    if [[ $TEST_EXIT -eq 0 && $GATE_EXIT -eq 0 && $MUTATION_EXIT -eq 0 ]]; then
        echo "✅ 全部通过！"
        echo ""
        echo "质量指标:"
        echo "  - 测试: ✅ 通过"
        echo "  - 代码质量: ✅ 达标"
        echo "  - 变异测试: ✅ 通过"
        echo ""
        echo "下一步: 更新 TASKS.md 状态为 ✅"
    else
        echo "❌ 部分检查未通过"
        echo ""
        echo "检查结果:"
        [[ $TEST_EXIT -eq 0 ]] && echo "  - 测试: ✅ 通过" || echo "  - 测试: ❌ 失败"
        [[ $GATE_EXIT -eq 0 ]] && echo "  - 代码质量: ✅ 达标" || echo "  - 代码质量: ❌ 未达标"
        [[ $MUTATION_EXIT -eq 0 ]] && echo "  - 变异测试: ✅ 通过" || echo "  - 变异测试: ❌ 未通过"
        echo ""
        echo "下一步: 修复问题后重新运行阶段 3-4"
    fi

    echo ""
    echo "=================================================="
fi
