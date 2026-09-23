package config

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ── IsPlaceholder ──

// TestIsPlaceholder 两种前缀、裸前缀、空串、真实 key。
//
// 裸 "env:" / "vault:"（长度恰为 4）是重点：旧实现用 len(k)>4 判断，
// 会把它当成真实 key 发上游（Authorization: Bearer env: → 401）。
func TestIsPlaceholder(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"env:AGNES_KEY_1", true},
		{"vault:freellm/provider/agnes/agnes-aimly", true},
		{"env:", true},   // 裸前缀——长度 4，旧实现放过它
		{"vault:", true}, // 同上
		{"env", false},
		{"vault", false},
		{"", false},
		{"sk-uP9TOcbXzHEtcaOAYXadGRIiMrUupmHo7KxEEShXOHxW0Y6N", false},
		{"cpk-abc", false},
		{"sk-xt-fdaa", false},
	}
	for _, tc := range cases {
		if got := IsPlaceholder(tc.key); got != tc.want {
			t.Errorf("IsPlaceholder(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// ── ResolveKeys ──

func cfgWith(keys ...string) *Config {
	return &Config{Providers: []Provider{{
		ID: "p", BaseURL: "http://x", Format: FormatOpenAI, Tier: TierFree, Keys: keys,
	}}}
}

// TestResolveKeys_VaultHit vault 命中 → 明文回填，占位符消失。
func TestResolveKeys_VaultHit(t *testing.T) {
	cfg := cfgWith("vault:freellm/provider/agnes/a")
	rep := cfg.ResolveKeys(context.Background(), func(_ context.Context, p string) (string, error) {
		if p != "freellm/provider/agnes/a" {
			t.Errorf("resolver got path %q", p)
		}
		return "sk-real", nil
	})
	if rep.Resolved != 1 || rep.Unresolved != 0 {
		t.Fatalf("report = %+v, want 1 resolved / 0 unresolved", rep)
	}
	if got := cfg.Providers[0].Keys[0]; got != "sk-real" {
		t.Errorf("key = %q, want sk-real", got)
	}
	if IsPlaceholder(cfg.Providers[0].Keys[0]) {
		t.Error("解析后的真值不该还是占位符")
	}
}

// TestResolveKeys_EmptyValueIsUnresolved vault 成功但返回空值 → 必须算未解析。
//
// 这是最容易漏的一种：resolver 返回 ("", nil)，若只判 err != nil 就会把空串
// 写回配置，而空串没有前缀、IsPlaceholder 认不出来 → 空 key 进池 →
// `Authorization: Bearer ` → 401，且池非空连 503 都不给。
func TestResolveKeys_EmptyValueIsUnresolved(t *testing.T) {
	for _, empty := range []string{"", "   ", "\r\n"} {
		cfg := cfgWith("vault:x")
		rep := cfg.ResolveKeys(context.Background(), func(context.Context, string) (string, error) {
			return empty, nil // 成功，但值是空白
		})
		if rep.Unresolved != 1 {
			t.Errorf("空值 %q：Unresolved = %d, want 1", empty, rep.Unresolved)
		}
		if !IsPlaceholder(cfg.Providers[0].Keys[0]) {
			t.Errorf("空值 %q：占位符未被保留，会被当成真实 key 进池", empty)
		}
	}
}

// TestResolveKeys_PathMissing path 取不到 → 保留占位符 + 明细可诊断。
func TestResolveKeys_PathMissing(t *testing.T) {
	cfg := cfgWith("vault:no/such/path")
	rep := cfg.ResolveKeys(context.Background(), func(context.Context, string) (string, error) {
		return "", errors.New("exit status 1: not found")
	})
	if rep.Unresolved != 1 {
		t.Fatalf("Unresolved = %d, want 1", rep.Unresolved)
	}
	if len(rep.Details) != 1 || !strings.Contains(rep.Details[0], "no/such/path") {
		t.Errorf("Details = %v, 应含失败的 path", rep.Details)
	}
	if !IsPlaceholder(cfg.Providers[0].Keys[0]) {
		t.Error("失败后应保留占位符（配置写错要看得见，但不能 fatal）")
	}
}

// TestResolveKeys_MixedPrefixes env: 与 vault: 混用，各自解析。
func TestResolveKeys_MixedPrefixes(t *testing.T) {
	t.Setenv("FTA_TEST_KEY", "sk-from-env")
	cfg := cfgWith("env:FTA_TEST_KEY", "vault:some/path", "sk-literal")
	rep := cfg.ResolveKeys(context.Background(), func(context.Context, string) (string, error) {
		return "sk-from-vault", nil
	})
	if rep.Resolved != 3 || rep.Unresolved != 0 {
		t.Fatalf("report = %+v, want 3/0", rep)
	}
	want := []string{"sk-from-env", "sk-from-vault", "sk-literal"}
	for i, w := range want {
		if got := cfg.Providers[0].Keys[i]; got != w {
			t.Errorf("Keys[%d] = %q, want %q", i, got, w)
		}
	}
}

// TestResolveKeys_EnvUnset env: 指向未设置的变量 → 未解析。
func TestResolveKeys_EnvUnset(t *testing.T) {
	cfg := cfgWith("env:FTA_DEFINITELY_NOT_SET_12345")
	rep := cfg.ResolveKeys(context.Background(), func(context.Context, string) (string, error) {
		t.Fatal("env: 不该触发 vault 调用")
		return "", nil
	})
	if rep.Unresolved != 1 {
		t.Errorf("Unresolved = %d, want 1", rep.Unresolved)
	}
}

// TestResolveKeys_Dedup 同一 vault path 在多处出现只解析一次。
//
// 45 个唯一 path 就要 45 次进程外 exec，重复 path 若不去重会成倍放大启动惩罚。
func TestResolveKeys_Dedup(t *testing.T) {
	cfg := &Config{Providers: []Provider{
		{ID: "a", BaseURL: "http://x", Format: FormatOpenAI, Tier: TierFree,
			Keys: []string{"vault:same/path", "vault:same/path"}},
		{ID: "b", BaseURL: "http://y", Format: FormatAnthropic, Tier: TierFree,
			Keys: []string{"vault:same/path", "vault:other"}},
	}}
	calls := 0
	rep := cfg.ResolveKeys(context.Background(), func(_ context.Context, path string) (string, error) {
		calls++
		return "sk-" + path, nil
	})
	if calls != 2 {
		t.Errorf("resolver 调用 %d 次，want 2（same/path 只该解析一次）", calls)
	}
	if rep.Resolved != 4 {
		t.Errorf("Resolved = %d, want 4（每个条目都该回填）", rep.Resolved)
	}
	for _, p := range cfg.Providers {
		for _, k := range p.Keys {
			if IsPlaceholder(k) {
				t.Errorf("provider %s 仍有未解析占位符 %q", p.ID, k)
			}
		}
	}
}

// TestResolveKeys_NegativeCacheNotRepeated 同一坏 path 不重复 exec。
func TestResolveKeys_NegativeCacheNotRepeated(t *testing.T) {
	cfg := cfgWith("vault:bad/path", "vault:bad/path", "vault:bad/path")
	calls := 0
	rep := cfg.ResolveKeys(context.Background(), func(context.Context, string) (string, error) {
		calls++
		return "", errors.New("boom")
	})
	if calls != 1 {
		t.Errorf("resolver 调用 %d 次，want 1（坏 path 应负缓存）", calls)
	}
	if rep.Unresolved != 3 {
		t.Errorf("Unresolved = %d, want 3", rep.Unresolved)
	}
}

// TestResolveKeys_PureEnvDoesNotExecVault 反向断言：纯 env: 配置永不触发 vault exec。
//
// 比"vault 命中"更重要——它保证 CI / 无 vault 环境不会因为测试而真的拉起 vault.exe
// （那会读进线上 key，且每次 50–250ms）。
func TestResolveKeys_PureEnvDoesNotExecVault(t *testing.T) {
	t.Setenv("FTA_PURE", "sk-x")
	cfg := cfgWith("env:FTA_PURE", "sk-literal")
	cfg.ResolveKeys(context.Background(), func(context.Context, string) (string, error) {
		t.Fatal("纯 env/字面量配置不该调用 vault")
		return "", nil
	})
}

// ── defaultVaultGet ──

// TestDefaultVaultGet_BinMissing vault 二进制缺失 → 可识别的哨兵错误。
//
// 与"path 取不到"分开：前者是能力缺失（静默降级），后者是配置错误（必须响）。
func TestDefaultVaultGet_BinMissing(t *testing.T) {
	t.Setenv("VAULT_BIN", "Z:/definitely/not/here/vault.exe")
	_, err := defaultVaultGet(context.Background(), "any/path")
	if !errors.Is(err, ErrVaultUnavailable) {
		t.Fatalf("err = %v, want ErrVaultUnavailable", err)
	}
}

// TestResolveKeys_VaultUnavailableDegrades vault 不可用时整体降级为"未解析"，不 panic、不 fatal。
func TestResolveKeys_VaultUnavailableDegrades(t *testing.T) {
	t.Setenv("VAULT_BIN", "Z:/definitely/not/here/vault.exe")
	cfg := cfgWith("vault:a", "vault:b")
	rep := cfg.ResolveKeys(context.Background(), nil) // 用默认实现（会走 os.Stat 失败）
	if rep.Unresolved != 2 {
		t.Errorf("Unresolved = %d, want 2", rep.Unresolved)
	}
	for i, d := range rep.Details {
		if !strings.Contains(d, "vault") {
			t.Errorf("Details[%d] = %q, 应指出 vault 不可用", i, d)
		}
	}
}

// TestResolveKeys_NilContext ctx 为 nil 时退化为 Background，不 panic。
func TestResolveKeys_NilContext(t *testing.T) {
	cfg := cfgWith("sk-literal")
	//nolint:staticcheck // 显式验证 nil ctx 的退化路径
	rep := cfg.ResolveKeys(nil, nil)
	if rep.Resolved != 1 {
		t.Errorf("Resolved = %d, want 1", rep.Resolved)
	}
}
