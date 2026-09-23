# ADR-001: 项目范围定义

## 状态
已接受（重构版）→ **已被 ADR-003 取代（2026-08-23）**，本文件仅作历史记录。新定位见 `ADR-003-positioning.md`。

## 日期
2026-07-25

## 上下文

### 项目背景

本机已稳定运行 OmniRoute (TypeScript)，实现了 AI 请求的自动路由。需要用 Go 重写 OmniRoute 的 auto 模式，实现相同效果。

### 关键理解（修正）

**原错误理解**：聚合免费模型数据，要求用户提供 API Key

**正确理解**：通过 OAuth 登录 GitHub Copilot、Google 等服务，自动获取免费 token，用户不需要提供任何 API Key

### OmniRoute 核心价值

1. **GitHub Copilot OAuth**：用户登录 GitHub，获取 Copilot token，免费使用 Claude、GPT 等模型
2. **Google Antigravity OAuth**：用户登录 Google，获取 Code Assist token，免费使用 Gemini
3. **Claude OAuth**：用户登录 Claude，获取 token，免费使用 Claude 模型
4. **自动 Token 刷新**：token 过期时自动刷新，用户无感知

### 项目目标

用 Go 重写 OmniRoute auto 模式，实现：
1. OAuth 认证流程（GitHub Device Code、Claude PKCE、Google Authorization Code）
2. Token 存储和自动刷新
3. OpenAI 兼容代理，AI 客户端无需修改
4. 用户只需登录一次，代理自动管理所有 token

### 约束条件

- 语言：Go 1.25+（无外部依赖，纯标准库）
- 运行环境：Windows 11
- 参考实现：`E:\AImlyForge\another\FreeToken\OmniRoute-release-v3.8.49`
- 零外部依赖（不引入第三方 Go 包）

## 决策

### 项目范围包含

1. **OAuth 认证模块**：
   - GitHub Device Code Flow
   - Claude Authorization Code + PKCE
   - Google Antigravity Authorization Code
2. **Token 管理**：
   - Token 存储（JSON 文件）
   - Token 自动刷新
3. **HTTP 代理层**：
   - OpenAI 兼容端点
   - 从 OAuth Manager 获取 token
   - 转发请求到 AI 服务
4. **Provider 路由**：
   - 基于 token 可用性选择 provider
   - 失败时自动降级

### 项目范围不包含

1. 不做 Web UI Dashboard（只做简单的登录页面）
2. 不做用户认证（本地服务）
3. 不做持久化数据库（JSON 文件存储）
4. 不做所有 OAuth provider（首版只支持 GitHub、Claude、Google）

### 分阶段范围

| 阶段 | 范围 | 覆盖率 |
|---|---|---|
| Phase 1 | GitHub Copilot OAuth + 基础代理 | ~60% 免费模型 |
| Phase 2 | Claude + Google OAuth | ~90% |
| Phase 3 | 其他 provider（Cursor、Windsurf 等） | ~95% |

## 理由

- **Go + 零依赖**：与 OmniRoute (TS) 互补，部署简单，单二进制
- **OAuth 优先**：真正实现"免费"，用户无需提供 API Key
- **参考 OmniRoute 实现**：不创新，照着原型的思路和方法

## 影响

- **正面**：
  - 用户体验好，无需配置 API Key
  - 真正免费使用 AI 模型
  - 与 OmniRoute 功能对齐
- **负面**：
  - OAuth 流程复杂，需要处理 token 刷新
  - 需要本地服务器接收回调
  - 部分 provider 可能限制第三方使用

## 替代方案

| 方案 | 优点 | 缺点 | 结论 |
|------|------|------|------|
| 原设计（API Key） | 实现简单 | 用户需要提供 Key，不是真正免费 | 放弃 |
| 直接用 OmniRoute | 功能完整 | TS 生态，与 Go 技术栈不一致 | 已在用，Go 版本是补充 |
| Go + OAuth | 真正免费，用户体验好 | 实现复杂 | **采纳** |
