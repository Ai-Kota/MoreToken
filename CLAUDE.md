# CLAUDE.md — AI 行为宪法

> **本文件会被 Claude Code 自动加载并强制生效。**
> 核心理念：**不看过程，只卡结果。用机器审查机器，不用人审查机器。**

---

## 一、四阶段流水线（核心架构）

```
阶段 1: 需求规范化
  └─ Agent 1: 将模糊需求转为死规矩，杜绝逻辑漏洞
  └─ 产出: 需求文档（结构化、可验证）

阶段 2: 代码生成
  └─ Agent 2: 根据需求写基础代码
  └─ 产出: 功能代码 + 单元测试

阶段 3: 重构优化
  └─ Agent 3: 优化代码质量（复杂度、重复、命名）
  └─ 产出: 优化后的代码

阶段 4: 架构审查 + 测试验证
  └─ Agent 4: 架构合规检查 + 运行测试
  └─ 产出: 测试报告 + 质量报告
  └─ 结果: 通过 = 完成，失败 = 回到阶段 2
```

---

## 二、自动化钢铁笼子（机器审查机器）

### 2.1 质量门禁（只看数据，不看代码）

| 检查项 | 工具 | 阈值 | 不通过后果 |
|--------|------|------|-----------|
| 单元测试 | `go test` / `npm test` / `pytest` | 100% 通过 | 阻塞 |
| 代码覆盖率 | `code-quality-gate` | ≥ 80% | 阻塞 |
| 圈复杂度 | `code-quality-gate` | 平均 ≤ 10，最大 ≤ 30 | 阻塞 |
| 代码重复率 | `code-quality-gate` | ≤ 5% | 警告 |
| Lint 检查 | `golangci-lint` / `npm run lint` | 0 错误 | 阻塞 |
| 硬编码密钥 | `code-quality-gate` | 0 处 | 阻塞 |

### 2.2 测试驱动闭环

```
写代码 → 写测试 → 运行测试
  ↑                    ↓
  └── 失败 ←── 反馈 ──┘
  ↓
  通过 → 进入下一阶段
```

### 2.3 变异测试（检测测试质量）

```bash
# 变异测试原理：
# 1. 自动修改代码（引入 bug）
# 2. 运行测试
# 3. 如果测试通过 → 测试不够好（没抓住 bug）
# 4. 如果测试失败 → 测试有效
# 变异分数 = 被抓住的 bug / 总 bug ≥ 70%
```

### 2.4 测试纪律（契约优先 · 功能矩阵 · 一键回归）

> **测试是行为的契约 + 完整性的定义，不是事后验证。** 每轮测试都漏 bug 的根因：测试沿用产生 bug 的同一套假设、增量补漏、覆盖行≠值正确。

| # | 规则 | 落地 |
|---|------|------|
| 1 | **功能矩阵为计划单位** | 编码前枚举 功能×正常×空×错误×边界×时序，填 `docs/quality/TEST-MATRIX.md`（模板见 `templates/quality/TEST-MATRIX.md`）。**矩阵 = 验收标准，有未覆盖格 = 阻塞** |
| 2 | **契约优先** | 真实捕获的 artifact（API 响应 / 文件格式 / 事件流）固化为 fixture，测试**从契约生成**，不从写的代码推断 |
| 3 | **分层测试** | 单元(逻辑) / 契约(真实schema) / 集成(真实依赖) / 时序(故障注入)。缺一层 = 漏一类 bug |
| 4 | **一键回归器** | 每个项目建 `test-all` 脚本（后端测试+lint + 前端build+lint + E2E），**每次改动全量重跑**，bug 在产生时就暴露 |
| 5 | **新增功能规则** | 先加矩阵行 + 写测试，再合并。禁止"代码先、测试后补" |
| 6 | **覆盖率是下限不是目标** | 覆盖行 ≠ 值正确；行为正确性由矩阵保证 |

---

## 三、执行规则（核心：什么时候干、什么时候停）

### 3.1 自主执行（不需要问人，闷头干）

以下操作 AI **直接执行，不问用户**：

| 类别 | 操作 | 原因 |
|------|------|------|
| 编码 | 写代码、改代码、重构 | 你的本职工作 |
| 测试 | 写测试、运行测试、修复失败的测试 | 测试驱动是铁律 |
| 构建 | `go build`、`npm run build`、`cargo build` | 构建失败是你的问题 |
| Lint | `golangci-lint`、`npm run lint` | 代码质量是你的责任 |
| Git 提交 | `git add`、`git commit` | 保存进度不需要问 |
| Git 推送 | `git push` | 推送不需要问 |
| 依赖管理 | `go mod tidy`、`npm install`、`pip install` | 项目依赖你决定 |
| 文档 | 更新 README、更新 TASKS.md、更新文档 | 文档是项目的一部分 |
| 质量检查 | `ai-guard code-quality-gate`、`ai-guard verify-task` | 机器审查机器 |
| 进度保存 | `ai-guard checkpoint` | 保存进度不需要问 |

**判断标准：如果这个操作是"把事情做对"的一部分，就直接做。**

### 3.2 必须停下来问的情况

以下情况 AI **必须停止执行，报告给用户**：

| 情况 | 原因 | 怎么做 |
|------|------|--------|
| 新项目/新模块，还没定目录结构 | 代码放错位置后面要重排 | **先输出目录结构方案，用户确认后再写代码** |
| 需求有歧义或矛盾 | 猜着做一定错 | 列出矛盾点，让用户选择 |
| 技术方案有多种合理选择 | 不同选择有不同后果 | 列出方案 A/B，让用户决策 |
| 发现任务范围超出预期 | 可能在做不该做的事 | 报告发现，让用户决定是否扩展范围 |
| 涉及安全/支付/认证 | 后果严重 | 必须有人工确认 |
| 测试覆盖率怎么都上不去 | 可能是设计问题 | 报告瓶颈，讨论是否调整方案 |

**判断标准：如果做错了不可逆、或者后果严重，就停下来问。**

### 3.3 禁止行为

| # | 禁止 | 原因 | 强制方式 |
|---|------|------|---------|
| X1 | 破坏系统环境 | 不可逆 | `env-protect` Hook 拦截 |
| X2 | 修改 Claude Code 配置 | 防止 AI 放宽约束 | `env-protect` Hook 拦截 |
| X3 | 跳过测试直接标完成 | 质量失控 | `verify-task` 阻塞 |
| X4 | 编造不存在的 API/函数 | 幻觉 | 反幻觉协议 |
| X5 | 未 `begin-task` 声明任务就写文件 | 范围失控 | `check-scope` Hook 物理拦截（exit 2） |

---

## 四、自动进度保存（Checkpoint）

### 4.1 什么时候保存

| 时机 | 命令 | 说明 |
|------|------|------|
| 完成一个子任务 | `ai-guard checkpoint T-XXX --summary "完成了 xxx"` | 记录成果 |
| 上下文快用完 | `ai-guard checkpoint --summary "正在做 xxx，下一步 yyy"` | 紧急保存 |
| 遇到阻塞 | `ai-guard checkpoint T-XXX --summary "阻塞在 xxx"` | 记录状态 |
| 会话结束前 | `ai-guard checkpoint` | 最终保存 |

### 4.2 会话开始时

**第一步：读 SESSION-STATE.md**，从上次中断处继续。不要重复已完成的工作。

### 4.3 Checkpoint 输出示例

```
ai-guard checkpoint T-001 --summary "实现了用户注册接口，测试通过" --next "实现登录接口"
```

---

## 五、执行流程

```
收到项目需求
  ↓
0. 运行 ai-guard project-assess "需求描述"
   └─ 输出: 难度、工时、风险、建议
  ↓
1. 你: 确认是否开始（根据评估结果决策）
  ↓
2. AI: 规划目录结构
   └─ 根据需求输出目录方案（cmd / internal / pkg 等）
   └─ 参考 docs/architecture/PROJECT-STRUCTURE.md
   └─ 用户确认后再开始写代码
  ↓
2.5 AI: 运行 ai-guard begin-task T-XXX（声明任务意图，加载边界与验收标准）
   └─ ⚠️ 未 begin-task 时 check-scope 会拒绝写任何业务文件（exit 2，物理强制）
  ↓
3. AI: 运行 ai-guard test-designer docs/requirements.md
   └─ 输出: 测试用例（独立于代码）
  ↓
4. AI: 执行阶段 1-4（代码生成→测试→重构→审查）
   └─ 每完成一个子任务，自动 checkpoint
   └─ 编译/测试失败 → 自己修，不问
   └─ 需求矛盾/范围超出 → 停下来报告
  ↓
5. AI: 运行 ai-guard verify-task T-XXX
   └─ 检查: 测试通过 + 覆盖率 + 复杂度 + Lint
  ↓
6. AI: 运行 ai-guard code-quality-gate
   └─ 输出: 质量报告（数据，不是代码审查）
  ↓
7. 你: 查看质量报告（只看数据，不看代码）
  ↓
8. 通过 → 确认完成
   不通过 → AI 修复后重新验证
```

---

## 六、环境保护

`env-protect` Hook 自动拦截：

| 拦截目标 | 原因 |
|---------|------|
| `~/.bashrc`, `~/.zshrc`, `~/.profile` | 防止 PATH 污染 |
| `/etc/`, `/usr/`, `/bin/` | 防止系统文件破坏 |
| `sudo`, `chmod 777`, `chown` | 防止权限问题 |
| `apt install`, `brew install` | 防止全局包污染 |
| `.claude/settings.json` | 防止 AI 自行放宽约束 |
| `crontab`, `systemctl` | 防止后台进程失控 |
| `docker run --privileged` | 防止容器逃逸 |

---

## 七、标准化约束

### 7.1 Bug 检测（golangci-lint）

格式统一不是 AI 的问题（AI 输出天然一致）。但 bug 检测仍然有价值：

| 规则 | 工具 | 检查什么 | AI 会犯吗 |
|------|------|---------|:---------:|
| 未处理 error | `errcheck` | 函数返回 error 未检查 | ⚠️ 偶尔 |
| 安全漏洞 | `gosec` | SQL 注入、硬编码密码等 | ⚠️ 偶尔 |
| 死代码 | `unused` | 未使用的变量/函数/导入 | ⚠️ 重构时 |
| 静态分析 | `staticcheck` | 高级 bug 模式 | ⚠️ 偶尔 |
| 复杂度过高 | `gocyclo` | 圈复杂度 > 15 | ⚠️ 复杂逻辑 |

配置文件：`.golangci.yml`

### 7.2 项目结构

**详见 `docs/architecture/PROJECT-STRUCTURE.md`**

核心规则：
- 源代码放 `src/<module>/`
- 单元测试放 `src/<module>/*_test.go`（同目录）
- 集成测试放 `tests/`
- 配置文件不放 `src/`
- 根目录不放源代码文件

### 7.3 命名规范（AI 自动遵循，无需配置）

| 语言 | 文件 | 函数 | 变量 | 常量 |
|------|------|------|------|------|
| Go | snake_case.go | PascalCase(导出)/camelCase(内部) | camelCase | PascalCase |
| TS/JS | kebab-case.ts | camelCase | camelCase | UPPER_SNAKE_CASE |
| Python | snake_case.py | snake_case | snake_case | UPPER_SNAKE_CASE |

AI 天然遵循命名规范，不需要 lint 配置来约束。

### 7.4 Commit 规范

遵循 Conventional Commits：`<type>(<scope>): <description>`

| type | 用途 | 示例 |
|------|------|------|
| feat | 新功能 | `feat(user): add login endpoint` |
| fix | Bug 修复 | `fix(auth): handle expired token` |
| docs | 文档 | `docs: update README` |
| refactor | 重构 | `refactor(api): extract validator` |
| test | 测试 | `test: add user service tests` |
| chore | 杂务 | `chore: update dependencies` |

---

## 八、关键工具速查

所有工具统一为 `ai-guard` 二进制的子命令：

| 子命令 | 用途 | 触发方式 |
|--------|------|---------|
| `ai-guard project-assess` | 项目评估（难度/工时/风险） | 手动 |
| `ai-guard test-designer` | 独立测试用例生成 | 手动 |
| `ai-guard code-quality-gate` | 代码质量检查（复杂度/重复/覆盖率） | 手动 |
| `ai-guard verify-task` | 任务验收（测试+Lint+密钥） | 手动 |
| `ai-guard checkpoint` | 保存进度到 SESSION-STATE.md | 自动/手动 |
| `ai-guard assess-maturity` | 评估项目成熟度等级（1-3） | 手动 |
| `ai-guard generate-settings` | 根据成熟度等级生成 settings.json | 手动 |
| `ai-guard env-protect` | 系统环境保护 | 自动（Hook） |
| `ai-guard check-read-before-write` | 先读后写检查 | 自动（Hook） |
| `ai-guard check-design-doc` | 设计文档检查 | 自动（Hook，Level 3） |
| `ai-guard check-scope` | 变更范围检查 | 自动（Hook，Level 2+） |
| `ai-guard pre-task-check` | 任务前置检查 | 手动 |
| `ai-guard begin-task` | 声明当前任务（意图门禁：加载边界+验收标准，check-scope 强制依据） | **每个任务开始必做** |
| `ai-guard end-task` | 清除任务上下文（任务切换闭环） | 手动 |
| `ai-guard session-bootstrap` | SessionStart 上下文快照注入 | 自动（Hook） |
| `ai-guard wu-guard` | WU 粒度（≤5 文件 + 关注点聚类）检查 | 自动（Hook） |
| `ai-guard verify-commit` | 提交前增量测试验收 | 自动（Hook） |
| `ai-guard track-read` | 读取追踪 | 自动（Hook） |
| `ai-guard clear-read` | 清除读取记录 | 自动（Hook） |
| `ai-guard constraint-metrics` | 约束有效性度量 | 手动 |
| `ai-guard mutation-test` | 变异测试 | 手动 |

---

## 九、版本记录

| 日期 | 版本 | 变更内容 |
|------|------|---------|
| 2026-08-25 | v3.5 | WU 最小开发单元 + 约束持久化（SessionStart 注入）+ 门禁实效化（exit 2 阻塞） |
| 2026-07-27 | v3.4 | 渐进式成熟度约束（assess-maturity + generate-settings） |
| 2026-07-26 | v3.3 | 自主执行规则 + 自动 Checkpoint + permission 放宽 |
| 2026-07-26 | v3.2 | 统一二进制 ai-guard，合并13个独立工具 |
| 2026-07-26 | v3.0 | 四阶段流水线 + 自动化钢铁笼子 + 环境保护 |
