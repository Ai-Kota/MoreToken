package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"moretoken/internal/config"
	"moretoken/internal/router"
)

// /v1/models 必须声明**上下文窗口**——字段名是客户端认的那两个。
//
// 从 Claude Code 二进制里挖到的 Anthropic API 文档：
//
//	Each model object has `id`, `display_name`, `created_at`, and — since Mar 2026 —
//	**`max_input_tokens`** (the context window), `max_tokens` (the output cap),
//	and `capabilities`. **There is no `context_window` field.**
//
// 为什么这件事重要：声明窗口 = 客户端**在超限前**压缩 ⇒ **根本不会产生超限请求**。
// 这是"无感"的正解；而翻译超限错误只是撞墙后的兜底。
// 2026-09-22 实证：客户端堆到 994166 token，而 agnes 上限 524288。

func modelsFixture(t *testing.T, format config.Format) []map[string]any {
	t.Helper()
	cfg := &config.Config{Providers: []config.Provider{
		{ID: "agnes-anthropic", BaseURL: "https://a.invalid", Format: config.FormatAnthropic,
			Tier: config.TierFree, Keys: []string{"k"},
			Models: []config.Model{{
				ID: "agnes-2.5-flash", Name: "Agnes 2.5 Flash", Kinds: []string{"reasoning"},
				ContextLength: 524288, MaxOutputTokens: 8192, // 实测值（从它自己的超限报错里读出）
			}}},
		{ID: "xkiro-anthropic", BaseURL: "https://x.invalid", Format: config.FormatAnthropic,
			Tier: config.TierFree, Keys: []string{"k"},
			Models: []config.Model{{
				ID: "minimax/minimax-m3:free", Name: "MiniMax M3", Kinds: []string{"reasoning"},
				ContextLength: 1000000, MaxOutputTokens: 65536, // 上游目录给的
			}}},
	}}
	srv := NewServer(router.New(cfg, router.WithClient(http.DefaultClient)), cfg, NewDecisionLog(4))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/v1/models?format=" + string(format))
	if err != nil {
		t.Fatalf("get models: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Data
}

// TestModels_DeclaresContextWindow —— 具体模型要带上窗口与输出上限。
func TestModels_DeclaresContextWindow(t *testing.T) {
	byID := map[string]map[string]any{}
	for _, m := range modelsFixture(t, config.FormatAnthropic) {
		byID[m["id"].(string)] = m
	}

	agnes, ok := byID["agnes-2.5-flash"]
	if !ok {
		t.Fatal("具体模型 agnes-2.5-flash 应在列表里")
	}
	if got := agnes["max_input_tokens"]; got != float64(524288) {
		t.Errorf("agnes max_input_tokens = %v, want 524288（客户端靠它决定何时压缩）", got)
	}
	if got := agnes["max_tokens"]; got != float64(8192) {
		t.Errorf("agnes max_tokens = %v, want 8192", got)
	}
	if got := agnes["display_name"]; got != "Agnes 2.5 Flash" {
		t.Errorf("display_name = %v，客户端认这个字段名", got)
	}
}

// TestModels_VirtualTakesMinWindow —— 虚拟模型的窗口取候选里**最小的那个**。
//
// 为什么是 min：客户端拿它决定何时压缩。取 min 是"我能保证的下限"；
// 取首选或取 max 会把"我可能被换到更小的模型"藏起来，客户端压得不够早，
// 于是又撞回那个刚花大力气翻译的超限错误。
//
// 【反证】把 minWindow 改成取 max ⇒ agnes(524288) 被 minimax(1000000) 盖过 ⇒ 本条变红。
func TestModels_VirtualTakesMinWindow(t *testing.T) {
	byID := map[string]map[string]any{}
	for _, m := range modelsFixture(t, config.FormatAnthropic) {
		byID[m["id"].(string)] = m
	}

	va, ok := byID["auto:reasoning"]
	if !ok {
		t.Fatal("虚拟模型 auto:reasoning 应在列表里（客户端用的就是它）")
	}
	if got := va["max_input_tokens"]; got != float64(524288) {
		t.Errorf("auto:reasoning 的窗口 = %v, want 524288（两个候选 524288 / 1000000 取 min）", got)
	}
	if got := va["max_tokens"]; got != float64(8192) {
		t.Errorf("auto:reasoning 的输出上限 = %v, want 8192（取 min）", got)
	}
	if va["virtual"] != true {
		t.Error("虚拟模型仍要标 virtual")
	}
}

// TestModels_UnknownWindowStaysUnknown —— 不知道就**不填**，不猜一个数出来。
//
// 猜错的两种代价不对称：猜大了客户端压得太晚（撞墙）；猜小了它压得过早（白费压缩）。
// 都不如让客户端退回它自己的默认假设——至少那是它自己的模型知识。
func TestModels_UnknownWindowStaysUnknown(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{ID: "p", BaseURL: "https://a.invalid", Format: config.FormatAnthropic,
			Tier: config.TierFree, Keys: []string{"k"},
			Models: []config.Model{{ID: "mystery", Kinds: []string{"reasoning"}}}}, // 无窗口信息
	}}
	srv := NewServer(router.New(cfg, router.WithClient(http.DefaultClient)), cfg, NewDecisionLog(4))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/models?format=anthropic")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)

	for _, m := range out.Data {
		if _, present := m["max_input_tokens"]; present {
			t.Errorf("%v 的窗口未知，不该凭空声明一个数", m["id"])
		}
	}
}
