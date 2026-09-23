package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"moretoken/internal/config"
	"moretoken/internal/provider"
)

func TestParseVirtual(t *testing.T) {
	cases := []struct {
		model  string
		kind   string
		virt   bool
		reason string
	}{
		{"auto", "", true, "无后缀 = 任意类型"},
		{"auto:reasoning", "reasoning", true, ""},
		{"AUTO:Coding", "coding", true, "大小写不敏感，kind 归一为小写"},
		{"auto:", "", true, "空 kind 视作任意类型"},
		{" auto : coding ", "coding", true, "两侧空白容忍"},
		{"agnes-2.5-flash", "", false, "字面模型"},
		{"autopilot", "", false, "前缀相似但非虚拟名"},
		{"", "", false, "空 model"},
	}
	for _, tc := range cases {
		kind, virt := ParseVirtual(tc.model)
		if virt != tc.virt || kind != tc.kind {
			t.Errorf("ParseVirtual(%q) = (%q,%v), want (%q,%v) %s",
				tc.model, kind, virt, tc.kind, tc.virt, tc.reason)
		}
	}
}

func TestSetModel(t *testing.T) {
	body := []byte(`{"model":"auto:reasoning","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	out, err := SetModel(body, "agnes-2.5-flash")
	if err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("改写后不是合法 JSON: %v", err)
	}
	if got["model"] != "agnes-2.5-flash" {
		t.Errorf("model = %v, want agnes-2.5-flash", got["model"])
	}
	// 其余字段必须原样保留
	if got["max_tokens"] != float64(100) || got["stream"] != true {
		t.Errorf("改写丢了字段: %v", got)
	}
	msgs, ok := got["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Errorf("messages 被破坏: %v", got["messages"])
	}
}

// TestSetModel_RejectsNonObject body 不是 JSON 对象时要报错——
// 绝不能把虚拟名当模型发上游（那会换来一个指向错误方向的 "model not found"）。
func TestSetModel_RejectsNonObject(t *testing.T) {
	for _, bad := range []string{`not json`, `[1,2,3]`, `"just a string"`} {
		if _, err := SetModel([]byte(bad), "m"); err == nil {
			t.Errorf("SetModel(%q) 应报错", bad)
		}
	}
}

func TestHasKindAndResolveModel(t *testing.T) {
	p := config.Provider{Models: []config.Model{
		{ID: "fast-one", Kinds: []string{"general", "fast"}},
		{ID: "smart-one", Kinds: []string{"reasoning", "coding"}},
	}}
	if !HasKind(p, "reasoning") || !HasKind(p, "General") {
		t.Error("HasKind 应命中（含大小写不敏感）")
	}
	if HasKind(p, "vision") {
		t.Error("HasKind 不该命中最没有的类型")
	}
	if m, ok := ResolveModel(p, "coding"); !ok || m != "smart-one" {
		t.Errorf("ResolveModel(coding) = (%q,%v), want smart-one", m, ok)
	}
	if m, ok := ResolveModel(p, ""); !ok || m != "fast-one" {
		t.Errorf("ResolveModel(空) 应取第一个 = fast-one, got (%q,%v)", m, ok)
	}
	if _, ok := ResolveModel(p, "vision"); ok {
		t.Error("无匹配类型时 ok 必须为 false（不得退而用不相干模型）")
	}
}

// captureRouter 建一个会记录收到的 model 字段的 fake 上游。
func captureRouter(t *testing.T, provs []config.Provider) (*Router, *[]string, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var models []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var probe struct {
			Model string `json:"model"`
		}
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		_ = json.Unmarshal(buf, &probe)
		mu.Lock()
		models = append(models, probe.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	for i := range provs {
		provs[i].BaseURL = srv.URL + "/v1"
	}
	return New(&config.Config{Providers: provs}, WithClient(http.DefaultClient)), &models, &mu
}

// TestRoute_VirtualModel_RewritesToConcrete 虚拟模型 → 落成具体模型名再发上游。
func TestRoute_VirtualModel_RewritesToConcrete(t *testing.T) {
	r, models, mu := captureRouter(t, []config.Provider{{
		ID: "p", Format: config.FormatOpenAI, Tier: config.TierFree,
		Keys: []string{"sk-1"},
		Models: []config.Model{
			{ID: "cheap", Kinds: []string{"general"}},
			{ID: "brainy", Kinds: []string{"reasoning"}},
		},
	}})

	body := []byte(`{"model":"auto:reasoning","messages":[]}`)
	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: body, Kind: provider.NonStreaming,
		VirtualModel: true, WantKind: "reasoning",
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Model != "brainy" {
		t.Errorf("Result.Model = %q, want brainy", res.Model)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*models) != 1 || (*models)[0] != "brainy" {
		t.Errorf("上游收到 model = %v, want [brainy]（虚拟名不得发上游）", *models)
	}
}

// TestRoute_LiteralModel_BodyUntouched 字面模型走原路径：body 一个字节都不动。
//
// 这是向后兼容的判据——新逻辑只对虚拟模型生效，既有客户端零行为变化。
func TestRoute_LiteralModel_BodyUntouched(t *testing.T) {
	r, models, mu := captureRouter(t, []config.Provider{{
		ID: "p", Format: config.FormatOpenAI, Tier: config.TierFree,
		Keys:   []string{"sk-1"},
		Models: []config.Model{{ID: "agnes-2.5-flash", Kinds: []string{"reasoning"}}},
	}})

	body := []byte(`{"model":"agnes-2.5-flash","messages":[]}`)
	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: body, Kind: provider.NonStreaming,
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Model != "" {
		t.Errorf("字面模型不该有解析结果，got %q", res.Model)
	}
	mu.Lock()
	defer mu.Unlock()
	if (*models)[0] != "agnes-2.5-flash" {
		t.Errorf("上游收到 %q, want agnes-2.5-flash", (*models)[0])
	}
}

// TestRoute_VirtualModel_KindFilteredCandidates 候选按类型过滤：
// 不持有该类型的 provider 直接被跳过，不会被选中后无模型可改写。
func TestRoute_VirtualModel_KindFilteredCandidates(t *testing.T) {
	var mu sync.Mutex
	var hit []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hit = append(hit, r.Host)
		mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "no-reasoning", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI,
			Tier: config.TierFree, Keys: []string{"sk-a"},
			Models: []config.Model{{ID: "plain", Kinds: []string{"fast"}}}},
		{ID: "has-reasoning", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI,
			Tier: config.TierFree, Keys: []string{"sk-b"},
			Models: []config.Model{{ID: "brainy", Kinds: []string{"reasoning"}}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"auto:reasoning"}`),
		Kind: provider.NonStreaming, VirtualModel: true, WantKind: "reasoning",
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.ProviderID != "has-reasoning" || res.Model != "brainy" {
		t.Errorf("选中 %s/%s，want has-reasoning/brainy", res.ProviderID, res.Model)
	}
}

// TestRoute_VirtualModel_NoProviderOfKind 无人提供该类型 → 明确报错，不静默降级。
func TestRoute_VirtualModel_NoProviderOfKind(t *testing.T) {
	r, _, _ := captureRouter(t, []config.Provider{{
		ID: "p", Format: config.FormatOpenAI, Tier: config.TierFree,
		Keys:   []string{"sk-1"},
		Models: []config.Model{{ID: "plain", Kinds: []string{"fast"}}},
	}})

	_, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"auto:vision"}`),
		Kind: provider.NonStreaming, VirtualModel: true, WantKind: "vision",
	})
	if err == nil {
		t.Fatal("无可提供该类型的 provider 时应报错，而不是退而用不相干的模型")
	}
	if !strings.Contains(err.Error(), "vision") {
		t.Errorf("错误信息应点名缺失的类型，got %v", err)
	}
}

// TestRoute_VirtualModel_PlainAutoAnyKind "auto"（无类型）不过滤候选，但仍要改写 model。
func TestRoute_VirtualModel_PlainAutoAnyKind(t *testing.T) {
	r, models, mu := captureRouter(t, []config.Provider{{
		ID: "p", Format: config.FormatOpenAI, Tier: config.TierFree,
		Keys:   []string{"sk-1"},
		Models: []config.Model{{ID: "only-one", Kinds: []string{"fast"}}},
	}})

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"auto"}`),
		Kind: provider.NonStreaming, VirtualModel: true, WantKind: "",
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Model != "only-one" {
		t.Errorf("Model = %q, want only-one", res.Model)
	}
	mu.Lock()
	defer mu.Unlock()
	if (*models)[0] != "only-one" {
		t.Errorf("上游收到 %q，虚拟名不得发上游", (*models)[0])
	}
}

// TestRoute_VirtualModel_FormatIsolation 虚拟模型仍受格式隔离约束。
func TestRoute_VirtualModel_FormatIsolation(t *testing.T) {
	r, _, _ := captureRouter(t, []config.Provider{{
		ID: "openai-only", Format: config.FormatOpenAI, Tier: config.TierFree,
		Keys:   []string{"sk-1"},
		Models: []config.Model{{ID: "m", Kinds: []string{"reasoning"}}},
	}})

	_, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic, Body: []byte(`{"model":"auto:reasoning"}`),
		Kind: provider.NonStreaming, VirtualModel: true, WantKind: "reasoning",
	})
	if err == nil {
		t.Fatal("anthropic 请求不该选中 openai provider")
	}
}
