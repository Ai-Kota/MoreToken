# WorkBoard 接入契约（T-005）

> 前端 WorkBoard（外部仓 `E:/repos/aimly-console`）经 NATS 订阅 MoreToken 网关的状态与事件。
> 纯后端、不内置 dashboard（GOALS 边界）；本文件是**前端订阅侧的权威契约**。
> 主题命名遵循 `deploy/nats/NEURAL_BACKBONE.md` §四。

## 一、数据流向

```
MoreToken 后端（纯逻辑，:8462）
  ├─ 路由/决策 ──决策日志──► /decisions（HTTP，本地）
  ├─ state 事件 ──NATS event──► aimly.system.freetier.event   （即时广播）
  └─ 池状态快照 ──NATS status──► aimly.system.freetier.status （30s 定期）
                                          │
                                          ▼
                              WorkBoard（aimly-console，NATS 订阅）
```

**降级语义**：NATS 不可用 / nats.cli 缺失时，后端发布静默 no-op，**不影响路由主链路**
（纯后端可独立运行，NATS 是增强非依赖）。WorkBoard 此时收不到消息，需自行显示"总线离线"。

## 二、主题注册表（本任务新增 2 条，须同步登记 NEURAL_BACKBONE §4.5）

| 主题 | 类型 | 发布者 | 订阅者 | 节奏 |
|------|------|------|------|------|
| `aimly.system.freetier.status` | status | moretoken | WorkBoard | 30s 定期 |
| `aimly.system.freetier.event` | event | moretoken | WorkBoard | 即时（切换/回归/抖动） |

> 归属域 = `system`（系统状态/健康类，与 `aimly.system.heartbeat.status` 同域）。

## 三、Payload Schema

### 3.1 status（`aimly.system.freetier.status`）

```jsonc
{
  "at": "2026-09-13T09:00:00+08:00",   // 快照时刻
  "tier": "paid",                        // 当前层："free" | "paid"
  "pools": [
    {
      "provider":  "agnes",             // provider id
      "format":    "openai",            // "openai" | "anthropic"
      "tier":      "free",              // 该 provider 层级
      "keys":      44,                  // 池内**真实可用** key 数（不含未解析占位符）
      "unresolved": 0,                  // 未解析的 key 指针数（env:/vault: 没取到值）
      "cooling":   1,                   // 冷却/隔离中的 key 数（429 冷却 + 401/403 长禁）
      "backed_off": false               // 池级退避中（全 key 挂过）
    }
    // … 每个 provider 一条
  ],
  "metrics": {                           // 对外服务质量（2026-09-21 新增，见下）
    "window_min":   60,                  // 滚动窗口长度（分钟）
    "total":        1284,                // 窗口内请求数
    "ok":           1268,
    "fail":         16,
    "success_rate": 0.988,               // 0..1；注意 total==0 时它也是 0，别把"没流量"读成"全失败"
    "avg_ms":       1480,
    "max_ms":       44647,
    "by_kind": {                         // 按 (format / kind) 分解，用于定位"是哪一类在坏"
      "anthropic/reasoning": { "total": 1200, "fail": 14 },
      "openai/literal":      { "total": 84,   "fail": 2 }
    },
    "degraded_sec": 12,                  // 当前连续失败已持续多少秒；0/缺省 = 此刻没在失败
    "last_fail_at": "2026-09-21T09:11:29+08:00",
    "last_5m":      { "total": 40, "ok": 40, "fail": 0, "success_rate": 1, "avg_ms": 900 }
  }
}
```

**密钥安全**：`keys` / `unresolved` / `cooling` 都是**数量**，不含任何 key 值；无请求体。
`metrics` 同理——只有计数与耗时，不含请求内容、不含模型名（字面模型统一归到 `literal`，
否则上游上百个模型号会把维度撑爆）。

> **2026-09-19（T-009）契约变更，前端实现前生效，无迁移成本**（T-008 仍是"🟡 跨仓待办、前端未实现"）：
> 1. **`keys` 语义修正**——此前实现返回的是配置里的原始条数（含未解析占位符），
>    于是"密钥一个都没接上、池实际 0 把、请求全 503"时它照样报 `keys: 3`。
>    契约写的一直是"真实 key 数"，**是代码没兑现**。现已改为池内真实可用数。
> 2. **新增 `unresolved`**——让"这个池是空的、因为 N 把 key 指针没解析出来"可被前端直接显示，
>    而不是看着一个非零的 `keys` 却等不到响应。建议 WorkBoard 在 `keys==0 && unresolved>0` 时
>    明示"密钥未装载（vault/环境变量取不到）"，这与"上游挂了"是两类不同故障。
> 3. **`cooling` 语义扩展**——现同时包含 429 冷却（60s）与 401/403 长禁（1h，key 已被上游禁用/吊销）。

> **2026-09-21（T-019）新增 `metrics`，纯加字段，对既有订阅方无影响**（前端只挑认识的字段读）：
>
> **为什么并进 status 而不是新开主题**：主题是**注册制**的（`NEURAL_BACKBONE §四`），
> 新主题要登记；而"我现在的状态"本来就该包含"我最近干得怎么样"。
>
> **为什么这些数字是零成本的**：全部来自**真实请求**（每次请求本来就经过网关，
> 成功没成功/花了多久它本来就知道），**没有任何新增探测**。
> 这是刻意的——另起进程去探测，就是在烧它本该保护的那份免费配额
>（`model-watchdog` 的前车之鉴：30–45s 探针 × 每模型 ≈ 2000+ req/天/模型，
>  而 OpenRouter 免费档只有 50 req/天/模型）。
>
> **`degraded_sec` 是给触发用的**：网关绝大多数失败是**设计来自愈**的
>（key 冷却 60s / 池退避 60s / 免费层恢复 ~180s / dev-fleet 拉进程 300s）。
> 所以「有失败」不是异常，「**失败超过了自愈承诺的时间**」才是。
> 消费方的判据应当是：`degraded_sec > N × 该模式的承诺时间` 才升级；
> 没过阈值就**不要**打扰——2026-09-21 那 9.5 小时里降级 7 次，**7 次全是自愈能搞定的**，
> 按"有失败就叫"会白叫 7 次。

### 3.2 event（`aimly.system.freetier.event`）

```jsonc
{
  "at": "2026-09-13T09:00:00+08:00",
  "from": "paid",                        // 切换前层
  "to":   "free",                       // 切换后层（jitter 时 from==to）
  "reason": "free-healthy>=T"           // 见下表
}
```

| reason | 含义 | WorkBoard 呈现建议 |
|--------|------|------|
| `all-free-unavailable` | 观测到免费层被证伪（免费候选真的试过且全挂） | 橙色"免费全挂，正在兜底" |
| `free-healthy>=T` | 免费连续健康≥T → 观测档位回摆 free | 绿色"免费恢复" |
| `jitter-aborted` | 抖动（<T 又挂）→ 观测档位不摆（I5 降噪） | 黄色"免费抖动" |

> ⚠️ **2026-09-20（T-015）起，这条时间线与 `tier` 字段都只是「观测」，不是路由状态。**
> 路由的兜底是**每请求**的：免费候选这一次不可用，这一次才落付费；下一条请求照常先试免费。
> 所以"已切付费"**不等于**接下来这段时间的流量都走付费——实际服务情况看同一份
> status 快照里的 `pools`（逐池 `backed_off` / `cooling`），那才是一手事实。
> 前端措辞已按此调整，避免"面板说付费、实际在走免费"这种自相矛盾的呈现。

## 四、订阅示例

### 4.1 nats.cli（CLI 侧）

```bash
nats sub aimly.system.freetier.>
# 或只看事件：
nats sub aimly.system.freetier.event
```

### 4.2 aimly-console（TS 侧，nats.ws）

```ts
import { connect, NatsConnection } from 'nats';

let nc: NatsConnection;
async function watchMoreToken(url: string) {
  nc = await connect({ servers: url });
  const sub = nc.subscribe('aimly.system.freetier.>', { ordered: true });
  for await (const m of sub) {
    const payload = JSON.parse(new TextDecoder().decode(m.data));
    const isStatus = m.subject.endsWith('status');
    if (isStatus) {
      console.log('freetier 当前层:', payload.tier, '池:', payload.pools);
    } else {
      console.log('freetier 事件:', payload.reason, `${payload.from}→${payload.to}`);
    }
  }
}
```

> WorkBoard 把 `status.tier` 渲染为主状态灯（free=绿 / paid=橙），
> `event` 流渲染为时间线（切层留痕）。

## 五、验收

- 后端侧（本仓）：`nats` 包发布 + 降级测试全绿（T-005 DoD）
- 前端侧（aimly-console）：订阅本契约两主题，渲染 status 灯 + event 时间线（跨仓，另行验证）
