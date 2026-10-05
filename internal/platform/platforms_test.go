package platform

import "testing"

// TestDiff_ThreeBuckets —— 三向 diff 的边界：注册表 × vault × config，每个组合落对桶。
//
// 判据（T-037）：已接线 > 有 key 未接线 > 已知但无 key，三桶**互斥**且覆盖全部条目。
func TestDiff_ThreeBuckets(t *testing.T) {
	reg := &Registry{Platforms: []Entry{
		{Platform: "wired", Name: "W", BaseURL: "https://api.wired.example/v1"},       // 已接线
		{Platform: "havekeys", Name: "H", BaseURL: "https://api.havekeys.example/v1"}, // 有 key 未接线
		{Platform: "missing", Name: "M", BaseURL: "https://api.missing.example/v1"},   // 已知但无 key
	}}
	vault := map[string]bool{"havekeys": true, "wired": true} // wired 也有 key —— 仍应落"已接线"
	wiredHosts := WiredHosts([]string{"https://api.wired.example", "https://api.wired.example/v1"})

	rep := reg.Diff(vault, wiredHosts)

	if len(rep.Wired) != 1 || rep.Wired[0].Platform != "wired" {
		t.Errorf("已接线 = %v, want [wired]（且「两处都算已接线」不该重复计入另一桶）", rep.Wired)
	}
	if len(rep.HaveKeys) != 1 || rep.HaveKeys[0].Platform != "havekeys" {
		t.Errorf("有 key 未接线 = %v, want [havekeys]", rep.HaveKeys)
	}
	if len(rep.Missing) != 1 || rep.Missing[0].Platform != "missing" {
		t.Errorf("已知但无 key = %v, want [missing]（这条正是用户要去注册的）", rep.Missing)
	}
	if n := len(rep.Wired) + len(rep.HaveKeys) + len(rep.Missing); n != len(reg.Platforms) {
		t.Errorf("三桶合计 %d != 注册表 %d —— 有条目掉桶或重复", n, len(reg.Platforms))
	}
}

// TestWiredHosts_MatchesSameUpstreamDifferentPaths —— 按**主机名**归一。
//
// 判据：config 里 agnes 与 agnes-anthropic 是同一上游的两条（协议不同），
// 注册表只记一条 ⇒ 必须能被认成"已接线"，否则会把 agnes 报成"缺平台"。
func TestWiredHosts_MatchesSameUpstreamDifferentPaths(t *testing.T) {
	wired := WiredHosts([]string{
		"https://apihub.agnes-ai.com/v1", // agnes（openai）
		"https://apihub.agnes-ai.com",    // agnes-anthropic（anthropic）
	})
	if !wired[HostOf("https://apihub.agnes-ai.com/v1")] {
		t.Error("同一上游的两种协议写法应归一到同一主机名")
	}
	if HostOf("https://API.Example.COM/x") != "api.example.com" {
		t.Error("主机名应大小写归一")
	}
	if HostOf("not a url") != "" {
		t.Error("解析不出主机名应返回空串（不得猜）")
	}
}

// TestParseVaultList —— 从 `vault list --prefix` 输出抠平台名。
//
// 【反证】把前缀判据去掉 ⇒ 末行那条 `moretoken/tokens/cc` 会被误当成平台。
func TestParseVaultList(t *testing.T) {
	out := `共 4 条
  freellm/provider/unorouter/unorouter      [] 2026-10-05T00:00:00+08:00
  freellm/provider/agnes/agnes-aimly        [] 2026-10-05T00:00:00+08:00
  freellm/provider/agnes/agnes-meimei       [] 2026-10-05T00:00:00+08:00
  moretoken/tokens/cc                       [] 2026-10-05T00:00:00+08:00
`
	got := ParseVaultList(out)
	if !got["unorouter"] || !got["agnes"] {
		t.Errorf("应认出 unorouter / agnes，实际 %v", got)
	}
	for k := range got {
		if k == "moretoken" || k == "tokens" {
			t.Errorf("非 freellm/provider/ 前缀的条目被误认成平台：%v", got)
		}
	}
	if len(got) != 2 {
		t.Errorf("平台数 = %d, want 2（agnes 两条应归一）", len(got))
	}
}

// TestParse_BadJSON —— 坏注册表必须**响亮**报错，不静默当空表。
//
// 静默当空表 = 把"注册表坏了"读成"一个平台都没有" ⇒ 三桶全空 ⇒ 假绿。
func TestParse_BadJSON(t *testing.T) {
	if _, err := Parse([]byte(`{"platforms": not json`)); err == nil {
		t.Error("坏 JSON 应报错，不得静默返回空注册表")
	}
}
