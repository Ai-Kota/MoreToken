# 系统架构（v5.0 重构版蓝图）

> 版本：v5.0 · 以 FreeLLMAPI 为蓝本重构
> 定位：`docs/project/GOALS.md` · 任务：`docs/tasks/TASKS.md`
> 创建：2026-08-28 · 取代 v4.0（会话保持型路由网关，已归档）

---

## 一、定位与不变量

**免费优先的会话保持型路由网关**：恒定端点 `:8462`，免费优先省钱、付费兜底、会话不掉、完全属于我（Go 单二进制）。

三个不变量：
1. **省钱确定性**：免费/付费**绝不混池**（评分高的付费模型不抢免费名额）
2. **会话不掉**：恒定端点 + 同格式 fallback + 重试安全边界
3. **属于我**：Go 1.25 纯标准库 · 单二进制 · 显式逻辑

## 二、系统上下文

```
┌───────────┐   NATS    ┌────────────────────────────┐
│ WorkBoard  │◄────────►│  MoreToken 后端 (纯逻辑)     │
│ (前端界面)  │ status/   │  ─────────────────────────── │
└───────────┘ events    │  Router（格式隔离 + 多 key）    │
                        │  Health（周期探测 + 冷却）      │
                        │  State（免费↔付费状态机）       │
                        │  NATS 发布（状态/事件）          │
                        └──────────┬──────────────────┘
                                   │ /v1/messages, /v1/chat/completions
                                   ▼
                     上游：agnes（多key）/ xkiro / cloudflare / deepseek-paid
```

## 三、核心机制（FreeLLMAPI 蓝本）

| 机制 | 说明 | 状态 |
|---|---|---|
| **多 key 轮换** | 每 provider 多把 key，账号级限流被吸收（稳定性的根） | 设计（T-001 config 已支持） |
| **精选免费池** | 只留实测验证的免费源（agnès/xkiro/cloudflare），不做灰色目录 | 设计（config 已含） |
| **健康检查 + 冷却** | 周期探测，失败 key 冷却/降权，恢复后回归 | T-002 |
| **格式隔离** | `/v1/messages`(Anthropic) 与 `/v1/chat/completions`(OpenAI) 路径完全隔离 | 保留（v4.0 好设计） |
| **同格式 fallback** | 失败冷却 → 下一个同格式 provider → 全失败 503 | T-002 |
| **协议适配** | Anthropic 端点零翻译直通（DeepSeek/GLM 官方兼容端点） | 保留 |
| **免费/付费状态机** | 差异化：确定性省钱，FreeLLMAPI 弱项 | T-004 |

## 四、模块结构（目标）

```
moretoken/
├── main.go                 # 入口：config → health → proxy → nats
├── config/config.json      # 声明式配置（provider×key×model+tier）
├── internal/
│   ├── config/             # 配置加载/校验（多 key + tier）   ← 已实现
│   ├── provider/           # provider 抽象（OpenAI/Anthropic 请求）
│   ├── pool/               # 多 key 轮换池（健康 key 选择）
│   ├── health/             # 周期健康探测 + 冷却
│   ├── router/             # 格式隔离选择 + 同格式 fallback
│   ├── proxy/              # /v1/messages + /v1/chat/completions
│   ├── state/              # 免费/付费确定性状态机
│   └── nats/               # NATS 发布（freetier.status/events）
├── docs/                   # GOALS/ARCHITECTURE/tasks（SPT 管理）
└── scripts/                # SPT ai-guard 工具链
```

## 五、免费/付费状态机（差异化核心）

```
FREE（免费层）──全部免费不可用──→ PAID（付费链）
    ↑                              │
    └──免费恢复健康 ≥T 分钟（确定性回归）─┘
```

- **层级优先绝对**：免费/付费不混池评分
- 免费层内部：按健康评分选当下最优
- 付费链：显式顺序（如 DeepSeek → GLM），不评分
- **防抖优先**：回归阈值宁慢勿抖（沿用"3s 超时误伤正常响应"教训）

## 六、配置模型（T-001 落地，T-009 扩展密钥装载）

`config/config.json`：
```jsonc
{
  "listen": ":8462",
  "providers": [
    { "id": "agnes-anthropic", "base_url": "https://apihub.agnes-ai.com",
      "format": "anthropic", "tier": "free",
      "keys": ["vault:freellm/provider/agnes/agnes-aimly"],  // 多 key 轮换
      "models": [{"id": "agnes-2.5-flash", "name": "Agnes 2.5 Flash"}] },
    { "id": "agnes-paid-anthropic", "base_url": "https://apihub.agnes-ai.com",
      "format": "anthropic", "tier": "paid",
      "keys": ["vault:freellm/provider/agnes/agnes-paid"], ... }
  ]
}
```

- **key 写指针，不写明文**（配置文件已入库）：
  - `env:NAME` → 环境变量
  - `vault:PATH` → 密码墙条目（`vault get PATH`）。仓库规范 `ops/dev-fleet/STANDARD.md` §7 要求走这条
- **解析不在 `Load` 里做**：`Load` 保持"读文件 + 校验"的纯职责；`Config.ResolveKeys(ctx, resolver)`
  由 `main` 显式调用，从而能带 ctx 预算（`DefaultVaultTimeout` = 10s，与 key 数无关）与 logger。
  与 `internal/nats` 的"探测能力 / 暴露状态 / 执行"分层同构。
- **零依赖**：不引 `vaultcli` 库，`internal/config` 内裸 `exec` 调 `vault.exe`
  ——镜像 `internal/nats` 经 exec 调 `nats.cli`。解密能力只存在于 vault 二进制内。
- **失败非致命**：解析失败保留占位符（该 key 视为无凭据 → 池空 → 503），并把明细写进
  `KeyReport` 供启动日志与 `/health` 的 `unresolved` 字段。判据是"**trim 后是否非空**"，
  不是"有没有报错"——vault 成功返回空串同样算失败。
- tier = free|paid 是状态机分层的依据；format 决定走哪条协议路径（严格隔离，各有独立 free→paid 链）
- 每个 model 可声明 `kinds`（功能类型：`general`/`reasoning`/`coding`/`fast`），供虚拟模型路由挑选

## 六-b、虚拟模型层（T-010）

客户端不点名具体模型，只声明"要什么类型"；网关解析成具体模型并改写请求体。

```
客户端: {"model":"auto:reasoning"}
   ↓  ParseVirtual → (wantKind="reasoning", virtual=true)
候选过滤: candidates(format, "reasoning") —— 不持有该 kind 的 provider 直接跳过
   ↓
逐候选解析: ResolveModel(op.pv, "reasoning") —— **每个候选各挑各的**
   ↓                                （不同 provider 持有的模型不同）
SetModel(body, "agnes-2.5-flash") —— 只对虚拟模型改写
   ↓
上游收到 {"model":"agnes-2.5-flash", ...}
```

**设计要点**

| 决策 | 理由 |
|------|------|
| 只对虚拟模型改写 body | 字面模型请求零行为变化——新逻辑的风险面压到最小，对既有客户端完全兼容 |
| 逐候选解析而非全局解析一次 | provider A 与 B 持有的模型可能不同；全局解析会选到某个候选没有的模型 |
| 无人提供该类型 → 明确报错 | "要推理型"静默变成别的，比失败更难查 |
| 候选过滤（而非事后纠正） | 选到不持有该类型的 provider 后无模型可改写，只能退而用不相干的模型 |

**为什么切换对客户端无感**（两个已实测的前提）

1. **凭据不限制模型**：付费 key 要 `agnes-2.5-flash` → 200 且 `model` 回显不变；反向亦然。
   所以"切 provider"是**换账号**，不是换模型。
2. **无模型专属状态**：agnes 不返回 `thinking` 块（带 `thinking:{type:enabled}` 请求仍只回
   text 块）。Anthropic 的 `thinking` 块带模型专属签名、跨模型回传会被拒——这是跨模型续话的
   主要障碍，agnes 不存在它。对话状态只有 `text`/`tool_use`，跨模型可携。

**会话延续**：由客户端重发完整历史保证。网关**完全无状态**——不存会话、不做上下文交接
（智能体侧的三要素接续协议与网关职责正交）。网关只负责"按类型给一个能用的模型"，
并把实际用到的模型写进 `/decisions`（`want`/`model` 两个字段），让切换**可见**。

**请求体上限**：32 MiB（`proxy.MaxBodyBytes`），超限 413。旧实现 `io.LimitReader(1<<20)`
静默截断——约 22 万 token 撞墙，且错误由上游以 `unexpected end of JSON input` 的形式返回，
矛头指向调用方。

## 七、演进路线

| 阶段 | 内容 | 状态 |
|---|---|---|
| T-001 | config 模型 + provider 抽象 | 🔄（config 完成） |
| T-002 | 多 key 路由 + 健康检查 + 冷却 | ⬜ |
| T-003 | 协议端点 + NATS 状态发布 | ⬜ |
| T-004 | 免费/付费确定性状态机 | ⬜ |
| T-005 | WorkBoard 接入 | ⬜ |

## 八、变更记录

| 日期 | 版本 | 变更 |
|------|------|------|
| 2026-07-25 | v3.0 | 旧架构（OAuth 免费聚合，废弃） |
| 2026-08-23 | v4.0 | 会话保持型路由网关（OmniRoute 参考，归档 15d8f68） |
| 2026-08-28 | v5.0 | **FreeLLMAPI 蓝本重构**：多 key/健康检查/精选池/状态机 + NATS+WorkBoard |
