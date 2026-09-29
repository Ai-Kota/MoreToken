# syntax=docker/dockerfile:1.7
#
# MoreToken 容器镜像（T-026）。
#
# 设计前提（见 docs/tasks/T-026.md）：**vault 解析发生在宿主**，容器只消费
# 已解析好的完整 config（经命名卷挂进来）。因此镜像里**没有 vault.exe**、
# 也不需要——运行路径零 vault 依赖正是 T-024 校验面设计的红利。
# 容器里唯一的外部 CLI 是 nats（状态发布，增强非依赖，缺失时静默降级 no-op）。

# ---- 构建阶段：静态编译 Linux 二进制 ----
FROM golang:1.25-alpine AS build
WORKDIR /src

# 仓根有 go.work（含 scripts/ 等本地模块），容器里只有 moretoken 一个模块——
# 关掉 workspace 模式，否则会去找 .dockerignore 排除掉的兄弟模块而构建失败。
ENV GOWORK=off CGO_ENABLED=0 GOOS=linux

# 先只拷 go.mod 拉依赖层（本仓纯标准库，此层近乎空，但保留惯例以利将来）。
COPY go.mod ./
# 再拷源码。COPY . . 受仓根 .dockerignore 约束（排除 .git/config/bin/docker/runtime 等）。
COPY . .

# -trimpath 去掉宿主路径；-s -w 去符号表减小体积。纯标准库 → 真静态，可跑在 scratch/alpine。
RUN go build -trimpath -ldflags="-s -w" -o /out/moretoken . \
 && /out/moretoken -h 2>&1 | head -1 || true

# ---- 运行阶段：最小 alpine + nats CLI ----
FROM alpine:3.21 AS runtime

# ca-certificates：NATS 走 TLS(nats://…:14222) 要系统信任库（NATS_CA_FILE=system 依赖它）。
# tzdata：日志/过期时间用本地时区。wget/unzip 仅构建期取 nats 用，装完即删。
RUN apk add --no-cache ca-certificates tzdata

# nats CLI（linux-amd64，pin 版本 + sha256）。**校验和是供应链闸**：
# 值来自 2026-09-29 从官方 nats-io/natscli v0.4.0 release 下载实测（与宿主 nats.exe 0.4.0 对齐）。
# 换版本必须同时更新 sha256，否则构建失败——这正是它的意义。
ARG NATS_VER=0.4.0
ARG NATS_SHA256=8dbd437c826b953dbd7432cf890ef22ba3c33dccc3dce5e71b3e8d055427849c
RUN apk add --no-cache wget unzip \
 && wget -qO /tmp/nats.zip "https://github.com/nats-io/natscli/releases/download/v${NATS_VER}/nats-${NATS_VER}-linux-amd64.zip" \
 && echo "${NATS_SHA256}  /tmp/nats.zip" | sha256sum -c - \
 && unzip -q /tmp/nats.zip -d /tmp/nats \
 && install -m 0755 "/tmp/nats/nats-${NATS_VER}-linux-amd64/nats" /usr/local/bin/nats \
 && rm -rf /tmp/nats /tmp/nats.zip \
 && apk del wget unzip \
 && nats --version

# 非 root 运行（uid 10001）。HOME=/tmp：read_only rootfs 下 nats CLI 需要一个可写 HOME，
# 而 /tmp 是 compose 挂的 tmpfs（唯一可写点）。
RUN adduser -D -u 10001 moretoken
ENV HOME=/tmp USER=moretoken

# 预建三个挂载点：read_only rootfs 下不能赌运行时替你创建目录。
# 属主无所谓（卷/挂载会覆盖），但目录必须在，否则挂载点是文件还是目录由 Docker 猜。
RUN mkdir -p /run/moretoken /run/mt-tokens /run/secrets/nats

COPY --from=build /out/moretoken /usr/local/bin/moretoken
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
# sed 去 CRLF：Windows 上 git 可能把 .sh 检出成 CRLF，alpine 的 /bin/sh 会在 `\r` 上炸。
# 与其赌 .gitattributes/core.autocrlf，不如构建期强制 LF——一行消灭整类脆弱。
RUN sed -i 's/\r$//' /usr/local/bin/entrypoint.sh && chmod 0755 /usr/local/bin/entrypoint.sh

USER moretoken
EXPOSE 8462

# entrypoint 做双文件存在性检查（缺失响亮失败），CMD 写死两个 flag——
# 默认值在容器里一个指向不存在路径（响亮崩）、一个指向卷内不存在文件（静默不鉴权），
# 两个默认值都不能留（T-026 评审 M2/S3）。
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["moretoken", "-config", "/run/moretoken/config.json", "-tokens-file", "/run/mt-tokens/tokens.json"]
