# MoreToken — 免费优先的会话保持型路由网关

一个端点，底下自动切模型：**默认走免费省钱；免费挂了无缝切付费兜底、会话不掉；免费恢复自动切回。**

定位：降低开发成本 + 会话永不掉线。详细设计见 [ADR-003](docs/decisions/ADR-003-positioning.md) 与 [系统架构](docs/architecture/ARCHITECTURE.md)。

> **当前状态：T-000 ~ T-009 已交付并端到端实跑验证**（截至 2026-09-19）。
> 两条协议路径均实测 200、tool_use 与流式均通、密钥经 `vault:` 指针装载（79 条全部解析）。
> 唯一未完成项是 **T-008 前端订阅**——归属外部仓 `E:/repos/aimly-console`，本仓已交付契约与后端发布侧。

## 功能

- **恒定端点**：Claude Code / OpenAI 客户端只配一次 `http://localhost:8462`，provider 故障不影响会话
- **虚拟模型**：客户端只说"要什么类型"（`auto:reasoning`），具体模型由网关挑——模型换代对客户端透明（见下）
- **双协议**：`/v1/messages`（Anthropic，Claude Code）+ `/v1/chat/completions`（OpenAI）
- **格式隔离**：两条协议路径内部完全隔离，互不串选（每条路径各有独立的 free→paid 链）
- **免费优先 + 确定性回归**：任一免费 key 健康即走免费；全挂才进付费；免费恢复健康 ≥T 分钟自动切回，抖动 <T 不切（防抖）
- **多 key 轮换**：账号级限流由多 key 吸收（agnes 免费档 20 RPM/**账号**且账号隔离）；429 冷却 60s、401/403 长禁 1h
- **零外部依赖**：Go 1.25 纯标准库、单二进制；NATS 与 vault 都经 `exec` 调本机 CLI，不进 Go 依赖

## 构建与运行

```bash
go build -o bin/moretoken.exe .
bin/moretoken.exe -config config/config.json
```

常驻由 dev-fleet 托管（`ops/dev-fleet/services.yaml` 的 `moretoken` 条目）；手动起的进程活不过会话。

## 密钥配置（重要）

**密钥不写进 `config/config.json`**——该文件已入库，明文进去就是泄漏。key 一律写**指针**，进程启动时解析：

```jsonc
{
  "id": "agnes", "base_url": "https://apihub.agnes-ai.com/v1",
  "format": "openai", "tier": "free",
  "keys": ["vault:freellm/provider/agnes/agnes-aimly", "env:AGNES_KEY_2"]
}
```

| 前缀 | 含义 |
|------|------|
| `vault:PATH` | 密码墙条目（`vault get PATH`）。**推荐**——明文只在墙里 |
| `env:NAME`  | 环境变量 |

仓库规范 `ops/dev-fleet/STANDARD.md` §7 要求密钥从 vault 取、不进配置明文。

**解析失败的语义**：不 fatal，保留占位符 → 该 key 视为"无凭据"不进池 → 该池为空时返回 **503**（而不是把占位符当 key 发上游换回 401）。
失败明细写入启动日志（`WARN unresolved key: <provider>[N]: vault:<path> (<原因>)`），并如实反映在 `/health` 的 `keys` / `unresolved` 字段上。

## API 端点

| 端点 | 方法 | 说明 |
|------|------|------|
| `/v1/messages` | POST | Claude Code（Anthropic Messages 协议） |
| `/v1/chat/completions` | POST | OpenAI 兼容客户端 |
| `/v1/models` | GET | 模型列表 |
| `/health` | GET | 各池状态：`keys`（真实可用数）/ `unresolved` / `cooling` / `backed_off` |
| `/decisions` | GET | 最近路由决策留痕（`?n=` 控制条数；无 key 值、无请求体；虚拟模型另带 `want`/`model`） |

## 监控界面

**WorkBoard**（外部仓 `E:/repos/aimly-console`）里的 MoreToken 模块：

```bash
cd E:/repos/aimly-console
npm run dev
```

渲染当前层（free/paid）、各池状态表（keys / unresolved / cooling / backed_off）、
切换事件时间线；**90s 收不到 status 会明示"总线离线"**，不静默。

**数据通路**：本网关 → NATS `aimly.system.freetier.status`（30s）/ `.event`（即时）→ 前端订阅。

> ⚠️ 这条通路曾长期是断的（T-014）：`nats.exe` 不在服务 PATH，且网关继承的
> `NATS_SERVERS`/`NATS_USER_CREDS`/`NATS_CA_FILE` **stock nats CLI 一个都不认**
> （它认 `NATS_URL`/`NATS_CREDS`/`NATS_CA`），CLI 于是回落到自己的默认 context
> （指向本机 `127.0.0.1:4222`）→ 发布全失败，而错误被忽略 → 一声不吭。
> 现在启动日志会明确打一行 `nats: 发布已启用（cli=… server=…）`，发布失败按原因去重记 WARN。

> **请求体上限**：默认 **32 MiB**，超限返回 **413** 并带上限值。
> 旧实现用 `io.LimitReader` 静默截断——超限不报错、只是少读，半个 JSON 被转发上游，
> 上游回 `unexpected end of JSON input` 把矛头指向调用方，而网关日志里一个字都没有。
> 换算下来约 22 万 token 就撞墙，长会话必然踩。现已改为显式拒绝。

## 虚拟模型（推荐用法）

客户端**不必点名具体模型**，只声明"要什么类型"，网关自己挑一个当下能用的：

```jsonc
{"model": "auto"}             // 任意类型
{"model": "auto:reasoning"}   // 要推理型
{"model": "auto:coding"}      // 要编码型
{"model": "auto:fast"}        // 要快
{"model": "auto:general"}     // 通用
```

`GET /v1/models` 会把这些虚拟名一并列出，供客户端发现。

**类型在哪声明**：`config/config.json` 里每个 model 条目的 `kinds` 字段。

> ⚠️ **哪些类型真的在区分**（2026-09-19 实测，证据见 [`docs/quality/MODEL-CAPABILITY.md`](docs/quality/MODEL-CAPABILITY.md)）：
>
> | 类型 | 是否区分 | 说明 |
> |------|:--------:|------|
> | `reasoning` | ✅ **区分** | 火车追及题 10 样本：`agnes-2.5-flash` 10/10、`minimax-m3` 6/10，其余 0/10 |
> | `fast` | ✅ 区分 | 中位延迟 0.9s–1.9s（仅 agnes 三模型有实测） |
> | `coding` | ❌ **不区分** | 全部模型 88–100%，`auto:coding` 实际等价于 `auto` |
> | `general` | ❌ **不区分** | 全部模型 100% |
>
> `coding`/`general` 保留为**普遍能力**声明（陈述属实），但**别指望它筛出更会写代码的模型**——
> 那会是虚假精度。要区分就得先有能区分它们的评测。

**语义保证**：

| 行为 | 说明 |
|------|------|
| 按类型过滤候选 | 不持有该类型的 provider 直接跳过；**无人提供该类型时明确报错**，不退而用不相干的模型 |
| 每个候选各自解析 | provider A 和 B 持有的模型可能不同，各挑各的 |
| model 字段改写 | 虚拟名不会发上游（否则换来一个指向错误方向的 "model not found"） |
| 字面模型不受影响 | 客户端给具体模型名时 **body 一个字节都不改**，行为与以前完全一致 |
| 切换可见 | `/decisions` 记录 `want`（如 `auto:reasoning`）与 `model`（实际落到的具体模型），便于发现上游换了 |

**为什么可以放心切**：agnes 的凭据不限制模型（同一个 key 能服务各模型），所以**切换是换账号、不是换模型**；且 agnes 不返回 `thinking` 块，对话历史里没有模型专属签名，跨模型重发天然可接受（均已实测）。

> 会话延续由**客户端重发完整历史**保证——网关完全无状态，不存会话、不做上下文交接。
> 智能体侧的会话接续（三要素协议）与网关职责正交。

## 接 Claude Code

**终端里直接敲 `cc-ft`**（本机第 14 条启动路，2026-09-19 起）：

```powershell
cc-ft          # 交互
cc-ft -p "..." # 非交互
```

它做的事：探活 `:8462`（不可达则明确报错并按命令，**不静默 hang**）→ 切 `CLAUDE_CONFIG_DIR`
到 `D:\ClaudeConfig\.claude\profiles\cc-ft` → 启动 Claude Code。

`ANTHROPIC_MODEL` 是**虚拟模型** `auto:reasoning`——所以上游换模型、换账号、切免费↔付费，
**这条启动路都不用改配置**。

手动接（任意客户端）：

```bash
set ANTHROPIC_BASE_URL=http://localhost:8462
set ANTHROPIC_MODEL=auto:reasoning
```

免费层有**两个厂商**（agnes 原生即 Anthropic 协议，xkiro 实测 `/v1/messages` 亦为 200）：
`agnes-anthropic` → `xkiro-anthropic`（MiniMax/Qwen，异厂商）；全挂时切 `agnes-paid-anthropic` 兜底。

> **厂商多样性**：agnes 与 xkiro 是不同平台，agnes 平台级故障时 xkiro 仍可服务。
> 但**付费兜底仍与免费主力同厂商**（agnes）——真正的付费级异厂商隔离需要一把别家付费 key 入库。

### 上游布局

| provider | 协议 | 层 | 账号数 | 厂商 / 模型 |
|----------|------|:--:|:------:|------------|
| `agnes` | openai | free | 33 | agnes（agnes-2.5/2.0-flash） |
| `xkiro` | openai | free | 11 | **xkiro**（MiniMax M3 / Qwen3 Coder / Qwen3.7 Max，仅 `:free` 可用） |
| `agnes-anthropic` | anthropic | free | 33 | agnes |
| `xkiro-anthropic` | anthropic | free | 11 | **xkiro**（异厂商兜底） |
| `agnes-paid-anthropic` | anthropic | paid | 1 | agnes Token Plan |
| `agnes-paid-openai` | openai | paid | 1 | agnes Token Plan |

## 项目结构

```
moretoken/
├── main.go                    # 入口：装配 config → router → proxy，启动状态机与 NATS 发布
├── config/config.json         # provider 声明（密钥写 vault:/env: 指针）
├── internal/
│   ├── config/                # 配置加载 + 密钥指针解析（env:/vault:）
│   ├── provider/              # 上游调用 + 失败分类（I7 重试安全边界）+ URL 拼装
│   ├── pool/                  # 多 key 池：RR + 429 冷却 + 401/403 长禁
│   ├── router/                # 格式隔离 + 免费优先 + 同格式 fallback（每请求兜底）
│   ├── state/                 # 免费层观测器：定向探活 + 平滑上报（**不参与路由**，2026-09-20 起）
│   ├── proxy/                 # 协议端点 + 决策日志
│   └── nats/                  # 状态/事件发布（exec nats.cli，缺失降级 no-op）
└── docs/
    ├── decisions/             # ADR-001(历史) ADR-002(技术栈) ADR-003(定位)
    ├── architecture/          # ARCHITECTURE.md
    ├── quality/               # TEST-MATRIX.md（不变量 I1-I9 + 覆盖登记）
    ├── tasks/                 # TASKS.md + 各任务定义
    └── workboard-contract.md  # 前端订阅契约（T-008 依据）
```

## 文档

- [定位决策 ADR-003](docs/decisions/ADR-003-positioning.md)
- [系统架构](docs/architecture/ARCHITECTURE.md)
- [测试矩阵（不变量 I1-I9）](docs/quality/TEST-MATRIX.md)
- [任务总表](docs/tasks/TASKS.md)
- [WorkBoard 契约](docs/workboard-contract.md)
