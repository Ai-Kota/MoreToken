# 功能测试矩阵（完整性定义）

> 权威清单：所有功能必须可测到。图例：✅ 已覆盖 · ⬜ 未覆盖（阻塞）· — 不适用
> **时序列是本矩阵的主战场**——成本确定性不变量只有把时间作为自变量
> （免费失效 N 分钟 / 恢复 M 分钟）才能被证伪。矩阵 = 验收标准，有未覆盖格 = 阻塞。

## 0. 被测对象与注入前提

- **被测对象**：`state` 包（免费/付费状态机）+ `router`（选择）+ `health`（探测），
  以接口注入 fake provider（纯标准库 `net/http/httptest`），**不依赖真实上游**。
- **注入手段**：
  - **健康位直拨**：测试直接调 `health.MarkUnhealthy(provider)` / `MarkHealthy(provider)`
    —— 精确控制"免费失效第 N 分钟"，不必真等 T 分钟；墙钟用 fake clock 注入。
  - **httptest 上游**：每个 provider 一个 `httptest.Server`，测试随时改它的响应
    （200 / 429 / 500 / 流中断 / hang）模拟上游故障。
  - **路由决策日志**：被测代码必须把每次 (tier, provider, key, reason) 写入
    可查询的决策日志（也是 T-003 切换留痕的落地），**断言全部基于日志，不基于内部状态**。
- **被测不变量（I1-I8，详见 §1）**：断言写死在测试名里，测试失败即不变量被打破。

## 1. 不变量定义（先写断言，再写测试）

| # | 不变量 | 一句话 | 对应 ADR/GOALS |
|---|--------|--------|---------------|
| I1 | 免费优先 | 任一免费 key 健康 → 路由决策必为 free tier；**付费请求数 = 0** | 免费优先（确定性） |
| I2 | 付费兜底 | 免费层全部不可用 → 路由必走显式付费链（按配置顺序） | 付费兜底（确定性） |
| I3 | 不混池 | 付费 key 健康分数再高，也**永远不**与免费层同池比较/抢占 | 层级优先绝对 |
| I4 | 每请求兜底（**2026-09-20 改写**） | 兜底候选集 **⊇** 主力候选集（付费档绝不删除免费候选）。免费层不可用 → **本次**请求落显式付费链；免费层恢复（池退避到期 / 探活解禁）→ **下一条请求即回落 free**，不得有档位粘性或最短占用期 | T-015：兜底是应急不是模式 |
| I5 | 防抖只作用于上报（**2026-09-20 改写**） | 抖动（<T 分钟再挂）**不得让上报档位来回摆**；但**不得**以此拦下任何一个请求——防抖没有路由否决权 | T-015：把 I5 从路由搬到告警降噪 |
| I6 | 会话不掉 | 付费/免费切换期间，客户端全程恒定端点；上游切换不返回 429（除非全池耗尽且已走完重试边界） | 会话不掉 |
| I7 | 重试安全边界 | 仅在响应字节发出前可重试；流已开始 → 不透明重放（tool-call 不重复执行） | 重试安全边界 |
| I8 | 多 key 吸收 | 单 key 429 → 同 provider 其他健康 key 接管；**该 key 冷却期内不再被选** | FreeLLMAPI 蓝本：多 key 轮换 |
| I9 | 凭据隔离 | 单 key 401/403 → 该 key 被隔离（长禁），请求换池内下一把；**401 绝不透传给客户端** | T-009 修复：多 key 承诺的前提 |
| I10 | 虚拟模型落点确定性 | `auto:<kind>` 必落到**持有该 kind** 的模型；无人持有 → 明确失败，**不得静默换成不相干模型**；字面模型请求体不被改写 | T-010：类型路由的正确性底线 |

## 2. 功能矩阵

### F1 免费层选优（正常路径）

| 正常 | 空 | 错误 | 边界 | 时序 |
|------|----|------|------|------|
| 2 个免费 provider 各 1 key 健康 → 选评分最高者，付费请求数=0（I1） | 免费层 0 个 provider → 启动即付费兜底，决策日志记 reason=tier-exhausted（I2） | 免费 key 全 401 → 视作不可用，转付费链 | 免费 1 key + 付费 1 key，付费 key 更"健康" → 免费永远优先，付费健康度不影响免费优先（I3） | 免费健康窗口内 100 连发 → 每决策 tier=free，付费请求数恒 0（I1） |

### F2 免费全挂 → 付费兜底（**每请求**，2026-09-20 改写）

| 正常 | 空 | 错误 | 边界 | 时序 |
|------|----|------|------|------|
| 免费全 unhealthy → **本次**请求按付费链配置顺序选第 1 个健康付费 key（I2） | 付费链 0 个 provider → 返回 503 + 诊断（哪个 tier 空、为何），**不得静默** | 付费 key 也 401/429 → 走付费链下一个；全耗尽 → 503 | **兜底不得低于主力**：付费档缺某 kind 时，该 kind 的免费候选**必须仍在链上**——旧缺陷（T-015）把候选删成空集 → 全线 503 | 免费挂第 N 分钟期间 50 连发 → 每一次都**先试免费**（退避中的跳过，成本 0）、失败才落付费 |

### F3 兜底不粘（免费恢复立即回落，2026-09-20 改写）

| 正常 | 空 | 错误 | 边界 | 时序 |
|------|----|------|------|------|
| 免费恢复 → 下一条请求即回落 free（I4） | — | 探活失败 → 不解禁，继续走付费 | 免费池仍在退避窗口内 → 本次仍走付费，到期后回落 | **核心序列**：t0 免费全挂 → 本次落付费；t0+60s（池退避到期）或探活成功（≤探测周期 30s）→ 下一条请求**必 free**。断言：**不存在**任何 ≥T 的强制付费窗口 |

### F4 防抖只降噪上报（不拦请求，2026-09-20 改写）

| 正常 | 空 | 错误 | 边界 | 时序 |
|------|----|------|------|------|
| — | — | — | — | 免费恢复 (T-ε) 即再挂 → **上报档位**不得来回摆（I5）；但期间每一条请求照常先试免费 —— 防抖没有路由否决权 |

### F8 降级信号的证据口径（T-015 新增）

| 正常 | 空 | 错误 | 边界 | 时序 |
|------|----|------|------|------|
| 免费候选**真的试过**且全挂 → sink 触发 1 次 | 免费候选全在退避窗口内（一个请求都没发）→ sink **不得**触发 | 只试探过付费候选就报"免费耗尽" → **不得**触发 | 免费候选试过失败 + 付费候选也失败 → 触发（免费层确被证伪） | — |

### F5 会话不掉 + 恒定端点

| 正常 | 空 | 错误 | 边界 | 时序 |
|------|----|------|------|------|
| 客户端恒定 :8462，底层 free→paid→free 三次切换，每次请求成功 | 全池（free+paid）耗尽 → 503 明确告知（不是 200 空响应） | 付费 key 超时 hang → 走重试边界（I7）后切下一 key，客户端单次调用内不感知 | 同一会话 100 轮 tool-call，期间 tier 切换 2 次 → 响应全部完整、tool-call 不重放（I7） | 切换瞬间并发 10 请求 → 无 429（除非全池真耗尽）；切换留痕时间戳单调（I6） |

### F6 多 key 轮换池（FreeLLMAPI 蓝本核心）

| 正常 | 空 | 错误 | 边界 | 时序 |
|------|----|------|------|------|
| 3 key 轮询接管，单 key 429 → 下一请求落到冷却外的 key（I8） | 1 个 key（退化情形）→ 冷却即不可用，按 I2 语义处理 | key 解密/加载失败 → 该 key 标记 error 并隔离，其余 key 继续（对齐 FreeLLMAPI `decrypt-error` 桶） | 3 key 中 2 个 429 → 第 3 个独占；第 3 个也 429 → 该 provider 视为不可用 | 429 风暴 100 连发 → 决策日志中每个 key 的命中次数近似均衡（±20%），冷却 key 在冷却期内命中数=0（I8） |

### F7 重试安全边界（对齐 FreeLLMAPI 已踩坑）

| 正常 | 空 | 错误 | 边界 | 时序 |
|------|----|------|------|------|
| 连接错误 / 429 / 5xx / 超时（响应字节前）→ 重放到链上下一个同格式 key | — | 流式已发首字节后上游 500 → **不重放**，按策略显式处理（tool-call 不重复）（I7） | max_retries 耗尽 → 返回最后一次错误 + 诊断 | 11 次慢失败（每次 4s）→ 总耗时被时间预算封顶（对齐 FreeLLMAPI 38.8s 血泪：预算在"开始下一次前"检查，首两次必执行） |

## 3. 契约 fixtures（真实捕获）

> 真实上游样本固化，测试从契约生成，不从写的代码推断。

| # | Artifact | 固化路径 | 验证内容 |
|---|----------|---------|---------|
| 1 | 免费 provider 429 响应体（含 Retry-After） | `tests/fixtures/upstream/free_429.sse` | 冷却时长解析、Retry-After 透传语义 |
| 2 | 流式 tool-call 事件序（anthropic 格式） | `tests/fixtures/upstream/tool_stream.sse` | I7：流中断后不重放 tool_use |
| 3 | 付费 key 401 响应体 | `tests/fixtures/upstream/paid_401.json` | I8：key 级隔离，不影响同 provider 其他 key |

## 4. 一键回归

```bash
# test-all.sh：单测 + 矩阵故障注入（F1-F7 时序格全部跑 fake clock，无真实等待）+ lint
bash test-all.sh
```

## 5. 覆盖登记（随 T-002/T-004 实现逐步打勾）

> 判据：**每格要么落到具体测试函数，要么显式标"延后 + 原因"**。不留 ⬜——
> `CLAUDE.md` §2.4 规则 1 写的是"有未覆盖格 = 阻塞"，一个不说明理由的 ⬜ 会永久卡着验收。
>
> **2026-09-19（T-009）重核**：本表此前只登记 6 格、其余留 ⬜，但仓里当时已有 **36 个测试函数**——
> **是登记陈旧，不是真的没测**。重核后逐格补登；同期新增 16 个（密钥装载 / 死 key 隔离 / health 如实上报），
> 当时补到 **52 个**；T-010 又加 14 个（虚拟模型层 + 请求体上限），**现共 66 个**。
> 图例：✅ 已覆盖 · 🔻 延后（附原因）

### F1 免费层选优

| 格 | 测试函数 | 状态 |
|----|---------|:----:|
| 正常（I1） | `TestRouter_FreeFirst_PaidZero` | ✅ router |
| 时序（I1 100 连发） | `TestRouter_FreeFirst_PaidZero`（同一测试内 100 连发，付费命中恒 0） | ✅ router |
| 边界（I3 不混池） | `TestRouter_FormatIsolation` · `TestRouter_TierPreference` | ✅ router |
| 错误（免费 key 全被拒） | `TestRouter_AllKeysDead_Exhausts` · `TestRouter_DeadKey_IsolatesAndRotates` | ✅ router |
| 空（免费层 0 个 provider） | — | 🔻 延后：`config.validate()` 已保证 provider 数 ≥1 且必属 free/paid；真实退化路径由 F2-空覆盖 |

### F2 免费全挂 → 付费兜底

| 格 | 测试函数 | 状态 |
|----|---------|:----:|
| 正常（I2） | `TestRouter_AllFreeUnavailable_PaidChain` | ✅ router |
| 边界（付费链 A→B 不跨级） | `TestRouter_FallbackNextWhenFreeDown` | ✅ router |
| 错误（免费故障时转付费） | `TestRoute_FallbackIsPerRequest_NotSticky`（T-015 重写） | ✅ router |
| 边界（**兜底不得低于主力**） | `TestRoute_ReasoningServedWhenPaidLacksKind`（T-015 新增） | ✅ router |
| 时序（故障期间连发：每次先试免费、失败才落付费） | `TestRoute_PaidNeverPreemptsHealthyFree`（T-015 重写） | ✅ router |
| 空（无凭据 → 503 + 诊断） | `TestEndpoint_NonStreaming_RoundTrip` | ✅ proxy |
| 错误（付费 key 全耗尽 → Retry-After ETA） | — | 🔻 延后：ETA 需 state 暴露冷却到期时刻，属体验优化非正确性；当前退化为 503 + 诊断体 |

### F3 兜底不粘（免费恢复立即回落，T-015 改写）

| 格 | 测试函数 | 状态 |
|----|---------|:----:|
| 时序（**核心**：池退避到期 → 下一条请求必 free） | `TestRoute_FallbackIsPerRequest_NotSticky` | ✅ router |
| 时序（观测档位仍按 ≥T 平滑；**不再拦请求**） | `TestState_FaultInjection_FreeRecovery` | ✅ state |
| 错误（回归探测失败 → 不计数不误判） | `TestState_ProbeFailureResetsWindow` | ✅ state |
| 正常（免费恢复 → 下一条即回落 free） | `TestRoute_FallbackIsPerRequest_NotSticky` | ✅ router |
| 边界（窗口内再次全挂 → 计时清零） | `TestState_FaultInjection_FreeRecovery`（窗口内断言） | ✅ state |
| 空 | — | ✅ 不适用（回归纯时序行为，"空"格无对应状态） |

### F4 防抖只降噪上报（**不拦请求**，T-015 改写）

| 格 | 测试函数 | 状态 |
|----|---------|:----:|
| 时序（I5 抖动 <T → 上报档位不来回摆） | `TestState_FaultInjection_JitterBelowThreshold` | ✅ state |
| 边界（**抖动期间请求照常先试免费**） | `TestRoute_FallbackIsPerRequest_NotSticky` | ✅ router |
| 正常 / 空 / 错误 | — | ✅ 不适用（F4 只由时序列定义，其余列无独立语义） |

### F5 会话不掉 + 恒定端点

| 格 | 测试函数 | 状态 |
|----|---------|:----:|
| 正常（恒定端点下请求成功） | `TestEndpoint_NonStreaming_RoundTrip` · `TestEndpoint_Streaming_SSETransparent` | ✅ proxy |
| 空（全池耗尽 → 明确 503 而非 200 空响应） | `TestEndpoint_NonStreaming_RoundTrip` | ✅ proxy |
| 错误（密钥装载失败 → 503 不是 401） | `TestResolveKeys_PathMissing` · `TestResolveKeys_EmptyValueIsUnresolved` · `TestRouter_VaultPlaceholderNeverSentUpstream` | ✅ config/router |
| 边界（切换留痕时间戳单调） | `TestDecisionLog_RingBuffer` | ✅ proxy |
| 时序（多轮 tool-call 期间切换且不重放） | `TestRetry_StreamStarted_NoReplay`（单轮不重放已证） | 🔻 部分：跨切换的长会话需集成夹具，T-008 前端联调时一并验 |

### F6 多 key 轮换池（FreeLLMAPI 蓝本核心）

| 格 | 测试函数 | 状态 |
|----|---------|:----:|
| 正常（轮询接管、单 key 429 → 换 key） | `TestKeyPool_429Storm_RoundRobin` | ✅ pool |
| 空（1 个 key 的退化情形） | `TestKeyPool_EmptyPool` · `TestKeyPool_AllCooled` | ✅ pool |
| 错误（key 被拒 → 隔离，其余继续） | `TestRouter_DeadKey_IsolatesAndRotates` · `TestRouter_AllKeysDead_Exhausts` | ✅ router |
| 边界（429 延长不缩短已有冷却） | `TestKeyPool_ExtendsOnNewRateLimit` · `TestKeyPool_CooldownExpiry` | ✅ pool |
| 时序（429 风暴下各 key 命中均衡 ±20%） | — | 🔻 延后：需 fake clock 长序列 + 分布断言；现测试只证"不撞冷却中的 key"，未证分布均匀度 |

### F7 重试安全边界

| 格 | 测试函数 | 状态 |
|----|---------|:----:|
| 正常（连接错误 / 429 / 5xx → 重放下一把） | `TestInvoke_429_Classified` · `TestInvoke_5xx_Classified` · `TestInvoke_TransportError` | ✅ provider |
| 错误（失败分类正确） | 同上三例（各自断言 FailKind） | ✅ provider |
| 边界（流已发字节 → 不重放，I7） | `TestRetry_StreamStarted_NoReplay` | ✅ provider |
| 时序（11 次慢失败 → 总耗时被预算封顶） | — | 🔻 延后：**能力未实现**（`provider.Invoke` 无请求级时间预算），非未测 |

### 密钥装载与状态如实上报（T-009 新增·同属验收面）

| 格 | 测试函数 | 状态 |
|----|---------|:----:|
| vault 指针命中 | `TestResolveKeys_VaultHit` · `TestResolveKeys_MixedPrefixes` | ✅ config |
| vault 取不到 / 二进制缺失 | `TestResolveKeys_PathMissing` · `TestDefaultVaultGet_BinMissing` · `TestResolveKeys_VaultUnavailableDegrades` | ✅ config |
| 成功但值为空（≠ 成功） | `TestResolveKeys_EmptyValueIsUnresolved` | ✅ config |
| 同 path 去重 + 负缓存 | `TestResolveKeys_Dedup` · `TestResolveKeys_NegativeCacheNotRepeated` | ✅ config |
| 纯 env 配置零 exec（反向断言） | `TestResolveKeys_PureEnvDoesNotExecVault` | ✅ config |
| 占位符判定（含裸 `env:`/`vault:`） | `TestIsPlaceholder` | ✅ config |
| `/health` 报真实 key 数而非原始条数 | `TestHealth_KeysAreRealCountNotRawCount` | ✅ router |
| URL 拼装（两种协议各自后缀） | `TestBuildURL` · `TestInvoke_AnthropicHeaders` | ✅ provider |

### 虚拟模型层 + 请求体上限（T-010 新增）

| 格 | 测试函数 | 状态 |
|----|---------|:----:|
| 虚拟名解析（`auto` / `auto:<kind>` / 大小写 / 空白 / 非虚拟） | `TestParseVirtual` | ✅ router |
| model 字段改写（保留其余字段） | `TestSetModel` | ✅ router |
| 非 JSON 对象 → 报错（不发虚拟名上游） | `TestSetModel_RejectsNonObject` | ✅ router |
| kind 查询与解析（含空 kind = 任取） | `TestHasKindAndResolveModel` | ✅ router |
| 虚拟模型落成具体模型 | `TestRoute_VirtualModel_RewritesToConcrete` | ✅ router |
| **I10** 无人提供该类型 → 明确报错 | `TestRoute_VirtualModel_NoProviderOfKind` | ✅ router |
| **I10** 候选按 kind 过滤 | `TestRoute_VirtualModel_KindFilteredCandidates` | ✅ router |
| **I10** 字面模型 body 零改写（向后兼容） | `TestRoute_LiteralModel_BodyUntouched` | ✅ router |
| `auto` 无类型 = 任意（仍改写） | `TestRoute_VirtualModel_PlainAutoAnyKind` | ✅ router |
| 虚拟模型仍受格式隔离 | `TestRoute_VirtualModel_FormatIsolation` | ✅ router |
| 超限 → 413 且**不转发上游** | `TestBodyLimit_OverLimit_Returns413` | ✅ proxy |
| 刚好到限 → 放行（多读的 1 字节不能误伤） | `TestBodyLimit_ExactlyAtLimit_Passes` | ✅ proxy |
| 超限一字节 → 拒绝（边界不差一） | `TestBodyLimit_OverLimitByOneByte` | ✅ proxy |
| 默认上限显著高于旧 1 MiB | `TestBodyLimit_DefaultIsGenerous` | ✅ proxy |

### 健康探测取 key（T-011 新增）

| 格 | 测试函数 | 状态 |
|----|---------|:----:|
| **第一把失效但其余健康 → 仍判健康**（本包存在的理由） | `TestProvider_FirstKeyDeadButOthersHealthy` | ✅ probe |
| 全挂 → 不健康，且调用次数被 attempts 封顶 | `TestProvider_AllKeysDead` | ✅ probe |
| 起点随 round 轮转（每把 key 都有机会被采到） | `TestProvider_RoundRotatesStart` | ✅ probe |
| 无真实凭据 → 不健康且不发请求 | `TestProvider_NoRealKeys` | ✅ probe |
| attempts 超过池大小时不越界 | `TestProvider_AttemptsCappedByPoolSize` | ✅ probe |
| 占位符被 `Keys` 跳过 | `TestKeys_SkipsPlaceholders` | ✅ probe |

> 这些测试提成独立包（`internal/probe`）而非留在 `main`，是因为 `test-all.sh` 只跑
> `./internal/...`——留在 main 里等于**没有回归覆盖**。

### 挂死防护（T-012 新增，I6 会话不掉的直接前提）

| 格 | 测试函数 | 状态 |
|----|---------|:----:|
| 上游不回响应头 → 有限时间内失败 | `TestResponseHeaderTimeout_HungUpstreamFails` | ✅ provider |
| 流式已开始后上游静默 → 主动中止 | `TestIdleBody_StalledStreamAborts` | ✅ provider |
| 有持续数据流入 → 不受影响 | `TestIdleBody_ActiveStreamPasses` | ✅ provider |
| idle<=0 落到默认值（不变成立即超时） | `TestIdleBody_ZeroUsesDefault` | ✅ provider |
| **没有 Total Timeout**（长生成不能被切断） | `TestUpstreamClient_NoTotalTimeout` | ✅ provider |

> 最后一条防的是"以后有人顺手把总超时加回来"——那会静默切断长生成，
> 而症状（响应被截断）离病因（client.Timeout）很远。

### NATS 发布链路（T-014 新增——监控前端的数据源）

| 格 | 测试函数 | 状态 |
|----|---------|:----:|
| 约定→CLI flag 翻译（名字全不匹配的那个坑） | `TestArgs_TranslatesConventionToCLIFlags` | ✅ nats |
| `CA_FILE=system` 不翻译成 `--tlsca` | 同上（内含断言） | ✅ nats |
| 真实 CA 文件路径才传 `--tlsca` | `TestArgs_RealCAFileBecomesTlsca` | ✅ nats |
| 环境变量为空时不传空 flag | `TestArgs_EmptyEnvOmitsFlags` | ✅ nats |
| `nats` 不在 PATH → 回落绝对默认路径 | `TestNewPublisher_ResolvesDefaultPath` | ✅ nats |
| `NATS_BIN` 可覆盖 | `TestNewPublisher_EnvOverride` | ✅ nats |
| 降级态 Publish 恒 nil（不阻塞主链路） | `TestPublish_DisabledIsNilSafe` | ✅ nats |
| 失败按原因去重记录 | `TestNoteFailure_DedupesByReason` | ✅ nats |

> 测试里刻意用"存在盘上的不存在目录"而非 `Z:`——Windows 解析不存在的**盘符**会走超时重试，
> 实测把这条测试从 0s 拖到 30s+。

### 兜底重回每请求（T-015 新增——治 auto:reasoning 全线 503）

| 格 | 测试函数 | 状态 |
|----|---------|:----:|
| F2-边界 **兜底不得低于主力**（事故逐字复现：付费缺 reasoning + 免费有） | `TestRoute_ReasoningServedWhenPaidLacksKind` | ✅ router |
| F2-时序 兜底不得被当主力用（免费健康 → 付费命中数恒 0） | `TestRoute_PaidNeverPreemptsHealthyFree` | ✅ router |
| F3-时序 应急只作用于那一次请求（池退避到期即回落 free） | `TestRoute_FallbackIsPerRequest_NotSticky` | ✅ router |
| F8-边界 sink 需"免费层真的被试过" | `TestRoute_SinkRequiresFreeAttempt` | ✅ router |
| F8-正常 免费试过且全挂仍照常发信号（防修过头） | `TestRouter_AllTriedAndFailed_StillDemotes` | ✅ router |
| F8-空 一个候选都没试 ≠ 耗尽（`555e60c` 的既有契约） | `TestRouter_NothingAttempted_IsNotExhaustion` | ✅ router |
| 探测时机：无隔离池 → 一个探测都不发 | `TestState_TickOnce_NoProbeWhenNothingSidelined` | ✅ state |
| 探测时机：有隔离池就探（**档位仍为 FREE 时也要探**） | `TestState_TickOnce_ProbesWhenSidelined` | ✅ state |
| 隔离池筛选：退避/冷却各算隔离，健康的除外 | `TestSelectSidelined_PicksBackedOffAndCooling` | ✅ router |
| 隔离池筛选：无隔离 → nil | `TestSelectSidelined_NoneSidelinedReturnsNil` | ✅ router |
| 隔离池筛选：只隔离付费池 → nil（免费侧无事可做） | `TestSelectSidelined_PaidNeverProbed` | ✅ router |
| 隔离池筛选：返回项**必须带 key**（防"从 Router 内部取→凭据被置空"） | `TestSelectSidelined_CarriesKeys` | ✅ router |
| 观测档位仍按 ≥T 平滑（防抖只降噪，不再拦请求） | `TestState_*`（T-004 既有 6 条，语义改为"上报"） | ✅ state |

> **变异检验（必须做，否则这条回归测试等于没写）**：把 `paidOnly` 以变异体形式贴回 `Route`
> （对候选做一次 paid-only 过滤），上述前三条必须红，且
> `TestRoute_ReasoningServedWhenPaidLacksKind` 报出的必须是**与事故逐字一致**的
> `router: all anthropic candidates cooling (tried 0, limit 4)`。
> 实测通过，实录见 `docs/tasks/T-015.md`。

### 延后项汇总

| 项 | 原因 | 何时补 |
|----|------|--------|
| F7-时序 时间预算封顶 | **能力未实现**（无请求级预算），非未测 | 真实出现慢上游时 |
| F6-时序 均衡性 ±20% | 需 fake clock 长序列 + 分布断言 | T-008 联调后按真实流量校准 |
| F5-时序 跨切换长会话 | 需长跑集成夹具 | T-008 前端联调时一并验 |
| F2-错误 Retry-After ETA | 需 state 暴露冷却到期时刻 | 体验优化，非正确性 |

## 原则

1. 契约优先：真实 artifact 固化为 fixture，测试从契约而非代码推断。
2. **新增功能 → 先加矩阵行 + 写测试，再合并**（§5 登记列先行）。
3. 覆盖率是下限不是目标（覆盖行 ≠ 值正确）——本矩阵的时序格是行为正确性的保证。
