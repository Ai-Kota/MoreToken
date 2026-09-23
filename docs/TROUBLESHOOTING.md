# 问题汇集（排查手册）

> **怎么用这份文档**：先按错误原文搜（症状字符串**原样保留**，可直接 grep），
> 再看「判据」一栏——那是把猜测与事实分开的地方。
> 每条都记了**当时是怎么定位的**，因为下次的现场不会长得一样，方法比结论耐用。
>
> 铁律：**没有判据的结论不算结论**（见 `E:/MASTER/真言.md`）。
> 本文的每条"根因"都附了复现或实测证据；只有推断的地方会显式写【推断】。

---

## 速查表

| 症状关键字 | 先看哪 | 一句话判据 |
|---|---|---|
| `no provider available for model` | §1（旧）/ §9（现行） | **2026-09-20 起档位不再参与路由**：这个错只剩"真的没有 provider 能提供该 kind"一种成因 |
| `exhausted ... candidates (tried 0` | §1 / §2 / §9 | `tried 0` = **一个候选都没试**，不是"试遍了都挂" |
| `state-machine` / `all-free-unavailable` | §2 / §9 | `/decisions` 里的事件留痕。**它现在只是观测留痕，不再改变路由**——看到它不等于免费流量真的受影响 |
| `all ... candidates cooling (tried 0` 且同刻免费池在出 200 | §9 | 门控已删；若再见到，先查是否跑着**旧 bin**（§6 staleness） |
| `model_not_found` / `Model "m" does not exist` | §3 | 上游拒绝未知模型名——探测体带假名就会恒判不健康 |
| `superfluous response.WriteHeader` | §1 | 是**结果**不是原因：流已开头又走 503 分支 |
| `lookup www.aimly.top: no such host` | §5 | 本机到云端的间歇抖动，**不是**本服务的锅 |
| `【待生效】源码新于 bin` | §6 | dev-fleet 的 staleness 闸；`restart` **不重编** |

---

## §1 auto:reasoning 全线 503（2026-09-19）

### 症状

```
POST /v1/messages  {"model":"auto:reasoning"}
→ 503 {"error":"no provider available for model \"auto:reasoning\"",
       "detail":"router: exhausted anthropic candidates (tried 0, limit 4)"}
```

同刻 `{"model":"auto"}` 却是 200。**这组对照本身就是判据**：不是"模型不存在"，
而是"档位不对"——`auto` 能过、`auto:reasoning` 不能过，说明路由走到了一个
没有能力提供 reasoning 的地方。

### 定位路径（可复用）

1. **`tried 0` 是自相矛盾的**。`router.go` 在候选为空时本来会走上面那个 return
   （报 `no ... provider offers kind ...`）。能走到函数尾报 `tried 0`，说明候选
   **入循环前非空、被清空了**——唯一能清空它的是 tier 门控 `paidOnly()`。
   ⇒ 当前档位必然是**付费**。
2. **验证档位**：发一条 `{"model":"auto"}`，看 `/decisions` 里落到哪个 provider。
   落在 `agnes-paid-*` = 付费档；落在 `agnes-anthropic` 等 = 免费档。
   （没有专门的档位查询端点，这是当时可用的间接探法。）
3. **`/decisions?n=40`** 看事件留痕：`provider_id: "state-machine"` 的行是状态机事件，
   `reason: "all-free-unavailable"` 就是进付费档的时刻。

### 根因（三层，每层独立可致故障）

| # | 缺陷 | 位置 | 判据 |
|---|---|---|---|
| A | 探测体硬编码假模型名 `"m"`，上游一律拒 → 探测恒判不健康 → **回免费分支在生产中不可达** | `internal/provider/provider.go` `ProbeBody` | 实测：假名 agnes 503 `model_not_found` / xkiro 404 `Model "m" does not exist`；换真名 4 个 provider 全 200 |
| B | 池级退避窗口内的候选被当成"不可用"，一个请求都没发就触发降级 | `internal/router/router.go` `Route` | 复现测试（全候选退避 → sink 被调用 1 次） |
| C | 门控清空候选后不复查，报误导性的 `tried 0` 并照常触发降级 | `internal/router/router.go:212-223` | 代码路径；**未修**（见 §7） |

### 时间线（判据来自 `/decisions` + 进程日志）

```
16:37:27 / 16:37:48   auto:reasoning → agnes-anthropic(免费) 200     ← 免费层是好的
16:41:01              http: superfluous response.WriteHeader (proxy.go:129)   ← 有请求失败
16:41:01              state-machine | all-free-unavailable             ← 就是它触发的降级
16:47:15 起           auto / auto:coding → agnes-paid-anthropic        ← 已在付费档
```

**关键**：免费池当时并没有用完（前 4 分钟还在正常出 200，事后实测四个 provider 全健康）。
那次降级是**误判**。把"瞬态耗尽"当既定前提接受，是当时分析上的错。

### 修法

| 提交 | 改了什么 |
|---|---|
| `f8b8cd1` | `ProbeBody(model)` 用 provider 真模型名；`probeModel` 取 `p.Models[0].ID`，无模型按不健康处理 |
| `555e60c` | `Route` 记 `attempted`，`attempted==0` 时**不降级**，报 `all ... candidates cooling` |
| `e1cb528` | 回归判定改「任一健康」；探活成功真解禁；NATS 发布抗抖（§4/§5） |
| **T-015**（2026-09-20） | **删层门控**（`paidOnly`/`TierGate`）。上面三条都没治到根：只要系统停在"付费档"，`auto:reasoning` 依然全线 503——因为门控把免费候选删掉、而付费档没有 reasoning 模型。详见 §9 |

> ⚠️ **本节的历史结论有半边已作废**：`tried 0` 的成因之一是"门控清空候选"，
> 那条路径**已随门控一并删除**（T-015）。本节保留原样作为排查方法的教学，
> 但**别再按"档位不对"这条线索走**——档位自 2026-09-20 起不参与路由。

### 验证方式（这案子值得抄的做法）

- **变异检验**：把新写的回归测试对着**旧代码**跑一遍，确认它失败。
  `ProbeBody` 那条报 `实际发出 [m]`；`state` 那条报 `tier = 1, want Free`——
  和事故症状一模一样。**没做过变异检验的回归测试等于没写。**
- **对照实验**：同一函数只换一个变量（模型名），假名 503/404 vs 真名 200。
- **真实链路回归**：用真配置 + 真 vault key + 真上游驱动状态机，验证"付费 → 免费"。
  临时验证件用完即删（`internal/state/live_verify_test.go` 已删）。

---

## §2 进了付费档就再也回不来

### 症状

`/decisions` 里有 `all-free-unavailable`，但**永远等不到** `free-healthy>=T`。
进程不重启就出不来（`state.New` 初始 tier=Free，重启即复位）。

### 判据

`internal/state/state.go` 里只有三处写档位：初始化（Free）、`AllFreeUnavailable`（Paid）、
`ProbeFree` 里满阈值的回归（Free）。**只有第三处能回免费**，而它要求
`healthyFor >= HealthyFor`——健康窗口只在探测成功时累积。

```bash
grep -n "s\.tier = " internal/state/state.go   # 应该只有 2 行：TierPaid / TierFree
grep -n "tier: TierFree" internal/state/state.go   # 另有 1 处初始化（New 里）
```

任何一处新的写入点都要警惕——多一条"直接改档位"的路径，就多一种绕过回归判定的方式。

### 根因与修法

两条，都指向"判死的门槛太低"：

1. **探测恒失败**（§1 缺陷 A）——修于 `f8b8cd1`。
2. **要求 4 个免费 provider *全部*健康才累积**——单个 provider 长期挂（某家 key 被
   集体吊销）就把系统**永久**钉在付费档，哪怕其余几十把 key 全是好的。
   修于 `e1cb528`：改「任一健康即可」，并**去掉短路**（探活侧要拿逐个结果去解禁，
   短路会让排在后面的池永远等不到解禁）。

### 顺带发现的假注释

`Router.ClearBackoff` 的注释写着"生产由 T-004 探测成功时驱动"，
`Pool.Clear` 写着"T-002 健康层会驱动它"——**实际生产路径零调用点**，
两条都是空头支票，导致 401/403 的 1h 长禁没有任何提前解除的途径。
修于 `e1cb528`：新增 `Router.MarkHealthy`，由 `main.go` 的 Probe 闭包在探测成功时调用。

> 教训：**注释里承诺的调用关系，要在代码里找得到调用点**。
> 查法：`grep -rn "\.Clear(\|ClearBackoff" --include=*.go . | grep -v _test.go`

---

## §3 探测相关的坑

### 探测体必须带真模型名

`ProbeBody` 曾硬编码 `"model":"m"`（两条格式分支还一模一样）。两个上游都拒绝未知模型名
→ `IsHealthy` 要求 2xx → **每个 provider 每轮探测都判不健康**。

**修法**：`ProbeBody(model string)`，调用方从 `p.Models[0].ID` 取（`probe.probeModel`）。

> 注意：模型 ID 可能含 `:`（xkiro 的 `minimax/minimax-m3:free`），必须原样携带，
> 不能被虚拟模型解析逻辑误处理。

### 探测只试第一把 key = 单点

`probe.Provider` 从 `round` 指定的起点轮换取最多 3 把，**命中任意一把即算健康**。
只认第一把的话，那把一挂就再也回不到免费——哪怕其余几十把全是好的。

### 探测只在付费档跑

`state.Run` 只在 `Tier() == TierPaid` 时探测（免费层"在用就是健康"）。
⇒ 探活解禁这类逻辑在免费档**不会触发**，改相关代码时别忘了这个前提。

---

## §4 判"免费层耗尽"的门槛

**原则（用户定性，2026-09-19）：免费池是主力，付费档只是兜底。**
免费池 40+ 把 key（agnes 33 + xkiro 11）覆盖 4 个 provider、多个模型，
正常负载下**不该被判"耗尽"**。

见到 `all-free-unavailable` / 档位切 paid，**先默认它是误判**，去找证据推翻。

"自我保护状态" **不是** 可用性证据：

| 状态 | 是什么 | 能不能当作"不可用" |
|---|---|---|
| 池级退避（60s） | "刚失败过，先别急着再打" | ❌ 不能（修于 `555e60c`） |
| key 429 冷却（60s） | "这个账号当下没额度" | ❌ 不能 |
| key 长禁（1h，401/403） | "凭据失效，重试无意义" | ⚠️ 需要提前解禁途径（修于 `e1cb528`） |

**当前口径（T-015 改写，2026-09-20）**：

1. **判"免费层耗尽"的门槛**：候选**真的试过**且全挂，**且其中至少有一个是免费候选**。
   退避跳过的一个都不试 → 只报错不降级；**只试探过付费候选** → 同样不算数
   （那不是"免费层耗尽"，是"这一层没轮到"）。
2. **但"判耗尽"这件事本身已经不要紧了** —— 它现在只写留痕、只发上报，**不再改变路由**。
   免费流量是否受影响，由各免费池自己的退避/冷却决定。旧口径下"判错一次 = 三分钟能力黑洞"
   的放大链已经断了。
3. **兜底档不得低于主力**：候选集恒为 free→paid，付费档**不删**免费候选。
   这条是本章其余规则的**前提**——它若破，下面所有"别误判"的努力都只是减少误判次数，
   而误判一次的代价是整层不可用。

---

## §5 NATS 发布失败

### 症状

```
WARN nats 发布失败（subject=aimly.system.freetier.status）：nats: error:
  dial tcp 203.25.219.173:14222: i/o timeout
WARN nats 发布失败（...）：nats: error: dial tcp: lookup www.aimly.top: no such host
```

### 判据：先分清是谁的锅

**同一时刻手动发一条**，能成功就是本机抖动，不是服务端故障：

```bash
E:/AImlyForge/tools/bin/nats.exe pub aimly.system.freetier.status '{"probe":1}' \
  --server "nats://www.aimly.top:14222" \
  --creds "C:/Users/aimly/.local/share/nats/nsc/keys/creds/AImlyCloud/AImlyCloud/bridge.creds"
```

⚠️ **不能用环境变量代替 flag**：`NATS_SERVERS` / `NATS_USER_CREDS` / `NATS_CA_FILE`
是 AImlyForge 的约定名，**stock nats CLI 一个都不认**（认 `NATS_URL` / `NATS_CREDS` / `NATS_CA`）。
用 env 会回落到 CLI 自己的 context（指向 `127.0.0.1:4222`）→ 报 TLS 证书错，
**看起来像另一回事**。详见 `internal/nats/publish.go` 的注释。

**验证快照真的在流**（比"没报错"强）：

```bash
E:/AImlyForge/tools/bin/nats.exe sub aimly.system.freetier.status --count 1 \
  --server "nats://www.aimly.top:14222" --creds "<同上 creds 路径>"
```

### 定性

**本机到云端的间歇性 DNS/网络抖动，全局性的**——context-store / jiutian /
master-loop 的日志里同样在报，不是本服务独有。本仓库能做的只是别让一次抖动
丢掉快照或拖住请求（`e1cb528`：3s→8s 超时 + 重试一次；事件回调改异步）。

**代价提醒**：2026-09-19 那次事故现场之所以没有池状态可查（只能靠探针反推），
就是因为这个链路当时断了。修好它 = 下次出事不用反推。

---

## §6 改完代码不生效

**`dev-fleet restart <id>` 不重编。** 必须显式 build：

```bash
cd E:/AImlyForge/tools/moretoken && go build -o bin/moretoken.exe .
cd E:/AImlyForge/ops/dev-fleet && ./dev-fleet.exe restart moretoken
./dev-fleet.exe staleness          # 应该显示"已生效（bin <时间>）"
```

判据：`staleness` 报 `【待生效】源码(...) 新于 bin(...)` 就是没生效。
也可以直接比时间：`find . -name "*.go" -newer bin/moretoken.exe`（应为空）。

---

## §7 工具箱（本机排查常用）

| 目的 | 命令 / 方法 |
|---|---|
| 看决策留痕 | `curl -s "http://127.0.0.1:8462/decisions?n=40"` |
| 看池健康 | `curl -s http://127.0.0.1:8462/health` （keys / cooling / backed_off） |
| 看模型清单 | `curl -s http://127.0.0.1:8462/v1/models` |
| 取真 key（勿打印） | `E:/AImlyForge/tools/PUBLIC/vault/bin/vault.exe get <path>` |
| 服务进程与命令行 | `powershell -NoProfile -Command "Get-CimInstance Win32_Process -Filter \"Name like '%moretoken%'\" \| Select ProcessId,CommandLine"` |
| 重启服务 | `cd E:/AImlyForge/ops/dev-fleet && ./dev-fleet.exe restart moretoken` |

### 两个环境坑（会让排查跑偏）

1. **本 shell 里 curl 的 TLS 是坏的**：明文 HTTP 通（`example.com` 200），
   HTTPS 一律 `curl: (43) A libcurl function was given a bad argument`。
   要打 HTTPS 用 PowerShell `Invoke-WebRequest`。
2. **PowerShell 5.1 读 UTF-8 无 BOM 的 .ps1 会解析错**（中文注释里的字节被误判）。
   临时脚本用纯 ASCII 注释，或存成带 BOM。

### 判据优先于症状

- `tried 0` ≠ "试遍了都挂"——先问"为什么一个都没试"。
- 上游返回的**原文**要留下来（`model_not_found`、`Model "m" does not exist`），
  它直接指出方向；顺手改写成"上游不可用"就把线索丢了。
- 怀疑"是环境还是代码"时，**同一时刻手动复刻一次**（§5）。

---

## §8 已知遗留（未修，留档）

1. ~~**门控清空候选后不复查**~~ —— **已消解**（T-015 把门控整个删了，这条路径不复存在）。
2. ~~**付费档缺 reasoning 模型**~~ —— **能力后果已消解**（T-015：付费档不再删免费候选，
   `auto:reasoning` 照常落免费）。但"付费兜底对 reasoning 实际失效"这件事**仍为真**——
   §4 的判断是对的：该补的不是配置，是别掉进兜底。若某天真要补，
   **先实测上游有没有付费 reasoning 模型，别猜**。
3. **本机到云端的 DNS/网络抖动**（§5）：不在本仓库范围，未治根。
4. ~~**"探活成功 → MarkHealthy"接线缺真实整轮验证**~~ —— 口径已改（T-015：探活不再只在
   付费档跑，改成"有池被隔离就探"），但**真实整轮仍未跑过**：需要制造"某池被隔离 →
   探活成功 → 解禁"的现场。仍未修。
5. **`/decisions` 不记 503**（T-015 新发现）：错误路径（`ferr != nil`）不写决策日志，
   于是 15:31:49–15:34:49 那段在 `/decisions` 里**是空白的**，只能靠前后对照反推。
   修它要动 `internal/proxy`，几行的事，但没做。
6. **进程日志断链**（T-015 新发现）：`ops/dev-fleet/logs/moretoken.log`
   停在 14:41 `shutting down`，而进程是 **14:54** 起的——45 分钟 stderr 一条没落盘。
   它是 §5 结尾"代价提醒"的同一个坑：出事时没有一手日志。归属 dev-fleet 的重定向，未治。
7. **`Route` 的 `i > r.maxRetry` 是下标上界**，不是注释说的"fallback 跳数"；
   被退避跳过的候选也吃预算。当前每格式 3 候选不咬人，**加 provider 就会静默截断链路**。

---

## §9 auto:reasoning 又 503 了，但这次不是"档位"（2026-09-20）

> **完整案子见 `docs/incidents/2026-09-20-autoreasoning-503.md`**——现场判据、定位路径、
> 根因推演、变异检验实录、牵出的四条缺陷、沉淀的原则，都在那份文件里。
> 本节只保留"按症状查"要用的部分；那份是"按时间读"的。
>
> §1 是同一个症状的上一代版本。**这一节的结论才是现行口径**——§1 的"档位"线索已作废。

### 症状

```
POST /v1/messages  {"model":"auto:reasoning"} → 503
```

用户在使用中报错。**没有留下错误原文**——因为 `/decisions` 不记 503（见 §8-5），
进程日志又断链（§8-6）。**这种情况下的定位方法是本节最值得抄的部分。**

### 定位路径（三条链全断时怎么查）

1. `/decisions` 看**状态机事件**，不是看错误：
   ```
   15:31:48.197  agnes-anthropic 200  auto:reasoning
   15:31:49.301  state-machine   all-free-unavailable
   15:32:08.405  agnes-anthropic 200  auto:reasoning   ← 关键
   15:34:49.565  state-machine   free-healthy>=T
   ```
2. **用"翻档前后免费池仍在出 200"证伪"免费层挂了"**。这是本节的中心判据：
   `15:31:48` 和 `15:32:08` 各有一条免费 200，而翻档就夹在中间。
   ⇒ 那次翻档是**误判**，真凶不是免费池。
3. **读代码确定"翻档之后会发生什么"**（配置是死的，可以静态推演）：
   付费档只有 `agnes-paid-anthropic`（`agnes-3.0-flash`，kinds=`[coding, general]`），
   而请求要 `reasoning` ⇒ 门控先把免费候选删光，`HasKind` 再删掉付费的那个
   ⇒ **候选成空集** ⇒ 循环一次都不进 ⇒ 报 `tried 0` ⇒ 503。
4. **用一条测试把第 3 步钉死**：`TestRoute_ReasoningServedWhenPaidLacksKind`
   （配置照抄生产子集）。变异检验报出的 `tried 0, limit 4` 与事故现场**逐字一致**，
   至此根因才算坐实——**在那之前它只是推断**。

### 根因

**"层"被实现成了过滤器。** `paidOnly()` 在付费档期间把免费候选从链上**删掉**，
于是"兜底"变成主力候选集的**子集**——付费档缺 reasoning 时，能力从 3 个候选塌成 0 个。

**兜底本来就不需要档位**：`candidates()` 返回 `free → paid` 的有序链，循环遇不可用候选
往后走就是原生兜底。层门控的**唯一净效果**是把付费从"这一次请求的备选"
提升为"接下来三分钟的首选"——那不是兜底，是把应急升格为主力。

### 修法（T-015）

删 `paidOnly` / `TierGate` / `WithTierGate` / `gate` 字段 / `Route` 里的门控分支。
状态机降为**观测器**（上报 + 定向探活），不再有路由否决权。详见 `docs/tasks/T-015.md`。

顺带修了两处同源缺陷：

- **sink 的证据口径**：免费候选全在退避窗口内被跳过、只试探过付费时，`attempted` 也非零
  → 照样报"免费耗尽"，而免费层**一个请求都没发过**。`555e60c` 的规矩只用在了一个出口。
- **探活时机**：原来"停在付费档才探全部"，于是"整体还在免费档、只有某个池被长禁"
  时没有解除途径。改成"有池被隔离就探"。

### 教训

- **"降级/兜底"的第一性要求：fallback 的候选集 ⊇ primary 的候选集。**
  违反它，触发兜底的唯一效果就是减少可用性——那不叫兜底。
- **粗粒度机制重造细粒度机制，是单点故障的温床。** 池级退避（60s / per-provider /
  到期自愈）已经解决了"别打刚挂的池"，全局档位没有增加信息，只增加了故障面。
- **防抖要对称，且门槛与代价成正比。** 误进兜底 = 整层黑洞，误回免费 = 一次无用尝试——
  而旧实现是前者零防抖、后者要 3 分钟证据，装反了。
- **观测链断了，就只能靠反推。** 这次能定住，全靠"翻档前后免费池还在出 200"这条对照——
  如果当时没有流量，连这一条都没有。

---

_最后更新：2026-09-20 · 相关提交 `f8b8cd1` `555e60c` `e1cb528` · T-015（删层门控）_
