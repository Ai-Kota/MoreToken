package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"moretoken/internal/config"
	"moretoken/internal/provider"
)

// 模型维度轮换的契约（T-018）。
//
// 起因：准备做"从上游目录自动纳新"时发现——**纳进来的模型用不上**。
// `tryCandidate` 在 key 循环**之前**解析一次模型（`ResolveModel` 取第一个匹配 kind 的），
// 然后只用它轮换 3 把 key；同一个 provider 的其它模型**永远不会被尝试**。
//
// 后果：池子的维度是 (provider × key)，**模型维度是死的**。
// 于是"往 config 里加 24 个免费模型"这件事的净收益是 **0**——
// 除非 provider 被整个换掉，那些模型一行服务流量都不会承担。
//
// 这条契约要求：模型与 key 一样是**可轮换**的资源——
// 首选模型失败（可重试类）时，同 provider 的次选模型应当顶上来。

// TestRoute_RotatesModelsWithinProvider —— 首选模型挂 → 次选模型顶上（同一个 provider 内）。
//
// 【当前为红】——这正是 T-018 要修的东西。红了才说明"纳新"不是装饰。
func TestRoute_RotatesModelsWithinProvider(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		m := ModelOf(body)
		mu.Lock()
		calls[m]++
		mu.Unlock()
		if m == "model-primary" {
			w.WriteHeader(http.StatusBadGateway) // 首选模型的上游挂了
			return
		}
		w.Write([]byte(`{"content":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "p", BaseURL: srv.URL, Format: "anthropic", Tier: "free",
			Keys: []string{"k"},
			Models: []config.Model{
				{ID: "model-primary", Kinds: []string{"general"}},
				{ID: "model-backup", Kinds: []string{"general"}}, // 同一个 provider 的次选
			}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic,
		Body:   []byte(`{"model":"auto:general","messages":[]}`),
		Kind:   provider.NonStreaming, VirtualModel: true, WantKind: "general",
	})
	if err != nil {
		t.Fatalf("首选模型挂了，同 provider 还有次选，不该整体失败：%v", err)
	}
	if res.ProviderID != "p" {
		t.Fatalf("provider = %q, want p（不该换 provider，先换模型）", res.ProviderID)
	}
	if res.Model != "model-backup" {
		t.Errorf("落地模型 = %q, want model-backup", res.Model)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls["model-backup"] == 0 {
		t.Errorf("次选模型一次都没被调用 —— 模型维度是死的，纳新等于装饰（调用分布 %v）", calls)
	}
}

// TestRoute_ModelDenied403_RotatesWithoutBurningKeys —— 403 按**模型级**处理。
//
// 实测判据（2026-09-20，agnes 与 xkiro 两个上游一致）：
//
//	好 key + 好模型 → 200 ／ 好 key + 无权模型 → 403 ／ 垃圾 key → 401
//
// 403 常常是"这个账号对这个模型没权限"，**凭据是好的**。
// 二者曾同归 FailAuth ⇒ 路由对 403 也 RecordDead（长禁该 key 1 小时）⇒
// 一个无权模型进 config 就会把该 provider 的 key 逐个禁光、整层下线一小时。
// 期望行为：换同 provider 的下一个模型，**一把 key 都不受损**。
//
// 【反证】把 provider.Invoke 里 403 那条分支去掉（重归 FailAuth）⇒ 本条的
// cooling 断言变红，且请求会整体失败。
func TestRoute_ModelDenied403_RotatesWithoutBurningKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		if ModelOf(body) == "denied" {
			http.Error(w, `{"error":"no permission for this model"}`, http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"content":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{{
		ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
		Keys: []string{"k1", "k2"},
		Models: []config.Model{
			{ID: "denied", Kinds: []string{"general"}},
			{ID: "alive", Kinds: []string{"general"}},
		},
	}}}
	r := New(cfg, WithClient(http.DefaultClient))

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"auto:general","messages":[]}`),
		Kind: provider.NonStreaming, VirtualModel: true, WantKind: "general",
	})
	if err != nil {
		t.Fatalf("403 是模型级事实，不该让请求失败：%v", err)
	}
	if res.Model != "alive" {
		t.Errorf("落地模型 = %q, want alive", res.Model)
	}
	if h := r.Health(); h[0].Cooling != 0 {
		t.Errorf("Cooling = %d, want 0 —— 403 不该烧 key（凭据明明是好的）", h[0].Cooling)
	}
}

// TestRoute_ModelGone404_RotatesAndDoesNotLeakToClient —— "上游不再提供某模型"的原生形态。
//
// 上游回 404 时，**不许把 404 甩给调用方**（今天 15:30 的 xkiro 两条 404 透传就是这个形态），
// 也不许整层 fallback 到别的 provider（那会白白绕开一个还能用的服务商）：
// 先在同一个 provider 里换模型。
func TestRoute_ModelGone404_RotatesAndDoesNotLeakToClient(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		m := ModelOf(body)
		mu.Lock()
		calls[m]++
		mu.Unlock()
		if m == "gone" {
			http.Error(w, `{"error":{"message":"Model \"gone\" does not exist"}}`, http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"content":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "p", BaseURL: srv.URL, Format: "anthropic", Tier: "free",
			Keys: []string{"k"},
			Models: []config.Model{
				{ID: "gone", Kinds: []string{"general"}},
				{ID: "alive", Kinds: []string{"general"}},
			}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic,
		Body:   []byte(`{"model":"auto:general","messages":[]}`),
		Kind:   provider.NonStreaming, VirtualModel: true, WantKind: "general",
	})
	if err != nil {
		t.Fatalf("404 是模型级事实，不该让整个请求失败：%v", err)
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d，404 不该透传给调用方", res.StatusCode)
	}
	if res.Model != "alive" {
		t.Errorf("落地模型 = %q, want alive", res.Model)
	}
}
