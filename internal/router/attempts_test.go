package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"moretoken/internal/config"
	"moretoken/internal/provider"
)

// 失败留痕的契约：**能回答"是谁坏的、怎么坏的"**。
//
// 血账（2026-09-21 实测）：9.5 小时里 7 次突发降级、16 条请求失败，
// 其中一次是同一秒内 7 条一起挂、`tried 3` 意味着三个候选同时失败。
// 三个不同上游同时挂，指向的往往不是上游而是本机（DNS/网络）——
// 但那是【推断】：当时的留痕只有聚合的 `tried 2, limit 4`，
// **一个字都没记谁失败了**，所以那个问题至今无法回答。
//
// 这条测试把"回答得了"钉死。

// TestRoute_AttemptsCarryProviderKindAndStatus —— 每个失败的候选都留下 provider/类别/状态码。
func TestRoute_AttemptsCarryProviderKindAndStatus(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway) // 5xx → 可重试 → 逐个候选都留痕
	}))
	t.Cleanup(bad.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "free-a", BaseURL: bad.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
		{ID: "free-b", BaseURL: bad.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"m","messages":[]}`),
		Kind: provider.NonStreaming,
	})
	if err == nil {
		t.Fatal("全挂应报错")
	}
	if len(res.Attempts) != 2 {
		t.Fatalf("attempts = %d 条，want 2（两个候选各一条）：%+v", len(res.Attempts), res.Attempts)
	}
	for i, a := range res.Attempts {
		if a.Provider != []string{"free-a", "free-b"}[i] {
			t.Errorf("attempts[%d].provider = %q，顺序应与候选顺序一致", i, a.Provider)
		}
		if a.Kind != "upstream" {
			t.Errorf("attempts[%d].kind = %q, want upstream（5xx）", i, a.Kind)
		}
		if a.Status != http.StatusBadGateway {
			t.Errorf("attempts[%d].status = %d, want 502（上游原文要留下）", i, a.Status)
		}
	}
}

// TestRoute_AttemptsDistinguishSkippedFromFailed —— "退避跳过"与"发了但失败"必须分开记。
//
// 混在一起，事后读日志的人会把"自我保护"（还没试）读成"试了没用"——
// 这正是 555e60c 那条教训（退避不是可用性证据）在留痕层面的同一件事。
func TestRoute_AttemptsDistinguishSkippedFromFailed(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(bad.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "in-backoff", BaseURL: bad.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
		{ID: "tried", BaseURL: bad.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))
	r.markBackoff(&r.providers[0]) // 第一个候选落在退避窗口内

	res, _ := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"m","messages":[]}`),
		Kind: provider.NonStreaming,
	})

	if len(res.Attempts) != 2 {
		t.Fatalf("attempts = %+v，want 2 条（一条跳过、一条失败）", res.Attempts)
	}
	if res.Attempts[0].Kind != "skipped_backoff" {
		t.Errorf("attempts[0].kind = %q, want skipped_backoff（退避跳过 ≠ 试过失败）", res.Attempts[0].Kind)
	}
	if res.Attempts[0].Status != 0 {
		t.Errorf("跳过的那条不该有状态码（一次请求都没发），实际 %d", res.Attempts[0].Status)
	}
	if res.Attempts[1].Kind != "upstream" {
		t.Errorf("attempts[1].kind = %q, want upstream", res.Attempts[1].Kind)
	}
}

// TestRoute_AttemptsCarryStreamAndElapsed —— 排障关键的一维：这条请求**是不是流式**。
//
// 血账（2026-09-21）：排查 14 次 `timeout awaiting response headers` 时，最大的障碍
// 就是**不知道那条请求是不是流式**。非流式请求的"响应头"要等整个生成完才发，
// 所以 `ResponseHeaderTimeout(90s)` 对流式≈首字节、对非流式≈**整段生成时长**——
// 两者语义完全不同，而这一个 bit 当时完全没记。
func TestRoute_AttemptsCarryStreamAndElapsed(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(bad.Close)

	mk := func() *Router {
		cfg := &config.Config{Providers: []config.Provider{{
			ID: "p", BaseURL: bad.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}},
		}}}
		return New(cfg, WithClient(http.DefaultClient))
	}
	body := []byte(`{"model":"m","messages":[]}`)

	// 非流式
	res, _ := mk().Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: body, Kind: provider.NonStreaming,
	})
	if len(res.Attempts) != 1 {
		t.Fatalf("attempts = %+v", res.Attempts)
	}
	if res.Attempts[0].Stream {
		t.Error("非流式请求的 attempt 不该标成 stream")
	}
	if res.Attempts[0].BodyBytes != len(body) {
		t.Errorf("body_bytes = %d, want %d（排查「是不是大上下文拖死的」要靠它）",
			res.Attempts[0].BodyBytes, len(body))
	}

	// 流式
	res, _ = mk().Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: body, Kind: provider.Streaming,
	})
	if len(res.Attempts) != 1 {
		t.Fatalf("attempts = %+v", res.Attempts)
	}
	if !res.Attempts[0].Stream {
		t.Error("流式请求的 attempt 必须标成 stream —— 这正是 09-21 那次排查缺的那一维")
	}
}

// TestRoute_AttemptsEmptyOnSuccess —— 成功路径不带失败留痕（别拿噪音淹没日志）。
func TestRoute_AttemptsEmptyOnSuccess(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"content":"ok"}`))
	}))
	t.Cleanup(ok.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "p", BaseURL: ok.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"m","messages":[]}`),
		Kind: provider.NonStreaming,
	})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(res.Attempts) != 0 {
		t.Errorf("成功路径不该有失败留痕，实际 %+v", res.Attempts)
	}
}
