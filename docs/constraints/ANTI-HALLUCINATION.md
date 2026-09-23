# 反幻觉协议

> **核心原则：AI 不知道的东西，绝不猜测、绝不编造、绝不"顺便"实现。**
> 任何引用（函数、API、变量、文件、配置、依赖）必须在**使用前**验证真实存在。

---

## 一、为什么需要反幻觉协议

| 幻觉类型 | 典型表现 | 后果 | 发生概率 |
|---------|---------|------|---------|
| **函数幻觉** | 调用不存在的 `utils.ParseConfig()` | 编译失败、运行时 panic | 极高 |
| **API 幻觉** | 假设库有 `client.GetUserByEmail()` | 编译失败、版本不兼容 | 高 |
| **配置幻觉** | 读取不存在的 `config.Database.Timeout` | 启动失败、默认值错误 | 高 |
| **文件幻觉** | 引用不存在的 `internal/auth/token.go` | 导入失败、构建中断 | 中 |
| **依赖幻觉** | 添加不存在的 `github.com/x/y/v2` | `go mod tidy` 失败 | 中 |
| **范围幻觉** | "顺便重构了 User 模块" | 范围蔓延、破坏稳定代码 | 极高 |

---

## 二、强制验证清单（编码前必须逐项执行）

### 2.1 函数/方法调用验证

```bash
# Go: 搜索函数定义
grep -rn "func FunctionName" --include="*.go" src/ pkg/ internal/

# Python: 搜索函数定义
grep -rn "def function_name" --include="*.py" src/

# TypeScript/JS: 搜索函数定义
grep -rn "function functionName\|const functionName = \|functionName(" --include="*.ts" --include="*.js" src/
```

**判定规则**：
- ✅ 找到定义 → 可调用，记录文件路径和行号
- ❌ 未找到定义 → **停止编码**，在 SESSION-STATE.md 记录「待决策项」，等待人工确认或补全实现

### 2.2 接口/类型验证

```bash
# Go: 搜索接口/结构体定义
grep -rn "type InterfaceName interface\|type StructName struct" --include="*.go" src/

# TypeScript: 搜索接口/类型定义
grep -rn "interface InterfaceName\|type TypeName =" --include="*.ts" src/
```

**判定规则**：
- ✅ 找到定义 → 可使用，确认字段/方法签名匹配
- ❌ 未找到 → **禁止编造字段**，必须先定义或确认外部依赖

### 2.3 配置/环境变量验证

```bash
# 搜索配置文件中的键
grep -rn "config_key\|CONFIG_KEY" --include="*.yaml" --include="*.yml" --include="*.json" --include="*.toml" config/ deploy/ .env*
```

**判定规则**：
- ✅ 存在于配置模板/文档 → 可使用
- ❌ 不存在 → **禁止编造新配置键**，必须先在配置模板中定义并文档化

### 2.4 外部依赖验证

```bash
# Go: 验证模块存在
go doc github.com/package/path@latest 2>&1 | head -10

# Node.js: 验证包存在
npm view package-name@latest version 2>&1

# Python: 验证包存在
pip index versions package-name 2>&1
```

**判定规则**：
- ✅ 注册表返回版本信息 → 可添加依赖
- ❌ 404/不存在 → **禁止添加**，检查包名拼写或寻找替代方案

### 2.5 文件/路径验证

```bash
# 确认文件存在
test -f "path/to/file.go" && echo "exists" || echo "NOT FOUND"

# 确认目录存在
test -d "path/to/dir" && echo "exists" || echo "NOT FOUND"
```

**判定规则**：
- ✅ 存在 → 可引用/导入
- ❌ 不存在 → **禁止假设存在**，检查路径拼写或先创建文件

---

## 三、反范围蔓延协议（防止"顺便"重构）

### 3.1 任务边界锁定

每个任务开始前，必须在任务文档中明确：

| 边界项 | 必须明确 | 违例处理 |
|-------|---------|---------|
| **修改文件白名单** | 列出允许修改的具体文件/目录 | 修改白名单外文件 → 立即回滚，记录违规 |
| **禁止修改清单** | 列出绝对不可碰的文件/模块 | 触碰禁止清单 → 立即停止，人工介入 |
| **新增文件规则** | 仅允许在指定目录创建指定类型文件 | 违规创建 → 删除，重新评估 |

### 3.2 变更范围检测 Hook

```bash
# .claude/hooks/check-scope.sh
#!/bin/bash
TASK_ID=$(cat .claude/current_task.txt 2>/dev/null || echo "")
ALLOWED_FILES=$(grep "允许修改文件" "docs/tasks/T-${TASK_ID}.md" | sed 's/.*://' | tr ',' '\n')

for file in $(git diff --name-only); do
    if ! echo "$ALLOWED_FILES" | grep -q "^$file$"; then
        echo "❌ 范围违规: $file 不在任务 $TASK_ID 允许修改清单中"
        exit 1
    fi
done
```

---

## 四、验证失败处理流程

```
验证失败（找不到函数/配置/文件/依赖）
    ↓
1. 立即停止当前编码任务
    ↓
2. 在 SESSION-STATE.md → "待决策项" 记录：
   - 缺失项名称
   - 预期位置
   - 搜索过的路径
   - 建议方案 A/B
    ↓
3. 标记当前任务为 ❌ 阻塞
    ↓
4. 等待人工决策或补全缺失实现
    ↓
5. 验证通过后，继续执行
```

---

## 五、常见幻觉模式识别与对策

| 幻觉模式 | 识别信号 | 对策 |
|---------|---------|------|
| **"标准库一定有这个函数"** | 未验证直接调用 `strings.NewReplacer` 等 | 必须 `go doc strings.NewReplacer` 验证 |
| **"这个库通常都有这个方法"** | 基于经验假设第三方库 API | 必须查阅官方文档/源码验证 |
| **"配置文件里肯定有这个字段"** | 未 grep 直接引用配置键 | 必须搜索现有配置文件确认 |
| **"我记得这个文件在 src/utils 里"** | 凭记忆引用文件路径 | 必须 `ls` 或 `find` 验证路径 |
| **"顺便把这个也优化了"** | 任务完成前修改无关代码 | 触发范围检测 Hook 拦截 |
| **"测试里 mock 一下就行"** | 为不存在的接口写测试 | 先确认接口存在，再写测试 |

---

## 六、自动化验证工具

### 6.1 预编码检查脚本

```bash
# scripts/pre-code-check.sh TASK_ID
#!/bin/bash
TASK_FILE="docs/tasks/T-${1}.md"
if [[ ! -f "$TASK_FILE" ]]; then
    echo "任务文件不存在: $TASK_FILE"
    exit 1
fi

# 提取任务要求的依赖/接口/配置
REQUIRED_FUNCS=$(grep "需要的函数" "$TASK_FILE" | sed 's/.*://')
REQUIRED_CONFIGS=$(grep "需要的配置" "$TASK_FILE" | sed 's/.*://')

# 验证每个函数
for func in $REQUIRED_FUNCS; do
    if ! grep -rq "func $func" --include="*.go" src/; then
        echo "❌ 缺失函数: $func"
        exit 1
    fi
done

# 验证每个配置
for config in $REQUIRED_CONFIGS; do
    if ! grep -rq "$config" config/ deploy/; then
        echo "❌ 缺失配置: $config"
        exit 1
    fi
done

echo "✅ 预编码检查通过"
```

### 6.2 编码后验证脚本

```bash
# scripts/post-code-check.sh
#!/bin/bash
# 检查代码中引用的所有外部符号是否存在

# 提取所有函数调用（简化版）
grep -rn "\.\([A-Z][a-zA-Z0-9]*\)(" --include="*.go" src/ | \
sed 's/.*\.\([A-Z][a-zA-Z0-9]*\)(.*)/\1/' | sort -u | \
while read func; do
    if ! grep -rq "func $func" --include="*.go" src/ pkg/ internal/; then
        echo "⚠️  疑似幻觉函数调用: $func"
    fi
done
```

---

## 七、版本记录

| 日期 | 版本 | 变更内容 | 变更人 |
|------|------|---------|--------|
| 2026-07-26 | v1.0 | 初始创建，建立验证清单与处理流程 | AI |