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

// TestRoute_Denied403_RotatesKeyNotModel —— 403 是 **key 级**事实（T-035）。
//
// 受控实验（2026-10-05，xkiro 实测，直打 api.xkiro.com）：
// **同一个模型** `qwen/qwen3-max:free`，逐把 key —— **4 把 200、4 把 403、1 把 429**。
// ⇒ "这个账号对这个模型没权限"是**按 key** 的，不是按模型的。
//
// 而 Router 原先走到 FailDenied 时只**换模型**、**不换 key** ⇒ 抽到无权限的那把，
// 整个 key 池的一半就被白扔了。这正是"不能循环找活模型"的根。
//
// 契约：同一 model，key A 回 403、key B 回 200 ⇒ 请求应当 **200**；
// 且 A **不被长禁**（403 不惩罚 key —— 2026-09-20 的成果必须保住）。
//
// 【反证】删掉 Router 里 `if fail.Kind == provider.FailDenied` 那个分支
// （回落到"只换模型"）⇒ 本条变红。
func TestRoute_Denied403_RotatesKeyNotModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer bad" {
			http.Error(w, `{"error":"no permission for this model"}`, http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"content":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{{
		ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
		Keys:   []string{"bad", "good"},
		Models: []config.Model{{ID: "m", Kinds: []string{"general"}}},
	}}}
	r := New(cfg, WithClient(http.DefaultClient))

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"auto:general","messages":[]}`),
		Kind: provider.NonStreaming, VirtualModel: true, WantKind: "general",
	})
	if err != nil {
		t.Fatalf("403 是 key 级事实，换一把 key 就该成功：%v", err)
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d, want 200 —— 抽到 403 的 key 后没有换 key 重试", res.StatusCode)
	}
	if h := r.Health(); h[0].Cooling != 0 {
		t.Errorf("Cooling = %d, want 0 —— 403 不该烧 key（2026-09-20 的成果必须保住）", h[0].Cooling)
	}
}

// TestRoute_Denied403_AllKeysDeniedFallsThrough —— 全部 key 都被 403 拒才落到模型/provider 级。
//
// 与上条互补：证明"换 key"是**有界**的（上界=池子大小），且试尽后行为与旧路径一致
// （不是死循环、不是把 403 当成功）。
func TestRoute_Denied403_AllKeysDeniedFallsThrough(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		http.Error(w, `{"error":"no permission"}`, http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{{
		ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
		Keys:   []string{"k1", "k2", "k3"},
		Models: []config.Model{{ID: "m", Kinds: []string{"general"}}},
	}}}
	r := New(cfg, WithClient(http.DefaultClient))

	_, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"auto:general","messages":[]}`),
		Kind: provider.NonStreaming, VirtualModel: true, WantKind: "general",
	})
	if err == nil {
		t.Fatal("三把 key 全 403 时应落到 provider 级失败，而不是当作成功")
	}
	mu.Lock()
	defer mu.Unlock()
	if hits > 3 {
		t.Errorf("上游命中 %d 次 > 池子大小 3 —— key 尝试上界应等于池子大小", hits)
	}
}
