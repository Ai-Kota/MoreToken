# TASKS — MoreToken 重构

> 项目：`moretoken`（MoreToken，以 FreeLLMAPI 为蓝本重构）
> SPT 接入：2026-08-28 · 成熟度 Level 2（41/100）
> 管理：SPT v3.5（ai-guard 统一工具，任务 begin-task → verify-task）

## 任务总表

| ID | 任务 | 状态 | 说明 |
|----|------|:---:|------|
| T-000 | SPT 模板接入 | ✅ | init.sh + 成熟度 + settings + 文档骨架 |
| T-001 | Go 骨架：config 模型 + provider 抽象 | ✅ | config + provider（SSE/失败分类）+ pool（多 key RR + 429 冷却）全绿 |
| T-002 | 多 key 路由 + 健康检查 + 冷却 | ✅ | internal/router：格式隔离+tier 优先+同格式 fallback+池级退避+I7 重试（T-004 状态机另起） |
| T-003 | 协议端点 + 切换留痕日志 | ✅ | internal/proxy：/v1/messages + /v1/chat/completions + /health + /v1/models + /decisions；无凭据→503；NATS 发布裁剪至 T-005（零依赖约束） |
| T-004 | 免费/付费确定性状态机 | ✅ | internal/state：探测驱动 + 连续窗口≥T 回归(I4) + 防抖(I5)；接线 TierGate + FreeExhaustedSink 反馈闭环 + main 启动 st.Run；故障注入测试全绿 |
| T-005 | WorkBoard 接入 | ✅ | internal/nats（exec nats.cli，NATS 缺失降级 no-op）+ state 事件/池快照发布 + docs/workboard-contract.md 前端契约 + NEURAL_BACKBONE 主题登记 |
| T-006 | 成本确定性不变量 → 故障注入测试矩阵 | ✅ | 不变量 I1-I8 落成可证伪矩阵（docs/quality/TEST-MATRIX.md），先于 T-002/T-004 编码 |
| T-007 | 修 scripts/ ai-guard 存量测试（findSrcDir flake） | ✅ | findSrcDir 加 stopAt 边界，测试不再依赖机器 Temp 状态；test-all 整链绿 |
| T-008 | 前端 WorkBoard 订阅（跨仓待办） | 🟡 | 归属 `E:/repos/aimly-console`；本仓已交付契约（workboard-contract.md）+ 后端 NATS 发布（T-005）；前端订阅本体在外部仓执行 |
| T-009 | 收口：密钥装载 + Claude Code 免费层 + 矩阵对齐 + fleet 登记 | ✅ | vault: 指针装载（裸 exec，守零依赖，79 条全解析）；补 agnes-anthropic 免费层（实测 200 + tool_use + 流式）；付费层改 agnes-paid；**修 401 透传缺陷（I9 凭据隔离）**；`/health` keys 不再撒谎；TEST-MATRIX 逐格对齐（52 测试，不留 ⬜）；dev-fleet 登记 |
| T-010 | 虚拟模型层（按类型提供连接）+ 修 1 MiB 静默截断 | ✅ | 智能体只声明 `auto` / `auto:<kind>`，网关按 kinds 过滤候选并逐候选改写 model（I10）；字面模型零行为变化；**修 22 万 token 硬墙**——旧 `LimitReader(1<<20)` 静默截断成上游 400，现 32 MiB + 显式 413；实测两条路径 + 决策日志可见 want/model；66 个测试全绿 |
| T-011 | 清真言的绊脚石：配置缺陷 + 探测缺陷 + kinds 实测 + 实接验证 | ✅ | ①拆回 agnes/xkiro（T-009 把两个不同 base_url 的池合并，11/44 把 key 必然 401）；②探测改轮换取 key 并提成 `internal/probe`（原来永远取第一把，那把一失效就永久卡付费层）；③kinds 实测——**发现 coding/general 根本不区分、是虚假精度**，仅 reasoning 有区分力，证据见 `docs/quality/MODEL-CAPABILITY.md`；④Claude Code 经网关实跑含 tool_use 通过；⑤顺带加了 xkiro-anthropic 异厂商免费层 |
| T-012 | 接通「能用」：cc-ft 启动路 + 挂死防护 + 托管验证 | ✅ | 网关正确但**13 条启动路没一条走它**＝等于零。新增第 14 条 `cc-ft`（profile + PowerShell 函数 + 探活守卫，走 `auto:reasoning` 故上游换代不用改配置；实测 `cc-ft -p` → `CC-FT-WORKS`，网关停时 2.1s 明确报错）；补上游挂死防护（`Timeout:0` 下不回响应头会无限等——挂死对智能体比报错糟，报错能重试、挂死是会话冻住）；dev-fleet 托管实测通过（此前都是手起、活不过会话） |
| T-013 | 修 check-scope 意图门禁死锁（TASKS.md 不豁免） | ✅ | `isTaskDefinitionPath` 只认 `T-*.md`，而 `begin-task` 要求任务行已在 `TASKS.md` → 登记新任务需要已声明的任务，死循环。同源 5 份（SPT 模板 + GoStrix + model-evaluator + jiutian + 本仓）全部修好并重编；**决定性验证：清空任务后直接写本文件成功**（修前必被拦） |
| T-014 | 修 NATS 发布链路（监控前端数据源从未通过） | ✅ | 查"有没有监控界面"时发现：**前端有**（`aimly-console/src/modules/freetier/MoreTokenModule.jsx`，T-008 其实已实现，我此前说"未实现"是错的），**但数据源三层全断**——①`nats.exe` 不在服务 PATH；②网关继承 `NATS_SERVERS/USER_CREDS/CA_FILE` 而 stock CLI 认 `NATS_URL/CREDS/CA`，名字全不匹配；③CLI 回落到的默认 context 指向 `127.0.0.1:4222`（本机非云）。且 `Publish` 错误被忽略 → 三层全挂也一声不吭。**已修**：调用侧翻译成 CLI flag（不动部署侧约定）+ 绝对路径回落 + 失败按原因去重留痕；端到端实测订阅端真收到 status |
| T-015 | 兜底回到每请求 —— 删层门控（治 auto:reasoning 全线 503） | ✅ | 用户 15:31:49 报错。`/decisions` 判据：翻档前后免费池都在出 200 ⇒ 误判。机制：`paidOnly` 把候选删成空集；付费档只有 `agnes-3.0-flash`（kinds=`[coding,general]`）**接不了 auto:reasoning** → 报 `tried 0, limit 4` → 三分钟全线 503。定性（用户）：**免费是主力，兜底只是暂时应急，不能当主力用**。**已删门控**（`paidOnly`/`TierGate`/`WithTierGate`）——`candidates()` 本就返回 free→paid 有序链，兜底**从来不需要档位**；层门控的唯一净效果是把应急升格为主力。顺带修 sink 证据口径（免费一个请求都没发过也报"免费耗尽"）+ 探活改由池隔离状态驱动（旧口径下"整体在免费档、单池被长禁"没有解除途径）。**变异检验**：贴回门控，回归测试报出与事故**逐字一致**的 `tried 0, limit 4` |
| T-016 | 观测链补齐（503 留痕 + `staleness` 假绿） | ✅ | T-015 复盘发现这起事故**差点不可复原**——能定住全靠两条运气。第一性原理：网关的职责是**吸收失败**，而**吸收即销毁证据**，它越擅长本职就越不透明；解法只能是**带外记录**。①`proxy.go` 的 503 分支**在 Record 之前就 return**（注释还写着"已留痕"，本仓 §2 栽过同款）→ 那段窗口在 `/decisions` 里一行都没有，**是什么把免费池判死的至今【未核实】**；②`dev-fleet staleness` 只比"源码 vs bin"，**不看进程加载了哪个 bin** → 对着 14:54 起的旧进程报 `✓ 已生效`，我差点信了。**假绿比空白更贵**：空白让人去查，假绿让人收工。修法：503 补 Record（变异检验）；staleness 补第二判据（进程启动时刻 < bin mtime ⇒ 待生效，**只严不松**） |
| T-017 | 决策日志落盘 | 🟡 | **已登记，未开工**。`DecisionLog` 是内存环形缓冲，**重启即抹**——事故前 14:54 那次重启把上一段现场一起抹了。它与 T-016 是 P1 的同一件事的两半：T-016 补"记录完不完整"，T-017 补"记录存不存活"。设计问题（落点/写盘时机/轮转/回读/失败降级）已在任务档里列明，开工前先定 |
| T-018 | 免费池的新陈代谢（发现 → 实测 → 入库） | ✅ | 用户定性：**需要有机制保证源源不断的优质模型进入池中，新陈代谢才是良性循环**——并纠正我的漏判（我只算了发现时延，漏算恢复时间常数：补模型要"人去找→验证→改配置→部署"，**无界**）。前提先查实：`GET /v1/models` 两上游都通；**xkiro 目录 111 个模型、27 个 `:free`，我们只用 3 个**；列表≠可用（抽样 4 个未用 `:free`：2 个 200、1 个上游 500；agnes pro 系列对免费 key 全 403）。**前置阻断项**：先验"纳进来用不用得上"→ 模型维度是死的（`tryCandidate` 只解析一次模型、然后轮 key），加 24 个模型净收益 0 → 先修模型轮换（`68f9b06`）。机制：拉目录 → **真调实测** → 写 `models.auto.json` → `config.Load` 合并；四条硬规矩（列表≠可用必须真调 / kinds 只给 general 且写成结构约束 / 只追加不抢偏好位 / 清单累积不重建）。真跑暴露并修掉三个问题（Max 被 403 旗舰吃光、清单每轮重建导致**自我侵蚀**、403 文案撒谎）。**真身四轮**：xkiro 3 → **8 个模型** |
| T-019 | 发布失败指标（网关自述服务质量） | ✅ | 用户提议"网关把运行状态经 NATS 发布；出问题调用 agent"。**先查实：第一半已有**（T-014 修的发布链路，实测每 30s 一份池快照 + 档位事件都在流）——**缺口在内容**：发了池状态与档位翻转，**没发成功率/延迟/失败**，所以看板看得到"翻档了"、看不到"翻了 7 次、坏了 16 个请求"。第一性原理：**观察者只许消费事实、不许重新探测**（model-watchdog 就是反例：30–45s 探针 × 每模型 ≈ 2000+ req/天/模型，而 OpenRouter 免费档 50/天/模型——烧的正是它该保护的配额）。第二条：**"有失败"不是触发判据**——实测 9.5 小时降级 7 次、7 次全是自愈能搞定的，按"有失败就叫"会白叫 7 次；正确判据是**自愈承诺的超时**（key 60s / 池 60s / 免费层 180s / dev-fleet 300s）。实现：`internal/metrics` 按**时钟分钟**分桶（判据要与时间比，按请求数计的窗口会随流量漂）+ proxy 全出口观测（**不包装 ResponseWriter**——会破坏 Flusher 断言把流式打回缓冲）+ 并进既有 status（不新开主题：主题是注册制的）。真身验证：造流量 → 订阅 NATS，payload 里确实带出 metrics |
| T-020 | 失败路径细粒度留痕（是谁坏的、怎么坏的） | ✅ | 用户定性：**"打铁还需自身硬，agent 修复是最后的防线"** → 重新排优先级：在网关自己能回答"我为什么失败"之前，不该继续往上叠外部机制。缺口实测：503 留痕只有聚合 `tried 2, limit 4`，**回答不了谁失败了**；而 09-21 有 7 次突发降级、一次三个候选同时失败（指向本机而非上游）——**但那是【推断】**，因为两个字都没记。两层证据丢失：聚合粒度 + `/decisions` 重启即抹（**本次为了部署 /doctor 亲手抹了 08:21 的现场**）。改法：`router.Attempt`（provider/model/kind/status/reason）+ `skipped_backoff` **单独一类**（退避跳过 ≠ 试过失败）+ 失败路径也返回带 Attempts 的 Result。4 条测试含端到端。**真身未验证**（制造真实候选失败要扰动生产）——下次真实降级会自然填上 |
| T-021 | 90s 响应头超时导致 ~1.8% 请求失败 | 🟡 | **调查中**。现象：14/497 失败，全是 `net/http: timeout awaiting response headers`，均匀成滴（每 4–7 分钟一条孤立）。**三个假设两个被实测排除**：provider 慢（agnes 小请求 1.8–2.7s ✗）、大 body（60KB → 2.0s ✗）；第三个「调用方断开」是**另一个时间窗**的 `context canceled`，不是同一回事。**核心洞察（量化未观测）**：`ResponseHeaderTimeout=90s` 的注释理由是"远大于 6–7s 首字节"，但**那只对 streaming 成立**——非流式的"响应头"要等整段生成完。实测约 30ms/token ⇒ 90s ≈ 3000 输出 token ⇒ **非流式长生成会撞墙**（与已修过的"22 万 token 硬墙"同类）。本轮按用户选择**只取证不改策略**：`Attempt` 补 `stream` / `elapsed_ms` / `body_bytes`，等下次复发自证。**刻意不同时改两处** |
| T-023 | 声明上下文窗口（`/v1/models` 的 `max_input_tokens`） | ✅ | 起因：某会话堆到 **994166 token** 而模型上限 524288 ⇒ 卡死循环。用户定性：**无感 ≠ 不报错；无感 = 报得准**；更根本的是**根本不该产生这种请求**。判据来自 Claude Code 二进制里嵌的 Anthropic API 文档：字段名是 **`max_input_tokens`**（不是 `context_window`），而我们一个都没给。窗口取值全部有据：**agnes 三个模型实测 524288**（读它自己的超限报错）；**xkiro 由纳新从目录的 `context_length` 自动带回**。设计：虚拟模型的窗口取候选 **min**（客户端据此决定何时压缩，min 才是"我能保证的下限"）；**不知道就留空、绝不猜**。未核实：客户端是否真会据此调整压缩阈值 |

## 存量登记说明

- v4.0"会话保持型路由网关"（Phase 1 完成）已归档提交 `15d8f68`，作为设计历史
- 重构蓝本 = FreeLLMAPI 成功机制：多 key 轮换 / 精选免费池 / 健康检查+冷却 / 协议适配 / 失败回退链
- 差异化 = 免费/付费确定性状态机（相对 FreeLLMAPI 评分池）
- 已完成的存量代码（config 模型，SPT 引入前编写）纳入 T-001 持续规范

## 规则

- 每个任务：`begin-task <id>` → 实现（WU 粒度 ≤5 文件）→ `verify-task <id>`
- 测试不通过 = 任务不完成（test-all.sh 全量回归）
- 进度：`checkpoint <id> --summary "..."` 写入 SESSION-STATE.md
