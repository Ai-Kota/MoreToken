package auth

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"moretoken/internal/config"
)

// VaultWriter 发放面对密码墙的全部需求：存明文、删明文（回滚用）。
// 接口注入式设计（镜像 config.Resolver）：单测给 mock，不碰真墙。
type VaultWriter interface {
	Put(path, plain string) error
	Delete(path string) error
}

// vaultTimeout 单次 vault 调用的超时。发放面是 CLI 同步操作，
// 墙无响应时快速失败并给出可操作的错误，而不是挂住终端。
const vaultTimeout = 15 * time.Second

// vaultBin 解析 vault 二进制：VAULT_BIN 覆盖，默认与 config.DefaultVaultBin 同源。
func vaultBin() string {
	if b := os.Getenv("VAULT_BIN"); b != "" {
		return b
	}
	return config.DefaultVaultBin
}

// CliVaultWriter 经 exec 调本机 vault.exe 的默认实现。
//
// 守"纯标准库 · 零外部依赖"铁律：不引 vaultcli 库，镜像 config.defaultVaultGet
// 经 exec 调用的既有做法。加密能力只存在于 vault 二进制内，本进程从不接触墙的存储。
//
// **明文一律走 stdin，绝不进 argv**（TH3 补充）：命令行参数对同机所有进程可见
// （/proc、任务管理器、进程审计日志），`--data <明文>` 等于把 token 广播给全机。
// `--data @-` 让 vault 从 stdin 读，明文只存在于管道缓冲区。
type CliVaultWriter struct{}

// Put vault put <path> --data -，明文经 stdin 管道传入。
//
// vault 的 stdin 记号是裸 `-`（实测：`--data @-` 会被当成"名为 - 的文件"报
// `open -: The system cannot find the file specified`）。help 里的 `@file|-`
// 是"两种取值"，`@file` 读文件、`-` 读 stdin，别把两者拼起来。
func (CliVaultWriter) Put(path, plain string) error {
	return runVault(func(ctx context.Context, bin string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, bin, "put", path, "--data", "-")
		cmd.Stdin = strings.NewReader(plain)
		return cmd
	}, "put "+path)
}

// Delete vault delete <path>（Issue 事务回滚用）。
func (CliVaultWriter) Delete(path string) error {
	return runVault(func(ctx context.Context, bin string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, bin, "delete", path)
		cmd.Stdin = nil
		return cmd
	}, "delete "+path)
}

func runVault(makeCmd func(context.Context, string) *exec.Cmd, what string) error {
	bin := vaultBin()
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("vault unavailable (%s): %s — 无墙环境可改用 -token-plain（回显一次，自担保管）", bin, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), vaultTimeout)
	defer cancel()

	cmd := makeCmd(ctx, bin)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("vault %s: %v (%s)", what, err, truncate(errBuf.String(), 200))
	}
	return nil
}

// truncate 截断过长的 stderr 摘要（与 config.truncate 同款；跨包不复用未导出符号）。
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
