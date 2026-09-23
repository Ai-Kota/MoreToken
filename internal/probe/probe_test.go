package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"moretoken/internal/config"
)

// 各测试用 provider 的探测模型：新契约下 Provider 必须拿到真模型名才发请求。
const testModel = "agnes-2.5-flash"

// keyedServer 起一个按 Authorization 头判定的 fake 上游：只有 good 前缀的 key 返回 200。
// 返回 (server, 收到的 Authorization 列表指针)。
func keyedServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if strings.HasSuffix(r.Header.Get("Authorization"), "good") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// TestProvider_FirstKeyDeadButOthersHealthy —— 本包存在的理由。
//
// 旧实现只试第一把 key：那把失效 → 探测永远失败 → 健康窗口永远累积不到阈值
// → 一旦进了付费层就再也回不到免费，哪怕其余几十把全是好的。
func TestProvider_FirstKeyDeadButOthersHealthy(t *testing.T) {
	srv, seen := keyedServer(t)
	p := config.Provider{
		ID: "p", BaseURL: srv.URL, Format: config.FormatOpenAI,
		Tier: config.TierFree, Keys: []string{"sk-dead", "sk-good", "sk-dead2"},
		Models: []config.Model{{ID: testModel}},
	}
	if !Provider(context.Background(), http.DefaultClient, p, 0, DefaultAttempts) {
		t.Fatalf("第一把失效但其余健康时应判健康（实际探测了 %v）", *seen)
	}
}

// TestProvider_AllKeysDead 全挂 → 不健康，且调用次数被 attempts 封顶。
func TestProvider_AllKeysDead(t *testing.T) {
	srv, seen := keyedServer(t)
	p := config.Provider{
		ID: "p", BaseURL: srv.URL, Format: config.FormatOpenAI,
		Tier: config.TierFree, Keys: []string{"sk-a", "sk-b", "sk-c", "sk-d", "sk-e"},
		Models: []config.Model{{ID: testModel}},
	}
	if Provider(context.Background(), http.DefaultClient, p, 0, DefaultAttempts) {
		t.Fatal("全挂时应判不健康")
	}
	if len(*seen) != DefaultAttempts {
		t.Errorf("探测了 %d 次，want %d（封顶，别把整池打一遍）", len(*seen), DefaultAttempts)
	}
}

// TestProvider_RoundRotatesStart 起点随 round 推进——保证每把 key 都有机会被采到。
func TestProvider_RoundRotatesStart(t *testing.T) {
	srv, seen := keyedServer(t)
	p := config.Provider{
		ID: "p", BaseURL: srv.URL, Format: config.FormatOpenAI,
		Tier: config.TierFree, Keys: []string{"sk-a", "sk-b", "sk-c"},
		Models: []config.Model{{ID: testModel}},
	}
	for round := uint64(0); round < 3; round++ {
		*seen = nil
		Provider(context.Background(), http.DefaultClient, p, round, 1)
		if len(*seen) != 1 {
			t.Fatalf("round=%d 应只探一次", round)
		}
	}
	// 三轮各取不同起点 → 三把 key 各被采到一次
	*seen = nil
	for round := uint64(0); round < 3; round++ {
		Provider(context.Background(), http.DefaultClient, p, round, 1)
	}
	want := []string{"Bearer sk-a", "Bearer sk-b", "Bearer sk-c"}
	if len(*seen) != 3 {
		t.Fatalf("采样 %d 次, want 3", len(*seen))
	}
	for i, w := range want {
		if (*seen)[i] != w {
			t.Errorf("第 %d 次采到 %q, want %q（起点未轮转）", i, (*seen)[i], w)
		}
	}
}

// TestProvider_NoRealKeys 全是未解析占位符 → 不健康，且不发任何请求。
func TestProvider_NoRealKeys(t *testing.T) {
	srv, seen := keyedServer(t)
	p := config.Provider{
		ID: "p", BaseURL: srv.URL, Format: config.FormatOpenAI,
		Tier: config.TierFree, Keys: []string{"env:NOT_SET", "vault:no/such", "env:"},
		Models: []config.Model{{ID: testModel}},
	}
	if Provider(context.Background(), http.DefaultClient, p, 0, DefaultAttempts) {
		t.Fatal("无真实凭据时不该判健康")
	}
	if len(*seen) != 0 {
		t.Errorf("无凭据却发了 %d 个请求", len(*seen))
	}
}

// TestProvider_AttemptsCappedByPoolSize 池比 attempts 小的时候不能越界。
func TestProvider_AttemptsCappedByPoolSize(t *testing.T) {
	srv, seen := keyedServer(t)
	p := config.Provider{
		ID: "p", BaseURL: srv.URL, Format: config.FormatOpenAI,
		Tier: config.TierFree, Keys: []string{"sk-only-dead"},
		Models: []config.Model{{ID: testModel}},
	}
	if Provider(context.Background(), http.DefaultClient, p, 0, DefaultAttempts) {
		t.Fatal("唯一一把是坏的，应判不健康")
	}
	if len(*seen) != 1 {
		t.Errorf("探测 %d 次, want 1（不能超过池大小）", len(*seen))
	}
}

// modelAwareServer 模拟真实上游对未知模型名的拒绝：只认 knownModel，其余一律 404
// （xkiro "Model \"x\" does not exist." 的形状；agnes 是 503 model_not_found）。
// 返回 (server, 收到的 model 列表指针)。
//
// 为什么要有它：状态机的探测体曾硬编码假名 "m"，两个上游都拒 → 探测恒不健康 →
// 进了付费层就再也回不到免费。假上游若对任何模型名都回 200，这个 bug 测不出来。
func modelAwareServer(t *testing.T, knownModel string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var probe struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&probe)
		seen = append(seen, probe.Model)
		if probe.Model != knownModel {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"message":"Model does not exist."}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// TestProvider_ProbesWithProvidersOwnModel —— 本包的第二条命。
//
// 探测体必须带该 provider 真实持有的模型名：带假名时上游拒绝 → 恒判不健康 →
// 状态机永远回不到免费层。这条测试若用"来者不拒"的假上游就抓不到这个 bug。
func TestProvider_ProbesWithProvidersOwnModel(t *testing.T) {
	const model = "agnes-2.5-flash"
	srv, seen := modelAwareServer(t, model)
	p := config.Provider{
		ID: "p", BaseURL: srv.URL, Format: config.FormatAnthropic,
		Tier: config.TierFree, Keys: []string{"sk-live"},
		Models: []config.Model{{ID: model, Kinds: []string{"reasoning"}}},
	}
	if !Provider(context.Background(), http.DefaultClient, p, 0, DefaultAttempts) {
		t.Fatalf("应带本 provider 的模型名 %q 探测并判健康（实际发出 %v）", model, *seen)
	}
	if len(*seen) != 1 || (*seen)[0] != model {
		t.Errorf("探测体里的 model = %v, want [%s]", *seen, model)
	}
}

// TestProvider_NoModelsUnhealthy provider 没配模型 → 不健康且不发请求。
//
// 没有可探的模型时绝不能退回假名探测（那等于恒判不健康，与本次事故同形），
// 也不能发空 model（同样换来 4xx）。按"无凭据"同一条口径直接判不健康。
func TestProvider_NoModelsUnhealthy(t *testing.T) {
	srv, seen := keyedServer(t)
	p := config.Provider{
		ID: "p", BaseURL: srv.URL, Format: config.FormatOpenAI,
		Tier: config.TierFree, Keys: []string{"sk-good"},
	}
	if Provider(context.Background(), http.DefaultClient, p, 0, DefaultAttempts) {
		t.Fatal("没配模型时不该判健康")
	}
	if len(*seen) != 0 {
		t.Errorf("没配模型却发了 %d 个请求", len(*seen))
	}
}

// TestProbeModel Takes models[0].ID；空/无模型一律 false。
func TestProbeModel(t *testing.T) {
	cases := []struct {
		name string
		p    config.Provider
		want string
		ok   bool
	}{
		{"无模型", config.Provider{}, "", false},
		{"空 ID", config.Provider{Models: []config.Model{{ID: "  "}}}, "", false},
		{"取第一个", config.Provider{Models: []config.Model{{ID: "a"}, {ID: "b"}}}, "a", true},
		{"去空白", config.Provider{Models: []config.Model{{ID: " a "}}}, "a", true},
	}
	for _, c := range cases {
		got, ok := probeModel(c.p)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: probeModel = (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

// TestKeys_SkipsPlaceholders Keys 只返回真实凭据。
func TestKeys_SkipsPlaceholders(t *testing.T) {
	p := config.Provider{Keys: []string{"sk-real", "env:X", "vault:y", "env:", "sk-real2"}}
	got := Keys(p)
	if len(got) != 2 || got[0] != "sk-real" || got[1] != "sk-real2" {
		t.Errorf("Keys = %v, want [sk-real sk-real2]", got)
	}
}
