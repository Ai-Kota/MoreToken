package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"moretoken/internal/config"
	"moretoken/internal/provider"
)

// 血账（2026-10-01）：字面模型路由**只问首个候选，它说不认识就终止**。
//
// 那个前提（"换谁都是同一个答案"）**只在"各 provider 的模型集合相同"时成立**。
// 本部署恰恰相反：agnes 与 xkiro 的模型集**不相交**（`qwen/*` 只有 xkiro 有），
// 而 agnes 排在候选首位 ⇒ 它永远先说"没有" ⇒ **xkiro 一次都轮不到，11 把 key 全成摆设**。
//
// 实测：`qwen/qwen3-max:free` 经网关一律 404，而直打 xkiro 上游是 200。
// 修法：字面模型时把候选收窄到**声明了该模型**的 provider（无人声明则保持全量）。

// TestRoute_LiteralModel_ReachesDeclaringProvider 是本次修复的回归闸：
// 排在前面、**不持有**该模型的 provider 回了 404，不得掐断链路；
// 真正声明了它的 provider 必须被走到。
func TestRoute_LiteralModel_ReachesDeclaringProvider(t *testing.T) {
	// 首选 provider：不持有目标模型 —— 按旧行为它一回 404 就终止
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"model not found"}}`))
	}))
	t.Cleanup(first.Close)

	// 次选 provider：声明并服务目标模型
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	t.Cleanup(second.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "first", BaseURL: first.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "only-in-first"}}},
		{ID: "second", BaseURL: second.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "only-in-second"}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"only-in-second","messages":[]}`),
		Kind: provider.NonStreaming, Model: "only-in-second",
	})
	if err != nil {
		t.Fatalf("应路由到声明了该模型的 second，却失败了: %v（attempts=%+v）", err, res.Attempts)
	}
	if res.ProviderID != "second" {
		t.Fatalf("应落在 second（唯一声明者），got %q", res.ProviderID)
	}
	// 收窄的额外收益：压根不给 first 发请求 ⇒ 它不该被记一次失败
	if len(res.Attempts) != 0 {
		t.Errorf("不该对未声明该模型的 provider 发请求，却留下 %d 条尝试留痕: %+v", len(res.Attempts), res.Attempts)
	}
}

// TestRoute_LiteralModel_SoleDeclarerStillShortCircuits 确认修复没有把原有的正确行为改坏：
// 当**只有一家**声明该模型、而它说"不认识"时，仍应立刻上抛 404（而不是继续问无关的 provider）。
func TestRoute_LiteralModel_SoleDeclarerStillShortCircuits(t *testing.T) {
	decl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"model not found"}}`))
	}))
	t.Cleanup(decl.Close)

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("第二个 provider 未声明该模型，不该被请求到")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(other.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "decl", BaseURL: decl.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
		{ID: "other", BaseURL: other.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "n"}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))

	_, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"m","messages":[]}`),
		Kind: provider.NonStreaming, Model: "m",
	})
	if err == nil {
		t.Fatal("唯一声明者说不认识 ⇒ 应上抛 model not found")
	}
}

// TestRoute_LiteralModel_NoDeclarerKeepsFullList 确认"无人声明"时**不收窄**。
//
// 为什么这条重要：聚合商可能服务配置里**没列出**的模型。若把候选收窄成空，
// 本来可用的路径会被堵死 —— 故此时必须回退到既有行为（全量候选 + 首个 not-found 即终止）。
func TestRoute_LiteralModel_NoDeclarerKeepsFullList(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "declared"}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))

	// 请求一个**谁都没声明**的模型：不该被收窄成空候选而提前 503，
	// 而应照旧把请求发出去（上游可能认识它）。
	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"undeclared","messages":[]}`),
		Kind: provider.NonStreaming, Model: "undeclared",
	})
	if err != nil {
		t.Fatalf("无人声明时应回退全量候选并照常发请求，却失败: %v", err)
	}
	if hits == 0 {
		t.Fatal("无人声明时不该把候选收窄成空——请求根本没发出去")
	}
	if res.ProviderID != "p" {
		t.Fatalf("应落在 p，got %q", res.ProviderID)
	}
}

// TestDeclaringCandidates 纯函数：只留声明者、保持原序。
func TestDeclaringCandidates(t *testing.T) {
	mk := func(id string, models ...string) orderedProvider {
		p := config.Provider{ID: id, Format: config.FormatOpenAI, Tier: config.TierFree, Keys: []string{"k"}}
		for _, m := range models {
			p.Models = append(p.Models, config.Model{ID: m})
		}
		return orderedProvider{pv: p}
	}
	cands := []orderedProvider{
		mk("a", "x"),
		mk("b", "x", "y"),
		mk("c", "y"),
		mk("d"),
	}
	got := declaringCandidates(cands, "y")
	if len(got) != 2 || got[0].pv.ID != "b" || got[1].pv.ID != "c" {
		t.Fatalf(`declaringCandidates(_, "y") 应得 [b c]（保持原序），got %v`, ids(got))
	}
	if n := declaringCandidates(cands, "zzz"); len(n) != 0 {
		t.Fatalf("无人声明应得空切片（调用方据此回退全量），got %v", ids(n))
	}
	if n := declaringCandidates(nil, "x"); len(n) != 0 {
		t.Fatalf("空候选应得空，got %v", ids(n))
	}
}

func ids(ops []orderedProvider) []string {
	out := make([]string, 0, len(ops))
	for _, o := range ops {
		out = append(out, o.pv.ID)
	}
	return out
}
