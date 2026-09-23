# 项目计划进度表

> 项目名称：MoreToken（免费优先的会话保持型路由网关）
> 定位：见 `docs/decisions/ADR-003-positioning.md`（免费优先 · 付费兜底 · 确定性回归）
> 最后更新：2026-09-19（对齐 T-009 收口后的实际状态）

---

## 一、项目总览

| 项目 | 说明 |
|------|------|
| **目标** | 免费优先的会话保持型路由网关：Claude Code 只配一次本地端点，底下自动切模型，会话永不掉，免费恢复自动切回 |
| **重构蓝本** | FreeLLMAPI 已验证的机制：多 key 轮换 / 精选免费池 / 健康检查+冷却 / 协议适配 / 失败回退链 |
| **差异化** | 免费/付费**确定性**状态机（相对 FreeLLMAPI 的评分池） |
| **技术栈** | Go 1.25 · 纯标准库 · 单二进制 · 零外部依赖 |

---

## 二、四支柱（ADR-003）

1. **无缝（继承）**：恒定端点 + 请求级重试 + 熔断自愈
2. **免费优先（确定性）**：任一免费健康就走免费，评分只在免费层内选最优
3. **付费兜底（确定性）**：全免费不可用 → 显式付费链，不与免费混池
4. **自动回归（确定性）**：免费恢复健康 ≥T 分钟 → 切回免费

---

## 三、阶段进度

> ⚠️ **本节的旧版（Phase 1-4）描述的是 v4.0「DeepSeek/GLM 直通」形态，已于重构中作废**
> （v4.0 归档于提交 `15d8f68`）。下表把旧阶段映射到实际落地的任务，以免读者以为还有四个阶段待做。

| 旧阶段 | 实际落地 | 状态 |
|--------|---------|:---:|
| Phase 1 Anthropic 端点 | 重构后并入 T-001~T-003（协议端点与格式隔离） | ✅ |
| Phase 2 免费/付费双层层级状态机 | → **T-002**（层级优先 + 同格式 fallback + I7 重试边界）+ **T-004**（确定性状态机） | ✅ |
| Phase 3 自动回归 | → **T-004**（探测驱动 + 连续窗口 ≥T 回归 I4 + 防抖 I5）+ **T-005**（切换留痕 + NATS 广播） | ✅ |
| Phase 4 Anthropic↔OpenAI 翻译层 | **已放弃**——`GOALS.md` 明确不做翻译层：agnes/DeepSeek/GLM 均原生支持 Anthropic 协议，直通转发即可 | ❌ 不适用 |

### 实际任务链（权威进度见 `docs/tasks/TASKS.md`）

| 阶段 | 任务 | 状态 |
|------|------|:---:|
| 骨架 | T-001 config 模型 + provider 抽象 + 多 key 池 | ✅ |
| 路由 | T-002 格式隔离 + tier 优先 + 同格式 fallback + 重试边界 | ✅ |
| 端点 | T-003 `/v1/messages` + `/v1/chat/completions` + `/health` + 决策日志 | ✅ |
| 状态机 | T-004 免费/付费确定性状态机（I4 回归 / I5 防抖） | ✅ |
| 可观测 | T-005 NATS 状态/事件发布 + WorkBoard 契约 | ✅ |
| 验收 | T-006 不变量 I1-I9 故障注入矩阵 | ✅ |
| 工具链 | T-007 ai-guard 存量测试修复 | ✅ |
| 前端 | T-008 WorkBoard 订阅（**跨仓**：`E:/repos/aimly-console`） | 🟡 |
| 收口 | T-009 密钥装载（`vault:` 指针）+ Claude Code 免费层 + 矩阵对齐 + dev-fleet 登记 | ✅ |

---

## 四、已实测验证（2026-09-19）

| 项 | 结果 |
|----|------|
| 密钥装载 | `keys: 79 resolved, 0 unresolved`（agnes 33 + xkiro 11，去重后 45 次 vault exec） |
| `/v1/chat/completions`（OpenAI 路径） | 200，agnes-2.5-flash 正常返回 |
| `/v1/messages`（Anthropic 路径） | 200，标准 message 对象 |
| tool_use（Claude Code 硬需求） | 200，返回 `tool_use` block + `stop_reason: "tool_use"` |
| 流式 | SSE 事件正常透传 |
| `/health` 如实上报 | `keys` 报真实可用数、`unresolved` 报未解析指针数 |

---

## 五、待办

- **T-008**（跨仓）：前端 `aimly-console` 按 `docs/workboard-contract.md` 订阅两主题渲染 WorkBoard
- Claude Code 实接印证：`ANTHROPIC_BASE_URL=http://localhost:8462`、`ANTHROPIC_MODEL=agnes-2.5-flash`（需人工跑一轮）
- 付费层目前与免费层同为 agnes 厂商，平台级故障兜不住；真隔离需一把异厂商付费 key 入库
