// Package platform 平台发现：把"**已知**平台注册表 × vault 里**有 key** 的平台 ×
// config 里**已接线**的平台"做三向 diff，回答一个问题——**我们还缺哪些平台。**
//
// # 为什么需要（T-037）
//
// `catalog.Harvester` 只对 `cfg.Providers` 里**已声明**的 provider 发现新**模型**
// （`for i := range cfg.Providers`）——**永远不会发现一个新"平台"**。
// 于是池子的大小由人手写的一份 provider 列表定死（当前 7 个），
// vault 里有多少平台、外部有多少免费源，都白搭。
//
// 本包不接线（接线要过能力测试，见 T-036 的规矩），只**回答"缺什么"**：
//
//	✅ 已接线        —— 注册表 ∩ config（按 base_url 主机名匹配）
//	⚠️ 有 key 未接线  —— vault 里有凭据但没接（可直接接，先跑能力测试）
//	🆕 已知但无 key   —— **用户去注册**（这就是本包交付的那份清单）
//
// # 判据为什么按"主机名"
//
// config 的 provider 是**按协议拆条**的（agnes 与 agnes-anthropic 是同一个上游），
// 而注册表按**平台**记一条。用 base_url 的**主机名**做键，天然把同上游的多条归一，
// 且不依赖 config 里再加一个字段。
package platform

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// DefaultVaultBin 与 internal/config 的 DefaultVaultBin 一致（可用 VAULT_BIN 覆盖）。
const DefaultVaultBin = `E:\AImlyForge\tools\PUBLIC\vault\bin\vault.exe`

// VaultPrefix 平台凭据在密码墙里的命名空间。
const VaultPrefix = "freellm/provider/"

// Entry 注册表里的一条平台记录。
type Entry struct {
	Platform string `json:"platform"` // vault 命名空间用的平台名
	Name     string `json:"name"`
	BaseURL  string `json:"base_url"`
	Format   string `json:"format"`
	Homepage string `json:"homepage"`
}

// Registry 已知平台注册表。
type Registry struct {
	Comment   string  `json:"_comment"`
	Platforms []Entry `json:"platforms"`
}

// Load 读注册表。
func Load(path string) (*Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读平台注册表 %s: %w", path, err)
	}
	return Parse(raw)
}

// Parse 从字节解析注册表（纯函数，测试用）。
func Parse(raw []byte) (*Registry, error) {
	var r Registry
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("解析平台注册表（期望 {platforms:[…]}）: %w", err)
	}
	return &r, nil
}

// Report 三向 diff 的结果。三桶互斥，顺序即"该做什么"。
type Report struct {
	Wired    []Entry // 已接线
	HaveKeys []Entry // 有 key 未接线（可直接接）
	Missing  []Entry // 已知但无 key（用户去注册）
}

// Diff 计算三向 diff。
//
//   - vaultPlatforms：vault 里**有凭据**的平台名集合（见 ListVaultPlatforms）
//   - wiredHosts：config 里**已接线**的 provider 的 base_url 主机名集合
func (r *Registry) Diff(vaultPlatforms, wiredHosts map[string]bool) Report {
	var rep Report
	for _, e := range r.Platforms {
		switch {
		case wiredHosts[HostOf(e.BaseURL)]:
			rep.Wired = append(rep.Wired, e)
		case vaultPlatforms[e.Platform]:
			rep.HaveKeys = append(rep.HaveKeys, e)
		default:
			rep.Missing = append(rep.Missing, e)
		}
	}
	return rep
}

// HostOf 取 base_url 的主机名（大小写归一）。解析失败返回空串。
func HostOf(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Host)
}

// WiredHosts 从"已接线的 base_url 列表"算主机名集合。
func WiredHosts(baseURLs []string) map[string]bool {
	out := make(map[string]bool, len(baseURLs))
	for _, b := range baseURLs {
		if h := HostOf(b); h != "" {
			out[h] = true
		}
	}
	return out
}

// ListVaultPlatforms 经 exec 调本机 vault.exe 列出 `freellm/provider/` 下的平台名集合。
//
// 守"零外部依赖"：不引 vault 库，与 internal/config 的 defaultVaultGet 同款裸 exec。
// **只读条目名，不取任何明文**（`list` 不解密）。
func ListVaultPlatforms(ctx context.Context, bin string) (map[string]bool, error) {
	if bin == "" {
		bin = os.Getenv("VAULT_BIN")
	}
	if bin == "" {
		bin = DefaultVaultBin
	}
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("vault 不可用: %s", bin)
	}
	cmd := exec.CommandContext(ctx, bin, "list", "--prefix", VaultPrefix)
	cmd.Stdin = nil
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("vault list: %v (%s)", err, strings.TrimSpace(errBuf.String()))
	}
	return ParseVaultList(out.String()), nil
}

// ParseVaultList 从 `vault list --prefix freellm/provider/` 的输出里抠出平台名集合。
//
// 输出形如：
//
//	共 27 条
//	  freellm/provider/unorouter/unorouter      [] 2026-…
//
// 判据：每行第一个空白分隔的 token 是路径。**纯函数**，便于单测。
func ParseVaultList(s string) map[string]bool {
	out := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		path := fields[0]
		if !strings.HasPrefix(path, VaultPrefix) {
			continue
		}
		rest := strings.TrimPrefix(path, VaultPrefix)
		if i := strings.IndexByte(rest, '/'); i > 0 {
			out[rest[:i]] = true
		}
	}
	return out
}
