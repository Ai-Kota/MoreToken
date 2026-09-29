package auth

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestCLI_GenVaultMode 默认发放：明文入墙、不回显、tokens.json 只有哈希+指针。
func TestCLI_GenVaultMode(t *testing.T) {
	path := storePath(t)
	vw := newMockVault()
	var out bytes.Buffer
	code := CLI{
		Command: "gen", Name: "agent-a", TTL: DefaultTTL,
		TokensFile: path, VW: vw, Out: &out,
		Now: time.Now(),
	}.Run()
	if code != 0 {
		t.Fatalf("exit = %d, out = %s", code, out.String())
	}
	plain := vw.puts[VaultPathFor("agent-a")]
	if plain == "" {
		t.Fatal("明文应已入墙")
	}
	if strings.Contains(out.String(), plain) {
		t.Fatal("vault 模式**不得回显明文**（TH3）")
	}
	if !strings.Contains(out.String(), "vault env") {
		t.Fatal("应给出指针接入示例")
	}
	// tokens.json 无明文、有指针
	entries, err := List(path)
	if err != nil || len(entries) != 1 {
		t.Fatalf("List = %v, %v", entries, err)
	}
	if entries[0].Hash != Hash(plain) || entries[0].VaultPath != VaultPathFor("agent-a") {
		t.Fatalf("条目不对: %+v", entries[0])
	}
	if entries[0].Status(time.Now()) != "active" {
		t.Fatalf("状态 = %s", entries[0].Status(time.Now()))
	}
}

// TestCLI_GenPlainMode 无墙模式：回显一次、无 vault_path。
func TestCLI_GenPlainMode(t *testing.T) {
	path := storePath(t)
	var out bytes.Buffer
	code := CLI{
		Command: "gen", Name: "no-vault", TTL: 0, Plain: true,
		TokensFile: path, Out: &out,
	}.Run()
	if code != 0 {
		t.Fatalf("exit = %d, out = %s", code, out.String())
	}
	s := out.String()
	if !strings.Contains(s, TokenPrefix) {
		t.Fatal("plain 模式应回显明文一次")
	}
	entries, _ := List(path)
	if len(entries) != 1 || entries[0].VaultPath != "" {
		t.Fatalf("plain 模式不应有 vault_path: %+v", entries)
	}
	if entries[0].Expires != nil {
		t.Fatal("ttl=0 → 永不过期")
	}
	// 回显的明文确实能过校验（回显的不是别的东西）
	plain := ""
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, TokenPrefix); i >= 0 {
			plain = strings.TrimSpace(line[i:])
		}
	}
	if Hash(plain) != entries[0].Hash {
		t.Fatal("回显明文与登记哈希不匹配")
	}
}

// TestCLI_GenErrors 缺 name / 同名 active 重复发放 → exit 1 + 人话错误。
func TestCLI_GenErrors(t *testing.T) {
	path := storePath(t)
	vw := newMockVault()
	var out bytes.Buffer
	if code := (CLI{Command: "gen", TokensFile: path, VW: vw, Out: &out}).Run(); code != 1 {
		t.Fatal("缺 name 应 exit 1")
	}
	if !strings.Contains(out.String(), "-token-name") {
		t.Fatalf("错误应指路: %s", out.String())
	}
	out.Reset()
	mustCLI(t, CLI{Command: "gen", Name: "dup", TTL: DefaultTTL, TokensFile: path, VW: vw, Out: &out})
	out.Reset()
	if code := (CLI{Command: "gen", Name: "dup", TTL: DefaultTTL, TokensFile: path, VW: vw, Out: &out}).Run(); code != 1 {
		t.Fatal("同名 active 重复发放应 exit 1")
	}
	if !strings.Contains(out.String(), "already active") {
		t.Fatalf("应说明冲突与出路: %s", out.String())
	}
}

// TestCLI_ListAndRevokeAndRotate 全生命周期走 CLI 一遍（矩阵"生命周期"链）。
func TestCLI_ListAndRevokeAndRotate(t *testing.T) {
	path := storePath(t)
	vw := newMockVault()
	now := time.Now()
	mustCLI(t, CLI{Command: "gen", Name: "agent-b", TTL: DefaultTTL, TokensFile: path, VW: vw, Out: &bytes.Buffer{}, Now: now})
	plain1 := vw.puts[VaultPathFor("agent-b")]

	// list：表头 + 条目 + active 状态
	var out bytes.Buffer
	if code := (CLI{Command: "list", TokensFile: path, Out: &out, Now: now}).Run(); code != 0 {
		t.Fatalf("list exit = %d", code)
	}
	for _, want := range []string{"NAME", "agent-b", "active", "moretoken/tokens/agent-b"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("list 输出缺 %q:\n%s", want, out.String())
		}
	}

	// rotate：指针不变、旧废新生
	out.Reset()
	mustCLI(t, CLI{Command: "rotate", Name: "agent-b", TTL: DefaultTTL, TokensFile: path, VW: vw, Out: &out, Now: now.Add(time.Second)})
	plain2 := vw.puts[VaultPathFor("agent-b")]
	if plain2 == plain1 {
		t.Fatal("轮换应换新明文")
	}
	if !strings.Contains(out.String(), "指针不变") {
		t.Fatalf("应告知客户端零动作: %s", out.String())
	}
	s, _ := LoadStore(path)
	if _, ok := s.Verify(plain1); ok {
		t.Fatal("旧 token 轮换后应失效")
	}
	if _, ok := s.Verify(plain2); !ok {
		t.Fatal("新 token 应可用")
	}

	// revoke：热加载后下一请求即拒
	out.Reset()
	mustCLI(t, CLI{Command: "revoke", Name: "agent-b", TokensFile: path, Out: &out, Now: now})
	if !strings.Contains(out.String(), "热加载") {
		t.Fatalf("应告知无需重启: %s", out.String())
	}
	s.ReloadIfChanged()
	if _, ok := s.Verify(plain2); ok {
		t.Fatal("吊销后应拒")
	}

	// revoke 已全废的 name → exit 1（CLI 可见性）
	out.Reset()
	if code := (CLI{Command: "revoke", Name: "agent-b", TokensFile: path, Out: &out, Now: now}).Run(); code != 1 {
		t.Fatal("重复吊销应 exit 1")
	}
	// rotate 不存在的 name → exit 1
	if code := (CLI{Command: "rotate", Name: "ghost", TokensFile: path, VW: vw, Out: &out, Now: now}).Run(); code != 1 {
		t.Fatal("轮换幽灵 name 应 exit 1")
	}
}

// TestCLI_ListEmpty 空名单 → 友好提示 + exit 0（不是错误）。
func TestCLI_ListEmpty(t *testing.T) {
	var out bytes.Buffer
	code := CLI{Command: "list", TokensFile: storePath(t), Out: &out}.Run()
	if code != 0 || !strings.Contains(out.String(), "不鉴权") {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
}

// TestCLI_VaultDownFailsLoud 墙不可用 → exit 1 + 给出 -token-plain 出路（发放面失败必须响）。
func TestCLI_VaultDownFailsLoud(t *testing.T) {
	vw := newMockVault()
	vw.putErr = errVaultDown
	var out bytes.Buffer
	code := CLI{
		Command: "gen", Name: "x", TTL: DefaultTTL,
		TokensFile: storePath(t), VW: vw, Out: &out,
	}.Run()
	if code != 1 {
		t.Fatal("墙挂应 exit 1")
	}
	if !strings.Contains(out.String(), "wall is down") {
		t.Fatalf("应透传原因: %s", out.String())
	}
}

// TestCLI_UnknownCommand 未知子命令 → exit 1。
func TestCLI_UnknownCommand(t *testing.T) {
	if code := (CLI{Command: "dance", Out: &bytes.Buffer{}}).Run(); code != 1 {
		t.Fatal("未知命令应 exit 1")
	}
}

func mustCLI(t *testing.T, c CLI) {
	t.Helper()
	if code := c.Run(); code != 0 {
		t.Fatalf("CLI %s exit = %d", c.Command, code)
	}
}

var errVaultDown = &vaultDownError{}

type vaultDownError struct{}

func (*vaultDownError) Error() string { return "wall is down" }
