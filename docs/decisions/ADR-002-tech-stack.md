# ADR-002: 技术选型

## 状态
已接受（重构版）· 技术选型在新定位下仍成立，当前项目范围见 [ADR-003-positioning.md](ADR-003-positioning.md)

## 日期
2026-07-25

## 上下文

需要为 Free Tier Aggregator 项目选择技术栈。

### 考虑因素

1. 必须用 Go（与 AImlyForge 技术栈一致）
2. 必须零外部依赖（Go 标准库 only）
3. 需要支持 OAuth 流程（Device Code、Authorization Code + PKCE）
4. 需要 HTTP 服务器 + SSE 流式转发
5. 需要 Token 存储和自动刷新

## 决策

### 后端

| 技术 | 选择 | 理由 |
|------|------|------|
| 语言 | Go 1.25 | 与 AImlyForge 技术栈一致 |
| HTTP 服务器 | `net/http` 标准库 | 零依赖，功能足够 |
| JSON 解析 | `encoding/json` 标准库 | 原生支持，无需第三方 |
| SSE 流式 | `bufio.Scanner` + 手动解析 | 标准库无 SSE 支持，需自行实现 |
| OAuth | 手动实现 | 标准库无 OAuth 支持，参考 OmniRoute |
| PKCE | `crypto/rand` + `encoding/base64` | 标准库支持 |
| Token 存储 | JSON 文件 | 简单，与现有 unified.json 一致 |
| 日志 | `log` 标准库 | 零依赖，够用 |

### OAuth 流程

| Provider | 流程类型 | 理由 |
|----------|----------|------|
| GitHub Copilot | Device Code | 无需本地服务器，用户体验好 |
| Claude | Authorization Code + PKCE | 安全，参考 OmniRoute |
| Google Antigravity | Authorization Code | 参考 OmniRoute |

### 架构模式

| 模式 | 选择 | 理由 |
|------|------|------|
| 架构风格 | HTTP 反向代理 | 客户端无感知，透明转发 |
| Token 管理 | OAuth Manager | 统一管理所有 provider 的 token |
| 路由策略 | 基于 token 可用性 | 有 token 才能使用 provider |
| 配置管理 | JSON 文件 + 环境变量 | 简单，安全 |

### 部署方式

| 方式 | 选择 | 理由 |
|------|------|------|
| 构建 | `go build` 单二进制 | 部署简单，无运行时依赖 |
| 运行 | 本地进程 | 个人使用，无需容器化 |
| 端口 | `:8462` | 可配置，默认端口 |
| 回调端口 | `:8463` | OAuth 回调专用 |

## 理由

- **Go 标准库**：`net/http` 足以构建高质量的 HTTP 代理，无需框架
- **零依赖**：安全性高，部署简单，无供应链风险
- **OAuth 实现**：参考 OmniRoute，不创新，照着原型的思路
- **Token 管理**：自动刷新，用户无感知

## 影响

- **正面**：
  - 用户体验好，无需配置 API Key
  - 真正免费使用 AI 模型
  - 单二进制部署，复制即用
- **负面**：
  - OAuth 流程复杂，需要处理 token 刷新
  - 需要本地服务器接收回调
  - 部分 provider 可能限制第三方使用

## 替代方案

| 方案 | 结论 | 理由 |
|------|------|------|
| Go + 第三方 OAuth 库 | 放弃 | 引入外部依赖 |
| 原设计（API Key） | 放弃 | 不是真正免费 |
| Go + 标准库 OAuth | 采纳 | 零依赖，参考 OmniRoute |
| Python + FastAPI | 放弃 | 不是 Go 技术栈 |
