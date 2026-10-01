package config

import (
	"testing"

	"moretoken/internal/admission"
)

func row(model, base string, verdict string, reasoning, coding [2]int) admission.ModelAdmission {
	dims := map[string]admission.DimScore{}
	if reasoning[1] > 0 {
		dims[admission.DimReasoning] = admission.DimScore{Passed: reasoning[0], Total: reasoning[1]}
	}
	if coding[1] > 0 {
		dims[admission.DimCoding] = admission.DimScore{Passed: coding[0], Total: coding[1]}
	}
	return admission.ModelAdmission{
		Model: model, Base: base, AnthropicOK: true, BatteryVerdict: verdict, Dims: dims,
	}
}

// TestApplyAdmission_AddsPassingModelToMatchingProvider：实测合格的**新**模型进对应的池。
//
// 这是"直接加入到相应的池中"的正例 —— 也是运行时 filter 做不到的那一半
// （filter 只能在已声明的模型里筛，新模型根本进不了候选）。
func TestApplyAdmission_AddsPassingModelToMatchingProvider(t *testing.T) {
	cfg := &Config{Providers: []Provider{
		{ID: "xkiro", BaseURL: "https://api.xkiro.com/v1", Keys: []string{"k"},
			Models: []Model{{ID: "existing", Kinds: []string{"general"}}}},
		{ID: "other", BaseURL: "https://other.example.com/v1", Keys: []string{"k"}},
	}}
	rows := []admission.ModelAdmission{
		row("qwen/qwen3-coder-plus:free", "https://api.xkiro.com", "PASS", [2]int{4, 5}, [2]int{5, 5}),
	}

	res := ApplyAdmission(cfg, rows, 0.8)
	added := res.Added

	if added != 1 {
		t.Fatalf("应新增 1 个模型，got %d", added)
	}
	p := cfg.Providers[0]
	if len(p.Models) != 2 || p.Models[1].ID != "qwen/qwen3-coder-plus:free" {
		t.Fatalf("新模型应被追加到 xkiro 末尾，got %+v", p.Models)
	}
	// base 归一化：表里是根地址、config 里是 /v1 结尾 —— 必须能对上是同一个 provider
	if len(cfg.Providers[1].Models) != 0 {
		t.Error("base 不匹配的 provider 不该被加模型")
	}
	// kinds 只来自实测证据：reasoning 4/5 在 0.8 线下通过、coding 5/5 通过
	got := p.Models[1].Kinds
	if len(got) != 2 || got[0] != admission.DimReasoning || got[1] != admission.DimCoding {
		t.Errorf("kinds 应为 [reasoning coding]（固定顺序、只来自实测），got %v", got)
	}
}

// TestApplyAdmission_FillsMissingKindsButNeverDeletes：**只补不删**。
//
// 手写 kinds 是人的显式偏好，"实测"没有改写它的权力 —— 剔除不合格是运行时 filter 的职责。
func TestApplyAdmission_FillsMissingKindsButNeverDeletes(t *testing.T) {
	cfg := &Config{Providers: []Provider{{
		ID: "p", BaseURL: "https://api.xkiro.com", Keys: []string{"k"},
		Models: []Model{{ID: "m", Kinds: []string{"coding"}}}, // 人工写的 coding
	}}}
	// 实测：coding 不通过（3/5 < 80%）、reasoning 通过
	rows := []admission.ModelAdmission{
		row("m", "https://api.xkiro.com", "UNCERTAIN", [2]int{5, 5}, [2]int{3, 5}),
	}

	res := ApplyAdmission(cfg, rows, 0.8)
	added, upgraded := res.Added, res.Upgraded

	if added != 0 {
		t.Errorf("模型已存在，不该新增，got %d", added)
	}
	if upgraded != 1 {
		t.Errorf("应补齐 1 个 kind（reasoning），got %d", upgraded)
	}
	kinds := cfg.Providers[0].Models[0].Kinds
	if !hasKind(kinds, "coding") {
		t.Error("**人工写的 coding 一个都不许删** —— 哪怕实测没过（剔除由运行时 filter 负责）")
	}
	if !hasKind(kinds, admission.DimReasoning) {
		t.Error("实测通过的 reasoning 应被补上")
	}
}

// TestApplyAdmission_NoEvidenceNoPool：没有任何维度实测通过 ⇒ 不入池。
//
// 与 MergeAuto「kinds 强制 general、不采信文件自称」同一条底线：
// **能力标签只来自实测证据**。
func TestApplyAdmission_NoEvidenceNoPool(t *testing.T) {
	cfg := &Config{Providers: []Provider{{
		ID: "p", BaseURL: "https://api.xkiro.com", Models: []Model{{ID: "keep"}},
	}}}
	rows := []admission.ModelAdmission{
		row("weak", "https://api.xkiro.com", "FAIL", [2]int{1, 5}, [2]int{0, 5}),
		// 覆盖不全（INCOMPLETE）⇒ 整行不作数，即便数字看着满分
		func() admission.ModelAdmission {
			r := row("incomplete", "https://api.xkiro.com", "INCOMPLETE", [2]int{5, 5}, [2]int{5, 5})
			return r
		}(),
	}

	res := ApplyAdmission(cfg, rows, 0.8)
	added, upgraded := res.Added, res.Upgraded

	if added != 0 || upgraded != 0 {
		t.Fatalf("无实测证据不得入池也不得补标签，got added=%d upgraded=%d", added, upgraded)
	}
	if len(cfg.Providers[0].Models) != 1 {
		t.Errorf("池里应仍只有手写的 1 个模型，got %+v", cfg.Providers[0].Models)
	}
}

// TestApplyAdmission_AppendsNeverPrepends：**只追加、绝不前插**。
//
// 为什么这条是硬规矩（MergeAuto 原文）：config 里手写声明的顺序就是偏好顺序，
// 否则"一次自动纳新就可能把主力模型换掉 —— 那是『自动的』不该有的权力"。
func TestApplyAdmission_AppendsNeverPrepends(t *testing.T) {
	cfg := &Config{Providers: []Provider{{
		ID: "p", BaseURL: "https://api.xkiro.com",
		Models: []Model{{ID: "hand-1"}, {ID: "hand-2"}},
	}}}
	rows := []admission.ModelAdmission{
		row("auto-x", "https://api.xkiro.com", "PASS", [2]int{5, 5}, [2]int{5, 5}),
	}

	_ = ApplyAdmission(cfg, rows, 0.8)

	ms := cfg.Providers[0].Models
	if len(ms) != 3 || ms[0].ID != "hand-1" || ms[1].ID != "hand-2" || ms[2].ID != "auto-x" {
		t.Fatalf("自动入池的模型必须排在手写模型**之后**，got %v", []string{ms[0].ID, ms[1].ID, ms[2].ID})
	}
}

// TestApplyAdmission_OneBaseAddsToEveryMatchingProvider：同一上游的 anthropic 与 openai
// 两个入口**都该加** —— 该模型在两条协议路径上都可用。
func TestApplyAdmission_OneBaseAddsToEveryMatchingProvider(t *testing.T) {
	cfg := &Config{Providers: []Provider{
		{ID: "x-anthropic", BaseURL: "https://api.xkiro.com", Format: FormatAnthropic},
		{ID: "x-openai", BaseURL: "https://api.xkiro.com/v1", Format: FormatOpenAI},
	}}
	rows := []admission.ModelAdmission{
		row("m", "https://api.xkiro.com", "PASS", [2]int{5, 5}, [2]int{5, 5}),
	}

	res := ApplyAdmission(cfg, rows, 0.8)
	added := res.Added

	if added != 2 {
		t.Fatalf("两个协议入口都该加，got added=%d", added)
	}
	for _, p := range cfg.Providers {
		if len(p.Models) != 1 || p.Models[0].ID != "m" {
			t.Errorf("provider %s 应含模型 m，got %+v", p.ID, p.Models)
		}
	}
}

// TestApplyAdmission_UnmatchedIsReportedNotSilent：**有证据、没去处**的模型必须被报出来。
//
// ⚠️ 为什么这条必须有（2026-10-01）：函数自己的注释早就写着 normBase 不归一化会
// "静默失败（模型进不了池、没有任何报错）"，**却没装任何仪表** —— 调用方只看到 `新增 0`，
// 无从区分"表里没有合格模型"和"全都没匹配上 base_url"。后者会让人一头扎向配额排查。
// 这与本项目反复吃亏的"静默归零"同型，故 Unmatched 必须非空且点名到模型。
func TestApplyAdmission_UnmatchedIsReportedNotSilent(t *testing.T) {
	cfg := &Config{Providers: []Provider{
		{ID: "xkiro", BaseURL: "https://api.xkiro.com/v1"},
	}}
	rows := []admission.ModelAdmission{
		// 这个上游压根没配 provider ⇒ 有证据也无处安放
		row("orphan-model", "https://api.nowhere.example", "PASS", [2]int{5, 5}, [2]int{5, 5}),
		// 对照组：能匹配上的不该被列进来
		row("homed-model", "https://api.xkiro.com", "PASS", [2]int{5, 5}, [2]int{5, 5}),
	}

	res := ApplyAdmission(cfg, rows, 0.8)

	if len(res.Unmatched) != 1 || res.Unmatched[0] != "orphan-model" {
		t.Fatalf("无 provider 可接的模型必须被点名报出，got Unmatched=%v", res.Unmatched)
	}
	if res.Added != 1 {
		t.Errorf("能匹配上的那个仍应正常入池，got Added=%d", res.Added)
	}
	// 反证：正常情形下 Unmatched 必须为空（否则调用方会把告警当噪音忽略）
	clean := ApplyAdmission(&Config{Providers: []Provider{
		{ID: "xkiro", BaseURL: "https://api.xkiro.com/v1"},
	}}, []admission.ModelAdmission{
		row("homed-model", "https://api.xkiro.com", "PASS", [2]int{5, 5}, [2]int{5, 5}),
	}, 0.8)
	if len(clean.Unmatched) != 0 {
		t.Errorf("全部匹配上时 Unmatched 必须为空（否则告警会变噪音），got %v", clean.Unmatched)
	}
}

// TestNormBase：归一化必须吃掉"尾斜杠"与"尾 /v1"的书写差异。
//
// ⚠️ 不归一化就**永远匹配不上**，且失败是静默的（模型进不了池、没有任何报错）——
// 与"跨系统比路径"那类血账同型。
func TestNormBase(t *testing.T) {
	want := "https://api.xkiro.com"
	for _, in := range []string{
		"https://api.xkiro.com",
		"https://api.xkiro.com/",
		"https://api.xkiro.com/v1",
		"https://api.xkiro.com/v1/",
		"  https://api.xkiro.com/v1  ",
	} {
		if got := normBase(in); got != want {
			t.Errorf("normBase(%q) = %q，want %q", in, got, want)
		}
	}
}
