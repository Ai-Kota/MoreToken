# 运维手册：自愈失效 → agent 处置

> 面向**被叫来处理 moretoken 问题的 agent / 人**。
> 触发信号是 `freetier-doctor`（见 `T-019`），不是"有失败"。

---

## 一、你是被什么叫来的

网关绝大多数失败**是设计来自愈的**：key 冷却 60s / 池退避 60s / 免费层恢复 ~180s /
dev-fleet 拉进程 300s。

> 实测（2026-09-21，9.5 小时窗口）：降级 **7 次**，每次 **3.0–8.1 分钟**自愈，**7 次全是自愈能搞定的**。

所以触发判据**不是"有失败"**，而是：

```
degraded_sec > 2 × 免费层自愈承诺（默认 6 分钟）
```

按这个判据过那 7 次：**只有 6.3 与 8.1 分钟那两次该升级**，其余 5 次不该。

**这意味着：你被叫来时，自愈已经试过并且失败了。** 不要再做自愈已经在做的事
（重启、清冷却、换 key），那不会有用——否则它自己就搞定了。

### 触发链路

```
网关 /doctor（在进程内算，只有它拿得到指标窗口）
   ↓ HTTP
moretoken.exe -doctor     退出码 0 正常 / 1 自愈失效 / 2 网关不可达
   ↓
dev-fleet 的 freetier-doctor（type: cli —— **只报告，不重启**）
   ↓ 心跳
NATS aimly.system.heartbeat.* → WorkBoard
```

`2` 与 `1` 刻意分开：**网关死了**归 `/health` 与 dev-fleet 的重启路径；
**网关活着但自愈失效了**才归这里。重启解决不了自愈失效，只会丢掉现场。

---

## 二、第一步：判断**可不可修**（不许跳过）

先分类，再动手。落到"不可修"就**出报告然后停**——硬修只会引入新故障。

| 类别 | 实例（都真实发生过） | 可修？ |
|---|---|---|
| **上游撤模型 / 改行为** | 09-20 那两条 `xkiro-anthropic` 404 | ✅ 改配置 |
| **配置缺 (F,K)** | `-check` 报出的供给面缺口 | ✅ 改配置 |
| **代码 bug** | 门控把候选删成空集 → `tried 0` → 全线 503（T-015） | ✅ 改代码 |
| **额度 / 预算耗尽** | 免费额度被打光 | ⚠️ 只能换池 / 加 key，通常要人 |
| **本机网络抖动** | `TROUBLESHOOTING §5` 记录的那个 | ❌ **谁都修不了** |
| **上游整体宕机** | 所有 provider 同时挂 | ❌ 只能等，或切付费 |

**判断方法**：先去 `docs/incidents/` 和 `TROUBLESHOOTING.md` 按症状搜——**这类问题很可能踩过**，
那两份文档记了当时的判据和方法。然后再看 `/decisions` + `/metrics` 定当前现场。

⚠️ 有一类是**假问题**：`degraded_sec` 顶起来了但 `success_rate` 仍然很高、
`by_kind` 只有一类在掉。那可能是某个 (F,K) 的**供给面塌了**（可以靠纳新修），
不是整机故障——别按整机故障处理。

---

## 三、安全边界（item 2，硬约束）

### 3.1 动手前先留回滚点

```bash
git add <你打算改的文件> && git commit -m "checkpoint: 修 XXX 之前的现场"
```

**没有 commit 就不许改代码。** 回滚 = `git revert` + 重编 + restart。

### 3.2 修复必须过**全部**部署闸，跑不过就不许部署

| 闸 | 命令 | 拦什么 |
|---|---|---|
| 变异检验 | （手写：把新测试对着**旧代码**跑一遍） | 测试是假的——它根本没覆盖旧行为 |
| 全量回归 | `bash scripts/test-all.sh` | 改坏了别处 |
| 供给面断言 | `bin/moretoken.exe -check -config config/config.json` | 改完之后需求面没被覆盖（**缺口即拒绝启动**） |
| staleness | `dev-fleet staleness` | 改了没编 / 编了没重启 |

**任何一条红 ⇒ 停下，不许部署。** 这四条不是建议，是这套系统唯一的安全网：
agent 自动改生产代码，错了的爆炸半径是**整个 LLM 网关**。

部署 = 构建 + 重启（`dev-fleet restart` **不重编**，必须显式 `go build -o bin/...`）：

```bash
cd E:/AImlyForge/tools/moretoken
go build -o bin/moretoken.exe .
./bin/moretoken.exe -check -config config/config.json
cd E:/AImlyForge/ops/dev-fleet && ./dev-fleet.exe restart moretoken
./dev-fleet.exe staleness | grep moretoken     # 必须显示已生效
```

### 3.3 修完必须验证"真身"，不是"测试绿"

- 起**旁路端口**实测（不碰在跑的 8462）：`bin/... -config config/config.json -listen 127.0.0.1:8463`
- 发一条**真实**请求，确认落到预期 provider/model
- 收尾 `taskkill //PID <pid> //F` —— **`TaskStop` 收不掉它**（只收 shell），必须核 `netstat`

### 3.4 禁止清单

| 禁止 | 为什么 |
|---|---|
| 改 vault / 凭据 | 不可逆，且影响别的工具 |
| 改 `config.json` 的 **provider 结构**（加删 provider、改 base_url、改 tier） | 那是**策略**，是人的决定。只许加模型（纳新走 `-harvest`） |
| 闸红着部署 | 唯一的爆炸半径控制手段 |
| 动 dev-fleet 的**其它服务** / 全局配置 | 你这是来修一个服务的 |
| 把"没找到根因"包装成"已修复" | 见 `E:/MASTER/真言.md`：没有判据的结论不算结论 |

---

## 四、当前状态：**半自动**（这是刻意的）

现在这条链路是：

```
doctor 报自愈失效 → dev-fleet 标 unhealthy → 心跳 → **人看到**
```

**最后一步没有自动拉起 agent。** 理由：

1. 自动 spawn 一个改生产代码的 agent，是这套系统里**风险最高的动作**；
2. 目前**没有"agent 改错了"的实战经验**，不知道它的失败模式长什么样；
3. 先跑一段"升级信号被人看到"，用真实数据回答两个问题再开：
   - **误报率**——6 分钟阈值是不是太紧？（实测只有 2/7 次该触发，但那是 n=7）
   - **可修率**——真被叫来时，有多少是第二节里"可修"那一栏的？

误报率高的自动升级，比不升级更糟：它训练人忽略告警——本仓已经在
"假绿训练人忽略闸"上栽过一次（`TROUBLESHOOTING §8`）。

**要开自动，前置条件**：至少积累两周的升级事件，误报率 < 20%，且第二节的可修性判断
在真实事件上验证过。
