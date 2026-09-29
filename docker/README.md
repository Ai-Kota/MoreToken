# MoreToken Docker 部署（T-026）

网关容器化：**vault 解析留宿主、运行进容器、开机零手动**。设计依据与安全姿态见 `../docs/tasks/T-026.md`。

---

## 日常操作

```bash
# 部署 / 重新部署（幂等，任何怀疑时重跑）——解析 vault→投递卷→起容器→验证
bash docker/mt-deploy.sh

# 只跑验证矩阵（不重新部署）
bash docker/mt-verify.sh

# 看状态 / 日志 / 重启
docker ps --filter name=moretoken
docker logs -f moretoken
docker restart moretoken

# 停 / 起（注意 restart:always 语义，见下）
docker compose -f docker/docker-compose.yml stop
docker compose -f docker/docker-compose.yml start
```

---

## Token 生命周期

tokens.json 在 `docker/runtime/`（目录 ro 挂载进容器，**宿主 CLI 是唯一写者**）。
里面只有 SHA-256 哈希 + vault 指针，无明文，可安全入库。

**发放 / 轮换**（需要 vault，在宿主跑；明文直入墙、不回显）：

```bash
bin/mt-host.exe -token-gen    -token-name dify-lan -tokens-file docker/runtime/tokens.json
bin/mt-host.exe -token-rotate -token-name laptop-cc -tokens-file docker/runtime/tokens.json
```

**吊销 / 查看**（不需 vault，两种姿势等价）：

```bash
# A. 宿主 CLI
bin/mt-host.exe -token-revoke -token-name dify-lan -tokens-file docker/runtime/tokens.json
bin/mt-host.exe -token-list -tokens-file docker/runtime/tokens.json

# B. 容器内 linux 二进制（revoke/list 零 vault 依赖，可直接进容器跑）
docker compose -f docker/docker-compose.yml exec moretoken \
  moretoken -tokens-file /run/mt-tokens/tokens.json -token-revoke -token-name dify-lan
```

吊销/轮换经**目录挂载**传播，容器 mtime 热加载，**下一请求即生效**（实测 <5s，mt-verify #6 断言）。无需重启。

**客户端接入不变**（cc-ft 已接好，见 T-025）：

```bash
vault env ANTHROPIC_AUTH_TOKEN=moretoken/tokens/laptop-cc -- claude   # cc-ft 函数已内置
```

---

## 密钥轮换 / 上游 key 变更后

上游 key（config.json 里的 90 个 `vault:` 指针）轮换后，卷里的已解析 config 是旧的 → **重跑 `mt-deploy.sh`**（重新 materialize + 投递 + `compose up`）。容器无需手动重启，`up` 会按需重建。

---

## 新陈代谢（models.auto.json 纳新）——每日自动，T-028

链路：dev-fleet `freetier-harvest`（已改指 moretoken 宿主二进制）每日实测纳新写
`config/models.auto.json` → **计划任务 `MoreToken-DailyRedeploy`（每日 12:37）**跑
`mt-deploy-logged.sh` → re-materialize 并进卷内 config → **变更检测**决定动不动容器：

- `SAME`（多数日子，harvest 自节流 20h）→ 只轻量健康检查，**容器全程不动、零停机**
- `CHANGED`（纳新/换 key/改配置）→ 原子换入 + `docker restart` + 等 healthy + 全量验证矩阵

上游 **key 轮换同样被捕获**（materialize 输出逐字节比对）→ 每日任务顺带是 key 变更的
自动收敛点（最迟 24h 生效）。

```bash
# 日志（自带 1MB 截断）
tail -30 docker/runtime/deploy.log

# 手动触发一次（等价于到点执行）
schtasks /run /tn MoreToken-DailyRedeploy

# 停用 / 恢复每日任务
schtasks /change /tn MoreToken-DailyRedeploy /disable
schtasks /change /tn MoreToken-DailyRedeploy /enable

# 停用整个任务（删除）
schtasks /delete /tn MoreToken-DailyRedeploy /f
```

注：任务动作是裸 `bash.exe <脚本绝对路径>`（**路径无空格故无引号**——dev-fleet
flashcheck 会自动包一层 hidden-run.vbs 隐藏窗口，动作里带内嵌引号会被包装搞坏，
2026-09-29 实测两次 exit 1 的教训）。若 dev-fleet 退役后包装消失，任务照跑，
只是 12:37 会闪一次控制台窗口；介意的话把动作换成
`wscript.exe //B //NoLogo E:\AImlyForge\tools\agent\moretoken\docker\run-mt-deploy-hidden.vbs`
（自带隐藏宿主的备用载体，已实测可用）。

容器内不做 harvest（避免挂 docker socket 或自重启的复杂度）。手动 harvest：

```bash
bin/mt-host.exe -harvest -config config/config.json
```

---

## 开机自启链（三条例外，务必知道）

正常链：**登录 → Docker Desktop(AutoStart=true) → daemon 拉起 `restart:always` 容器 → 卷内 config 就位（无需 vault）**。零手动。

例外（都会让"零手动"失效，恢复方式如下）：

1. **AutoStart 是登录项，不是系统服务** —— 无人登录的重启（如半夜自动更新重启后停在登录界面）网关不会起。cc-ft 本就要求登录态，可接受；要真无人值守得改 Windows 服务方案（本任务范围外）。
2. **`restart: always` 语义** —— 只要不是被你显式 `docker compose stop`/`docker stop`，崩溃/宿主重启都会拉起。**但一旦你手动 stop 了，跨重启它会保持停止**（unless-stopped 陷阱；这里用 always 已比 unless-stopped 强，仍挡不住显式 stop）。恢复：`docker compose -f docker/docker-compose.yml start`。
3. **Docker Desktop "Reset to factory defaults" / WSL 数据盘重建会清掉命名卷** —— 卷里的已解析 config 没了 → entrypoint 检测到缺失 → 容器响亮 crash（红字指路，不静默裸奔）。恢复：重跑 `bash docker/mt-deploy.sh`（幂等，会重新解析投递）。

---

## 迁移期说明：dev-fleet /doctor 判据失效

`freetier-doctor` 探针不带 token，而 `/doctor` 受 T-024 鉴权保护 → 探针吃 401。已配合 `runDoctorClient` 改动：**401 归 exit 2（不可达/配置错）而非回落 ok**（防假绿，T-016 铁律）。dev-fleet 废弃前，该升级判据处于失效态——已在 T-026 消解节把 `freetier-doctor` 停用并记录，不留静默坏判据。

---

## 排障

| 症状 | 查 | 恢复 |
|------|----|----|
| 容器 crash loop | `docker logs moretoken` | entrypoint 红字会指路（缺 config→跑 mt-deploy；缺 tokens→查 runtime/） |
| 全绿但客户端 401 | `docker exec moretoken moretoken -tokens-file /run/mt-tokens/tokens.json -token-list` | token 过期/吊销？重发或轮换 |
| 全绿但**无鉴权**（不该发生） | 卷内 tokens 是否空名单 | runtime/tokens.json 有 active 条目吗；容器 CMD 是否带 -tokens-file |
| :8462 bind 失败 | `docker ps` + 宿主 `netstat` | 旧手动实例/dev-fleet 抢占了端口，先停（见 T-026 消解） |
| NATS 无 status | `docker logs moretoken \| grep -i nats` | creds 挂载在不在；NATS 是增强项，缺了路由照常 |
