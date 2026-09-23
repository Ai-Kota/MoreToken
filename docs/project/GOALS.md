# GOALS — MoreToken 重构版

> 更新：2026-08-28 · 重构蓝本：FreeLLMAPI（本机验证稳定的网关）

## 为什么存在

第一性原理：**降低开发成本**。免费模型为原则主力，付费模型仅"救生艇"；会话永不掉线；
代码完全属于我（Go 单二进制，无第三方依赖）。

## 参考对象反思（2026-08-28 定锚）

- 旧参考 **OmniRoute auto**：加权评分池 + bandit 随机探索 + 灰色目录（517 条 tos=avoid/caution）
  + fail-open 回退付费——**实测不稳定**（opencode 单上游 + 单账号，429 打满 = 全死）。
  → 放弃作为参考。
- **FreeLLMAPI**：本机验证稳定一周的核心机制 = **多 key 轮换**（40+ key 吸收账号级限流）
  + 健康检查 + 冷却 + 精选池 + 协议适配 + 失败回退链。MIT 可合法借鉴。
  → 作为重构蓝本。

## 核心目标（不变量）

1. **省钱确定性**：任一免费健康就走免费层；评分只在免费层内选优；付费绝不与免费混池
2. **会话不掉**：恒定端点 + 同格式 fallback + 请求级重试安全边界
3. **属于我**：Go 1.25 纯标准库 · 单二进制 · 显式逻辑（非黑盒评分）

## 技术栈

- Go 1.25 · 纯标准库（net/http / encoding/json）· 单二进制 · 零外部依赖
- NATS 状态总线（前端 WorkBoard 订阅，不内置 dashboard）

## 边界（明确不做什么）

| 不做 | 原因 |
|------|------|
| 免费目录聚合管线（爬灰色目录） | FreeLLMAPI 蓝本 = 精选手写池，不做爬虫 |
| bandit 随机探索 / 复杂评分池 | OmniRoute 不稳定之源，砍掉 |
| Dashboard / 多用户 / 计费 | 个人场景；前端走 WorkBoard |
| OAuth（GitHub Copilot 等） | 聚合管线已移除；免费源用 API key 多 key |
| Anthropic↔OpenAI 翻译层 | 用官方 Anthropic 兼容端点直通（零翻译） |
| 不为高并发优化 | 真实负载 = Claude Code 同刻 1-2 active request，单实例性能（Go 内存状态 + 零落库）天然优于 FreeLLMAPI（Node+SQLite 每请求 3 写），无负载触发；不为它加 benchmark/连接池/异步队列等过度设计 |

## 对标 FreeLLMAPI 的限定域（2026-09-12 分析定锚）

完成 T-001~T-005 后，MoreToken **严格优于** FreeLLMAPI 的维度（仅限此域）：
- **省钱确定性**：免费/付费确定性状态机（FreeLLMAPI 无成本分层，bandit 采样会随机碰付费）
- **可维护**：单二进制纯标准库 vs FreeLLMAPI 的 1600 行 router + 48 lib + SQLite + 40+ 迁移
- **运行成本 / 拥有权**：单进程零依赖 vs Node+SQLite+桌面端的 fork 项目

FreeLLMAPI 仍更强的维度（**不追**）：高并发韧性（熔断/45s 时间预算/model-level bench/canary）、全请求落库可观测性、多协议（OpenAI/Anthropic/Responses/Gemini wire）、7 实例容器隔离运维。
→ 结论：对标 = "功能子集 + 成本确定性 + 拥有权"，不是全功能替代。

## 差异化（相对 FreeLLMAPI）

**免费/付费确定性状态机**：免费层选优 → 全免费不可用 → 显式付费链 → 免费恢复健康 ≥T 分钟
确定性切回。这是 FreeLLMAPI 的弱项（评分池，不保证省钱最大化），是重构版的存在意义。

## 架构

详见 `docs/architecture/ARCHITECTURE.md`（v5.0 重构版蓝图）。

## 里程碑

- M1：config 模型 + provider 抽象（多 key）→ 完成 config 模型（T-001 进行中）
- M2：多 key 路由 + 健康检查 + 冷却（T-002）
- M3：协议端点 + NATS 状态发布（T-003）
- M4：免费/付费确定性状态机（T-004）
- M5：WorkBoard 接入（T-005）
