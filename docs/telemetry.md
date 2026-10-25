# 运行记录（T-050）——给智能体的优化证据

> 目的：一份**完整、可查询、重启不丢**的运行记录，供智能体分析问题、调优网关。
> 不做看板、不做告警、不引入 Prometheus/Grafana。
>
> 设计原则：只记事实；不记 key 明文、prompt、请求/响应正文。

## 一、文件位置与生命周期

```
docker/runtime/telemetry/telemetry-YYYY-MM-DD.jsonl
```

- 容器内经 `MORETOKEN_TELEMETRY_DIR=/var/lib/moretoken/telemetry` 追加写；
  宿主 `docker/runtime/telemetry/` 直接可读。
- 按天轮转；`MORETOKEN_TELEMETRY_RETENTION_DAYS`（默认 30）到期自动删除。
- 目录为空/不可写 ⇒ 记录禁用，只告警一次，**不影响路由**。
- 每行一个 JSON 对象，`type` 区分：`request` / `snapshot` / `boot` / `shutdown`。

## 二、request 行

每个 `/v1/messages`、`/v1/chat/completions` 请求一行。

| 字段 | 含义 |
|---|---|
| `ts` | RFC3339Nano 时间 |
| `request_id` | 进程内单调 ID（`boot_id-序号`） |
| `boot_id` | 本次进程标识（区分重启边界） |
| `client` | 入站 token name（不是 key 值） |
| `protocol` | `openai` / `anthropic` |
| `stream` | 是否流式 |
| `want` / `virtual` / `want_kind` | 客户端请求的模型原文、是否虚拟模型、类型 |
| `provider` / `model` | 最终服务（或失败）的 provider/model |
| `status` | HTTP 状态码 |
| `outcome` | 稳定分类，见下 |
| `fail_kind` / `fail_reason` | 路由失败类别 / 截断后的原因 |
| `total_ms` | 总耗时（流式为整段流时长） |
| `ttft_ms` | 首块时间（流式）；非流式 = 总耗时 |
| `req_bytes` / `resp_bytes` | 请求体 / 响应字节数 |
| `input_tokens` / `output_tokens` / `usage_known` | token 用量（能提取才记，绝不猜） |
| `budget_raised` | T-044 抬过 max_tokens 时的说明 |
| `config_hash` | 策略面指纹（不含 key） |
| `admission_mtime` / `admission_size` | 准入表版本 |
| `attempts[]` | **每次真实 key 尝试**：`provider/model/key_id/kind/status/reason/stream/elapsed_ms/body_bytes` |

### outcome 取值

| 值 | 含义 |
|---|---|
| `ok` | 2xx |
| `client_canceled` | 调用方主动取消（`context canceled`）——**不是网关失败** |
| `exhausted` | 候选链耗尽 → 503 |
| `context_too_long` | 确定性超限 → 400 |
| `model_not_found` | 字面模型无人持有 → 404 |
| `request_too_large` | 网关请求体上限 → 413 |
| `rate_limited` | 429 |
| `upstream_error` | 上游 5xx |
| `client_error` | 其他 4xx |
| `auth_error` / `forbidden` / `not_found` / `no_response` / `unknown` | 其他稳定分类 |

### key_id

形如 `agnes-anthropic/key3`，是池内序号，**不是 key 明文**。
成功请求也有 attempts —— 能回答“这次是哪把 key 服务的”。

## 三、snapshot 行

每 60 秒一行（无流量时也有），字段：

- `uptime_s`
- `tier`：`free` / `paid`（观测值，不参与路由）
- `pools[]`：`provider/format/tier/keys/unresolved/cooling/backed_off`
- `metrics`：60 分钟滚动窗口（total/ok/fail/success_rate/avg_ms/max_ms/by_kind/degraded_sec/last_5m）
- `admission`：`active/models/reasoning/coding/min_ratio/err`
- `config_hash` / `admission_mtime` / `admission_size`

`boot` / `shutdown` 行记录进程边界。

## 四、智能体怎么用

### 1. 一键摘要

```bash
python scripts/mt-report.py                # 最近 1 天
python scripts/mt-report.py --days 7
python scripts/mt-report.py --json         # 机器可读
```

输出：成功率（**剔除客户端取消**）、outcome 分布、协议/客户端/provider/kind、
总延迟与 TTFT 分位、每模型/每 key 的尝试与失败、Top 失败原因、最新快照。

### 2. 直接查 JSONL

```bash
# 最近 100 条失败
jq -c 'select(.type=="request" and .outcome!="ok" and .outcome!="client_canceled")' \
  docker/runtime/telemetry/telemetry-*.jsonl | tail -100

# 某模型的分位
python scripts/mt-report.py --days 7 --json | jq '.models'

# 用 DuckDB 做 SQL（可选）
duckdb -c "select provider, model, count(*) n,
           quantile_cont(total_ms, 0.95) p95
           from read_json_auto('docker/runtime/telemetry/*.jsonl')
           where type='request' group by 1,2 order by n desc"
```

### 3. 优化问题 → 证据

| 问题 | 看什么 |
|---|---|
| 哪个模型适合 coding/reasoning | `models` 的 count/ok/fail/p95 + `want_kind` |
| 哪把 key 在撞墙 | `keys` 的 fails/fail_kinds（429/401/403） |
| fallback 救回了吗 | `attempts[]` 链 + 最终 outcome |
| 准入表是否卡死供给 | snapshot 的 `admission` + `kind=admission_empty` 尝试 |
| 6 秒取消是谁的问题 | `outcome=client_canceled` 的 `ttft_ms`/`client`/`stream` |
| 改配置有没有效 | 对比不同 `config_hash` / `admission_mtime` 前后 |

## 五、隐私与边界

- **不记录**：key 明文、prompt、请求/响应正文、Authorization 头。
- key 只记池内 ID；client 只记 token name。
- 失败原因截断到 300 字节，避免上游大 body 撑爆一行。
- 记录文件 0644；目录不含秘密，但不要往 git 里提交（`docker/runtime/` 已忽略）。

## 六、验收

- [x] 成功请求也有 attempts（含 key_id）
- [x] 客户端取消单独分类，不污染成功率
- [x] 跨日轮转 + 30 天清理
- [x] 无流量时 60s 快照仍在
- [x] 记录中 grep 不到 `mt_` / `sk-` / `vault:`
- [x] `go test ./...` 全绿；`mt-report.py` 可读真实文件
