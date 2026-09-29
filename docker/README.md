# MoreToken Docker 部署

两种形态，**同一份镜像与代码**（功能面零差异，验证矩阵两边通用）：

| | **generic（默认推荐）** | **vault（密码墙机器）** |
|---|---|---|
| 密钥来源 | config 里 `env:VAR` 引用 + `docker/.env.keys` 注入 | config 里 `vault:PATH` 指针，宿主 materialize 解析 |
| 宿主依赖 | 只要 docker | docker + Go 工具链 + vault.exe |
| 明文落盘 | `.env.keys`（你本来就要放 key 的地方，chmod 600） | 命名卷内（Docker VM，不进仓库；管道投递不落宿主文件） |
| 部署入口 | `mt-up.sh` | `mt-deploy.sh` |
| token 管理 | `mt-token.sh`（one-shot 容器，-plain 回显一次） | 宿主 `bin/mt-host.exe`（明文直入墙不回显） |
| compose 文件 | `docker-compose.yml` | `docker-compose.vault.yml` |
| NATS 发布 | 可选（compose 里注释掉的 env 三件套） | 已接好（creds ro 挂载） |

两形态共用：`restart:always` 自愈 + healthcheck + read_only rootfs + uid 10001 +
no-new-privileges + 端口只绑 127.0.0.1 + tokens 目录 ro 挂载（吊销热加载 <5s）。

---

## Generic 形态（三步跑起来）

```bash
# 1. 配置：填入你的上游 base_url / 模型 / env:VAR 名
cp config/config.example.json config/config.json   # 然后编辑

# 2. 密钥：config 里每个 env:VAR 在这里给真值（本文件已被 gitignore）
cp docker/.env.keys.example docker/.env.keys       # 然后编辑；Linux/macOS: chmod 600

# 3. 起
bash docker/mt-up.sh
```

**发 token 并接入客户端**（不发 token = 不鉴权，仅回环可接受；发了 = 401 挡生人）：

```bash
bash docker/mt-token.sh gen my-laptop        # 明文回显一次，妥善保存
bash docker/mt-token.sh list
bash docker/mt-token.sh revoke my-laptop     # <5s 热生效，无需重启

# 客户端：API Key 位填这个 token
export ANTHROPIC_BASE_URL=http://127.0.0.1:8462
export ANTHROPIC_AUTH_TOKEN=mt_xxx           # 或 OPENAI_API_KEY=mt_xxx
```

**多实例 / 旁路测试**（四个变量必须一起改——compose 同项目同服务名会互相 recreate）：

```bash
MT_PROJECT=mt2 MT_CONTAINER=mt2 MT_PORT=8463 MT_CONFIG=$PWD/config/other.json bash docker/mt-up.sh
```

**NATS 可选**：有 NATS 服务器的，在 `docker-compose.yml` environment 里取消注释
`NATS_SERVERS`/`NATS_CA_FILE`（镜像已内置 linux nats CLI）。没有就什么都不用做——
发布器缺失即 no-op，路由主链路不受影响。

**验证**：`bash docker/mt-verify.sh`（15 断言；旁路实例加 `MT_CONTAINER=/MT_BASE=/MT_TOKEN_VIA=container`）。

---

## Vault 形态（密码墙机器，本仓作者的形态）

原则：**解析留宿主、运行进容器**。容器里没有也不能有 vault.exe——宿主把
`vault:` 指针解析成完整 config 经管道直投命名卷（明文不落宿主文件系统），
容器只消费成品，运行路径零 vault 依赖。

```bash
bash docker/mt-deploy.sh     # 部署/重部署：-check 供给闸 → materialize → 投卷 → up → 验证
bash docker/mt-verify.sh     # 只跑验证矩阵
```

- 投递带 **cmp 变更检测**：`SAME` → 容器全程不动（零停机）；`CHANGED` → 原子换入 + restart + 等 healthy + 全量矩阵
- token 生命周期在宿主（明文直入墙、不回显）：

```bash
bin/mt-host.exe -token-gen    -token-name dify-lan  -tokens-file docker/runtime/tokens.json
bin/mt-host.exe -token-revoke -token-name dify-lan  -tokens-file docker/runtime/tokens.json
bin/mt-host.exe -token-rotate -token-name laptop-cc -tokens-file docker/runtime/tokens.json  # 指针不变，客户端零动作
```

- 客户端接入（指针注入，配置文件零明文）：`vault env ANTHROPIC_AUTH_TOKEN=moretoken/tokens/laptop-cc -- claude`
- 上游 key 轮换 / config 变更后：重跑 `mt-deploy.sh`（re-materialize，CHANGED 自动 restart）

### 每日代谢任务（本机已注册）

计划任务 `MoreToken-DailyRedeploy`（每日 12:37）跑 `mt-deploy-logged.sh`：
harvest 纳新的新模型 / 轮换的 key 最迟 24h 自动收敛进容器（SAME 日零停机）。

```bash
tail -30 docker/runtime/deploy.log                    # 日志（1MB 自截断）
schtasks /run /tn MoreToken-DailyRedeploy             # 手动触发
schtasks /change /tn MoreToken-DailyRedeploy /disable # 停用（/delete /f 删除）
```

注：任务动作是**裸路径不带引号**（dev-fleet flashcheck 会自动包 hidden-run.vbs
隐藏窗口；动作带内嵌引号会被包装搞坏——两次 exit 1 的实测教训）。dev-fleet 退役后
包装消失任务照跑，只是会闪一次窗；介意就换动作载体为
`wscript.exe //B //NoLogo E:\AImlyForge\tools\agent\moretoken\docker\run-mt-deploy-hidden.vbs`。

---

## 开机自启链（两形态通用，三条例外）

正常链：**登录 → Docker Desktop(AutoStart) → daemon 拉起 `restart:always` 容器**。零手动。

1. **AutoStart 是登录项**：无人登录的重启不会起（要真无人值守需 Windows 服务方案，范围外）
2. **手动 `docker stop` 会跨重启保持停止**（`always` 也尊重显式停止；恢复：`docker start moretoken` 或重跑部署脚本）
3. **Docker reset/WSL 数据盘重建会清掉命名卷**（vault 形态）：entrypoint 检测到缺 config 会响亮 crash（绝不静默裸奔）→ 重跑 `mt-deploy.sh` 即恢复

## 排障

| 症状 | 查 | 恢复 |
|------|----|----|
| 容器 crash loop | `docker logs moretoken` | entrypoint 红字指路（缺 config→跑部署脚本；缺 tokens→查 runtime/） |
| 全绿但客户端 401 | `mt-token.sh list` / 宿主 `-token-list` | token 过期/吊销？重发或轮换 |
| 池全 503、日志一堆 `unresolved key` | `.env.keys` 变量名与 config 的 env: 对不上（generic）；vault 指针坏（vault 形态，materialize 会先挡住） | 对齐变量名重跑部署 |
| :8462 bind 失败 | `docker ps` + 宿主 netstat | 别的进程占着（旧手动实例/别的项目） |
| NATS 无 status | `docker logs moretoken \| grep -i nats` | creds/SERVERS 配置；NATS 是增强项，缺了路由照常 |
| token 操作报 `mkdir D:` | Git Bash MSYS 路径转换（已在 mt-token/mt-verify 内修） | 若自写 docker run 传 `/rt/...` 参数：加 `MSYS_NO_PATHCONV=1` + `cygpath -m` 挂载源 |
