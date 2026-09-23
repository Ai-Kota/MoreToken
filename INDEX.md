# 项目索引（AI 入口）

> **AI 执行本项目时，首先读取此文件。**
> 本文件是导航中枢，定义了项目全局信息和所有文档的查找路径。

---

## 约束的三个层次（核心创新）

```
层次 3: 技术强制（Technical Enforcement）← 最强
  └─ .claude/settings.json 权限控制 + Go 二进制 Hooks
  └─ 违规 = 工具拒绝执行，物理上不可能绕过

层次 2: 工具约束（Tool Constraint）← 中等
  └─ CLAUDE.md 行为宪法 + Go 二进制门禁脚本
  └─ 违规 = 无法通过验收，CI 阻断合并

层次 1: 文档指导（Document Guidance）← 基础
  └─ 各类模板文档、清单、规范
  └─ 违规 = 依靠 AI 自觉（不可靠，仅作参考）
```

> **核心原则**：所有层次 1 的规则，必须尽可能下沉到层次 2 和层次 3 实现。

---

## 一、项目基本信息

| 字段 | 值 |
|------|------|
| **项目名称** | [项目名] |
| **项目目标** | [一句话描述] |
| **当前阶段** | [启动/规划/执行/监控/收尾] |
| **当前 Sprint** | Sprint N |
| **最后更新** | YYYY-MM-DD |

---

## 二、AI 执行规则（不可跳过）

### 2.1 强制行为

| # | 规则 | 说明 | 强制执行方式 |
|---|------|------|-------------|
| R1 | **先读后写** | 修改任何文件前，必须先 Read 该文件 | Hook 拦截 |
| R2 | **先设计后编码** | 每个任务必须有设计文档通过后才写代码 | Hook 拦截 |
| R3 | **先测试后完成** | 任务完成前必须运行测试并通过 | verify-task |
| R4 | **状态同步** | 每完成一个任务，更新 TASKS.md 状态 | verify-task |
| R5 | **会话存档** | 会话结束前更新 SESSION-STATE.md | verify-task |
| R6 | **不确定就停** | 遇到歧义/矛盾，停下报告，不猜 | 人工判断 |

### 2.2 禁止行为

| # | 禁止 | 原因 | 强制执行方式 |
|---|------|------|-------------|
| X1 | 禁止修改架构设计而不更新 ARCHITECTURE.md | 架构漂移 | 人工审查 |
| X2 | 禁止跳过测试直接标记任务完成 | 质量失控 | verify-task |
| X3 | 禁止在未更新 TASKS.md 时开始下一个任务 | 状态失同步 | pre-task-check |
| X4 | 禁止引入新的外部依赖而不更新 ADR-002 | 技术债 | settings.json deny |
| X5 | 禁止一次修改超过 5 个文件而不报告 | 范围失控 | 人工判断 |
| X6 | 禁止删除/覆盖未读取的文件 | 数据丢失 | Hook 拦截 |
| X7 | 禁止编造不存在的 API/函数/文件/库 | 幻觉 | 反幻觉协议 |
| X8 | 禁止越权操作 | 权限越界 | settings.json deny |

### 2.3 执行流程

```
收到任务
  ↓
0. 运行 ai-guard pre-task-check T-XXX  ← 【任务开始前置检查】
  ↓
1. 读 INDEX.md（本文件）获取全局上下文
  ↓
2. 读 SESSION-STATE.md 获取上次状态
  ↓
3. 读 TASKS.md 找到目标任务
  ↓
4. 读任务关联的所有文档（设计、接口、依赖）
  ↓
5. 执行任务（写代码、写测试、写文档）
  ↓
6. 运行 ai-guard verify-task T-XXX ← 【强制验收检查】
  ↓
7. 更新 TASKS.md 状态为 ✅
  ↓
8. 更新 SESSION-STATE.md 记录本次成果
```

---

## 三、文档导航

### 3.1 约束文档（最高优先级）

| 文档 | 路径 | 用途 | 何时读 |
|------|------|------|--------|
| **行为宪法** | `CLAUDE.md` | AI 最高行为约束（自动加载） | 每次会话开始 |
| **反幻觉协议** | `docs/constraints/ANTI-HALLUCINATION.md` | 验证外部引用真实性 | 编码前 |
| **授权模型** | `docs/constraints/AUTHORIZATION-MODEL.md` | L0-L6 操作权限 | 越权操作前 |
| **违规处理** | `docs/constraints/VIOLATION-HANDLING.md` | 违规分级、处理、恢复 | 违规时 |
| **紧急协议** | `docs/constraints/EMERGENCY-PROTOCOL.md` | 破玻璃机制 | 紧急情况 |

### 3.2 项目定义（为什么做、做什么）

| 文档 | 路径 | 用途 | 何时读 |
|------|------|------|--------|
| 项目目标 | `docs/project/GOALS.md` | 项目目标、约束、成功标准 | 每次会话开始 |
| ADR-001 范围 | `docs/project/ADR/ADR-001-scope.md` | 项目边界、包含/不包含 | 开始新模块前 |
| ADR-002 技术 | `docs/project/ADR/ADR-002-tech.md` | 技术选型、约束 | 引入新技术前 |
| 架构设计 | `docs/architecture/ARCHITECTURE.md` | 系统架构、数据流、部署 | 设计阶段 |
| 项目结构规范 | `docs/architecture/PROJECT-STRUCTURE.md` | 目录组织、命名规范、文件约定 | 创建新文件前 |
| 模块设计 | `docs/architecture/modules/` | 各模块详细设计 | 开发对应模块时 |

### 3.3 执行管理（做什么、做得怎样）

| 文档 | 路径 | 用途 | 何时读/写 |
|------|------|------|----------|
| **任务清单** | `docs/tasks/TASKS.md` | 所有任务状态（AI主控面板） | 每次会话必读 |
| 任务模板 | `docs/tasks/TASK-TEMPLATE.md` | 新任务的创建规范 | 创建新任务时 |
| 需求追踪 | `docs/tasks/REQUIREMENTS.md` | 需求→设计→代码→测试映射 | 验证完整性时 |
| **会话状态** | `docs/tasks/SESSION-STATE.md` | 跨会话上下文同步 | 会话开始/结束 |

### 3.4 质量保证（做得好不好）

| 文档 | 路径 | 用途 | 何时读/写 |
|------|------|------|----------|
| 测试计划 | `docs/quality/TESTING.md` | 测试策略、命令、覆盖率 | 开发/测试时 |
| 质量门禁 | `docs/quality/GATES.md` | 任务完成的强制检查项 | 每个任务完成前 |
| Code Review | `docs/quality/CODE-REVIEW.md` | 审查标准、检查清单 | 提交 PR 前 |
| 变更日志 | `CHANGELOG.md` | 版本变更记录 | 发布新版本时 |

### 3.5 风险与变更（出问题怎么办）

| 文档 | 路径 | 用途 | 何时读/写 |
|------|------|------|----------|
| **问题汇集** | `docs/TROUBLESHOOTING.md` | 已发生问题的症状→判据→根因→修法；含排查工具箱 | **遇到故障时先搜这里**（按错误原文） |
| **事故集** | `docs/incidents/` | 每起真实事故一份完整案子（现场→定位→根因→修法→验证→教训） | 想知道"这类问题踩过几次、当时判断对不对"时读；一次事故一份，只增不改 |
| 风险登记册 | `docs/risk/RISK-REGISTER.md` | 风险识别、评估、应对 | 每周/发现新风险时 |
| 变更请求 | `docs/risk/CHANGE-REQUEST.md` | 变更申请、影响评估 | 需求变更时 |

### 3.6 工作流（怎么协作）

| 文档 | 路径 | 用途 | 何时读 |
|------|------|------|--------|
| 工作流标准 | `docs/workflow/WORKFLOW.md` | 项目生命周期、Git规范 | 需要了解流程时 |
| 文档流转 | `docs/workflow/DOC-FLOW.md` | 文档创建、评审、归档 | 管理文档时 |
| 模板索引 | `docs/workflow/TEMPLATES.md` | 所有模板的清单 | 创建新文档时 |

---

## 四、关键文件速查

```
最快路径：
  要做什么？           → docs/tasks/TASKS.md
  上次做到哪了？       → docs/tasks/SESSION-STATE.md
  这个任务怎么算完？   → docs/tasks/TASK-TEMPLATE.md 的验收标准
  测试怎么跑？         → docs/quality/TESTING.md
  代码怎么审查？       → docs/quality/CODE-REVIEW.md
  出问题了？           → docs/TROUBLESHOOTING.md（问题汇集 · 按错误原文搜）
  需求变了？           → docs/risk/CHANGE-REQUEST.md
  任务开始前要做什么？ → ai-guard pre-task-check T-XXX
  任务怎么验收？       → ai-guard verify-task T-XXX
  违规了怎么办？       → docs/constraints/VIOLATION-HANDLING.md
  紧急情况怎么办？     → docs/constraints/EMERGENCY-PROTOCOL.md
```

---

## 五、版本记录

| 日期 | 版本 | 变更内容 | 变更人 |
|------|------|---------|--------|
| 2026-07-26 | v2.0 | 脚本重写为 Go 二进制文件，零依赖 | AI |