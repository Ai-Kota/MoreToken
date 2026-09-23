package config

import (
	"strings"
	"testing"
)

// 断言「需求面 ⊆ 供给面」的契约（T-018）。
//
// 血账：2026-09-20 的事故是"某个 (format,kind) 的能力面塌成空集"，
// 而系统里没有任何一处记录过"哪些 kind 是必须覆盖的"——只能等用户在 15:31 撞上 503。
// 这个检查把那条隐含承诺变成**可穷举、可在部署前跑**的断言。

func prov(id string, f Format, tier Tier, kinds ...string) Provider {
	m := Model{ID: id + "-m"}
	m.Kinds = kinds
	return Provider{ID: id, Format: f, Tier: tier, Keys: []string{"k"}, Models: []Model{m}}
}

// demandKinds 把 DemandKinds 铺成一组 provider 用的参数。
func allDemandKinds() []string { return append([]string{}, DemandKinds...) }

// TestCheckCoverage_FullCoverageIsClean —— 每个需求 kind 都有多个免费 provider ⇒ 零告警。
func TestCheckCoverage_FullCoverageIsClean(t *testing.T) {
	cfg := &Config{Providers: []Provider{
		prov("free-a", FormatAnthropic, TierFree, allDemandKinds()...),
		prov("free-b", FormatAnthropic, TierFree, allDemandKinds()...),
	}}

	fatal, warn := CheckCoverage(cfg)

	if len(fatal) != 0 || len(warn) != 0 {
		t.Errorf("全覆盖不该报任何问题，实际 fatal=%v warn=%v", fatal, warn)
	}
}

// TestCheckCoverage_UncoveredKindIsFatal —— 某 kind 无人覆盖 ⇒ 致命（这类请求必然 503）。
//
// 【反证】把 CheckCoverage 里 `case g.Fatal(): fatal = append(...)` 拿掉 ⇒ 本条变红。
func TestCheckCoverage_UncoveredKindIsFatal(t *testing.T) {
	// 只提供 reasoning；coding/general/fast 无人覆盖
	cfg := &Config{Providers: []Provider{
		prov("free-a", FormatAnthropic, TierFree, "reasoning"),
	}}

	fatal, _ := CheckCoverage(cfg)

	missing := map[string]bool{}
	for _, g := range fatal {
		missing[g.Kind] = true
	}
	for _, k := range []string{"coding", "general", "fast"} {
		if !missing[k] {
			t.Errorf("kind %q 无人覆盖，应判致命缺口；实际 fatal=%v", k, fatal)
		}
	}
	if missing["reasoning"] {
		t.Error("reasoning 有人覆盖，不该出现在缺口里")
	}
}

// TestCheckCoverage_SingleProviderIsWarn —— 只有 1 个 provider 覆盖 ⇒ 告警（无冗余），不阻塞。
func TestCheckCoverage_SingleProviderIsWarn(t *testing.T) {
	cfg := &Config{Providers: []Provider{
		prov("free-a", FormatAnthropic, TierFree, allDemandKinds()...),
	}}

	fatal, warn := CheckCoverage(cfg)

	if len(fatal) != 0 {
		t.Errorf("有覆盖就不该致命，实际 fatal=%v", fatal)
	}
	if len(warn) != len(DemandKinds) {
		t.Errorf("每个 kind 都只有 1 个 provider ⇒ 应报 %d 处告警，实际 %d：%v",
			len(DemandKinds), len(warn), warn)
	}
}

// TestCheckCoverage_FreeLayerGapIsWarn —— 只有付费层覆盖 ⇒ 主力层缺口，告警。
//
// 定性（用户）：免费是主力，付费只是暂时应急。所以"只有兜底覆盖"这件事必须显式可见，
// 但**不阻塞**——否则会把"兜底能顶"误判成"服务不可用"。
func TestCheckCoverage_FreeLayerGapIsWarn(t *testing.T) {
	cfg := &Config{Providers: []Provider{
		prov("free-a", FormatAnthropic, TierFree, "reasoning"),
		prov("free-b", FormatAnthropic, TierFree, "reasoning"),
		prov("paid", FormatAnthropic, TierPaid, "coding", "general", "fast"),
	}}

	fatal, warn := CheckCoverage(cfg)

	if len(fatal) != 0 {
		t.Fatalf("有兜底覆盖就不该致命，实际 fatal=%v", fatal)
	}
	found := false
	for _, g := range warn {
		if g.Kind == "coding" && g.Free == 0 {
			found = true
			if !strings.Contains(g.String(), "免费层") {
				t.Errorf("措辞应指出是免费层缺口：%s", g)
			}
		}
	}
	if !found {
		t.Errorf("coding 只有付费层覆盖，应报免费层缺口；实际 warn=%v", warn)
	}
}

// TestCheckCoverage_OnlyChecksFormatsPresentInConfig —— 没配 provider 的格式不重复告警。
//
// 请求一个没配任何 provider 的格式，本来就会明确报错（"no X provider in config"），
// 在这里再报一遍只是噪音。
func TestCheckCoverage_OnlyChecksFormatsPresentInConfig(t *testing.T) {
	cfg := &Config{Providers: []Provider{
		prov("free-a", FormatAnthropic, TierFree, allDemandKinds()...),
		prov("free-b", FormatAnthropic, TierFree, allDemandKinds()...),
	}}

	fatal, warn := CheckCoverage(cfg)

	for _, g := range append(append([]CoverageGap{}, fatal...), warn...) {
		if g.Format == FormatOpenAI {
			t.Errorf("config 里没有 openai provider，不该检查它：%v", g)
		}
	}
}

// TestCheckCoverage_GapFixtureIsFatal —— 走**真实 Load**（不是手搓 struct）跑夹具。
//
// 手搓 Config 只证明"函数逻辑对"；这条证明"磁盘上的配置真的会被判出缺口"——
// 即 `-check` 那把闸在真实路径上有效。
// 配套的真身验证：`bin/moretoken.exe -check -config <夹具>` 必须 exit 1。
func TestCheckCoverage_GapFixtureIsFatal(t *testing.T) {
	cfg, err := Load("testdata/gap-config.json")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	fatal, _ := CheckCoverage(cfg)

	if len(fatal) == 0 {
		t.Fatal("夹具刻意只覆盖 reasoning，应该报出致命缺口——报不出来说明闸是假的")
	}
	for _, g := range fatal {
		if g.Kind == "reasoning" {
			t.Errorf("reasoning 有人覆盖，不该致命：%v", g)
		}
	}
}

// TestCheckCoverage_NilConfigIsSafe —— 别 panic（穷举性质很重要，但别在这里炸）。
func TestCheckCoverage_NilConfigIsSafe(t *testing.T) {
	fatal, warn := CheckCoverage(nil)
	if len(fatal) != 0 || len(warn) != 0 {
		t.Error("nil config 应返回空结果")
	}
}
