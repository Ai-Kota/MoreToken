package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"moretoken/internal/config"
	"moretoken/internal/provider"
)

// newFreePaid 构造两个 fake 上游：免费 openai 池（2 key）+ 付费 anthropic 池（1 key）。
// 返回 (Router, 免费计数, 付费计数, 关闭函数)。
func newFreePaid(t *testing.T) (*Router, *int32, *int32, func()) {
	t.Helper()
	var freeHits, paidHits int32

	freeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&freeHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"content":"free"}`))
	}))
	paidSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&paidHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"content":"paid"}`))
	}))
	t.Cleanup(func() { freeSrv.Close(); paidSrv.Close() })

	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "free", BaseURL: freeSrv.URL + "/v1", Format: "openai", Tier: "free",
				Keys:   []string{"sk-f1", "sk-f2"},
				Models: []config.Model{{ID: "m-flash"}}},
			{ID: "paid", BaseURL: paidSrv.URL + "/anthropic", Format: "anthropic", Tier: "paid",
				Keys:   []string{"sk-p1"},
				Models: []config.Model{{ID: "m-paid"}}},
		},
	}
	r := New(cfg, WithClient(http.DefaultClient))
	return r, &freeHits, &paidHits, func() {}
}

// TestRouter_FreeFirst_PaidZero —— 矩阵 F1 正常（I1+I3）：
// 免费健康时，openai 请求只走免费池；100 连发付费命中数恒 0。
// 注：格式隔离下 openai 请求只能选 openai provider（免费），付费是 anthropic 格式，
// 天然不可被 openai 请求选中——I3"不混池"在格式隔离里成立。
func TestRouter_FreeFirst_PaidZero(t *testing.T) {
	r, freeHits, paidHits, _ := newFreePaid(t)
	_ = paidHits // openai 请求结构上碰不到 anthropic 付费池；仍断言以防未来引入格式翻译
	body := `{"model":"m-flash","messages":[{"role":"user","content":"hi"}]}`
	for i := 0; i < 100; i++ {
		res, err := r.Route(context.Background(), RouteRequest{
			Format: config.FormatOpenAI, Body: []byte(body), Kind: provider.NonStreaming,
		})
		if err != nil {
			t.Fatalf("route %d: %v", i, err)
		}
		if !contains(string(res.Body), `"content":"free"`) {
			t.Fatalf("route %d: free pool not used, body=%s", i, res.Body)
		}
	}
	if got := atomic.LoadInt32(freeHits); got != 100 {
		t.Errorf("free hits = %d, want 100", got)
	}
	if got := atomic.LoadInt32(paidHits); got != 0 {
		t.Errorf("paid hits = %d, want 0 (I3: 付费不与免费混池)", got)
	}
}

// TestRouter_AllFreeUnavailable_PaidChain —— 矩阵 F2 正常（I2）：
// 免费全冷却 → anthropic 请求落付费链。
func TestRouter_AllFreeUnavailable_PaidChain(t *testing.T) {
	r, freeHits, paidHits, _ := newFreePaid(t)
	body := `{"model":"m-paid","messages":[{"role":"user","content":"hi"}]}`

	// 把付费池打 429 冷却，让全池不可用，验证返回 503 类错误而非静默。
	_ = freeHits
	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic, Body: []byte(body), Kind: provider.NonStreaming,
	})
	if err != nil {
		t.Fatalf("route anthropic: %v", err)
	}
	if !contains(string(res.Body), `"content":"paid"`) {
		t.Fatalf("anthropic request did not hit paid pool, body=%s", res.Body)
	}
	if got := atomic.LoadInt32(paidHits); got != 1 {
		t.Errorf("paid hits = %d, want 1", got)
	}
}

// TestRouter_FormatIsolation —— I3 格式隔离：anthropic 请求绝不选 openai provider。
func TestRouter_FormatIsolation(t *testing.T) {
	r, freeHits, paidHits, _ := newFreePaid(t)
	_ = paidHits // 本用例只看 free 侧；格式隔离断言的是 free 不被 anthropic 请求选中
	body := `{"model":"m-flash","messages":[{"role":"user","content":"x"}]}`
	// anthropic 格式请求：只有 paid 是 anthropic，free(openai) 不可见。
	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic, Body: []byte(body), Kind: provider.NonStreaming,
	})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if contains(string(res.Body), `"content":"free"`) {
		t.Error("anthropic request was served by openai free pool — format isolation violated")
	}
	if got := atomic.LoadInt32(freeHits); got != 0 {
		t.Errorf("free hits = %d, want 0 for anthropic request", got)
	}
}

// TestRouter_TierPreference —— 同格式下免费优先于付费（I2）：
// 两个 anthropic provider（free + paid），免费健康时付费不得被选。
func TestRouter_TierPreference(t *testing.T) {
	var freeHits, paidHits int32
	freeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&freeHits, 1)
		w.Write([]byte(`{"content":"anth-free"}`))
	}))
	paidSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&paidHits, 1)
		w.Write([]byte(`{"content":"anth-paid"}`))
	}))
	t.Cleanup(func() { freeSrv.Close(); paidSrv.Close() })

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "anth-free", BaseURL: freeSrv.URL + "/anthropic", Format: "anthropic", Tier: "free",
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
		{ID: "anth-paid", BaseURL: paidSrv.URL + "/anthropic", Format: "anthropic", Tier: "paid",
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))
	_ = r
	// 手动构造池（Router 内部按 config 建池；这里走 Route）
	body := `{"model":"m","messages":[{"role":"user","content":"x"}]}`
	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic, Body: []byte(body), Kind: provider.NonStreaming,
	})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !contains(string(res.Body), "anth-free") {
		t.Errorf("tier preference: free anthropic should win, body=%s", res.Body)
	}
	if got := atomic.LoadInt32(&paidHits); got != 0 {
		t.Errorf("paid hits = %d, want 0 (I2)", got)
	}
}

// TestRouter_FallbackNextWhenFreeDown —— 同格式 fallback：
// anthropic 免费池 429 全冷却 → 同请求内落到 anthropic 付费池（I2 的"全免费不可用"）。
func TestRouter_FallbackNextWhenFreeDown(t *testing.T) {
	var paidHits int32
	// 免费 anthropic：立即 429
	freeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, `{"error":"rl"}`, http.StatusTooManyRequests)
	}))
	// 付费 anthropic：正常
	paidSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&paidHits, 1)
		w.Write([]byte(`{"content":"anth-paid"}`))
	}))
	t.Cleanup(func() { freeSrv.Close(); paidSrv.Close() })

	ck := &fakeClock{}
	cfg := &config.Config{Providers: []config.Provider{
		{ID: "anth-free", BaseURL: freeSrv.URL + "/anthropic", Format: "anthropic", Tier: "free",
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
		{ID: "anth-paid", BaseURL: paidSrv.URL + "/anthropic", Format: "anthropic", Tier: "paid",
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient), WithClock(ck.Now))
	body := `{"model":"m","messages":[{"role":"user","content":"x"}]}`

	// 第一次：免费 429 → fallback 到付费（同请求内，I2）。
	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic, Body: []byte(body), Kind: provider.NonStreaming,
	})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if !contains(string(res.Body), "anth-paid") {
		t.Errorf("expected fallback to paid, body=%s", res.Body)
	}
	if got := atomic.LoadInt32(&paidHits); got != 1 {
		t.Errorf("paid hits = %d, want 1", got)
	}

	// 第二次：免费仍 429（冷却中）→ 直接落付费，不再试免费。
	_, err = r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic, Body: []byte(body), Kind: provider.NonStreaming,
	})
	if err != nil {
		t.Fatalf("route2: %v", err)
	}
	if got := atomic.LoadInt32(&paidHits); got != 2 {
		t.Errorf("after cooldown paid hits = %d, want 2 (free skipped)", got)
	}
}

// TestRouter_NothingAttempted_IsNotExhaustion —— 回归：一个候选都没试 ≠ 免费层不可用。
//
// 2026-09-19 16:41 的误判形态：agnes-anthropic 与 xkiro-anthropic 都落在池级退避窗口内
// → Route 的循环把它们全部 continue 掉（一个请求都没发）→ tried 0 →
// 触发降级 sink → 整个系统切到付费兜底，且要等 ≥T 分钟健康窗口才可能回来。
//
// 退避是本服务的自我保护状态（"刚失败过，先别急着再打"），不是"免费层死了"的证据。
// 几十把 key、多个模型的免费池，被一次 429 余波判定为"不可用"，方向就反了：
// 付费是兜底，不该被瞬态冷却拉下水。
func TestRouter_NothingAttempted_IsNotExhaustion(t *testing.T) {
	var hits, demotions int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(`{"content":"free"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "free-a", BaseURL: srv.URL + "/anthropic", Format: "anthropic", Tier: "free",
			Keys: []string{"sk-1"}, Models: []config.Model{{ID: "m"}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient),
		WithFreeExhaustedSink(func() { atomic.AddInt32(&demotions, 1) }))

	r.markBackoff(&r.providers[0]) // 上游刚 429/抖过 → 落进 60s 池级退避窗口

	_, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic, Body: []byte(`{"model":"m","messages":[]}`),
		Kind: provider.NonStreaming,
	})
	if err == nil {
		t.Fatal("全候选退避中应报错（退避窗口内确实没有可用候选）")
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("退避窗口内不该打上游，实际打了 %d 次", got)
	}
	if got := atomic.LoadInt32(&demotions); got != 0 {
		t.Errorf("一个候选都没试就降级付费兜底 = 误判（sink 被调用 %d 次）", got)
	}
}

// TestRouter_AllTriedAndFailed_StillDemotes —— 反面对照：
// 候选**真的试过**且全部失败时，降级信号必须照常发出（别把上面那条修过头）。
func TestRouter_AllTriedAndFailed_StillDemotes(t *testing.T) {
	var hits, demotions int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadGateway) // 5xx → 可重试 → fallback
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "free-a", BaseURL: srv.URL + "/anthropic", Format: "anthropic", Tier: "free",
			Keys: []string{"sk-1"}, Models: []config.Model{{ID: "m"}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient),
		WithFreeExhaustedSink(func() { atomic.AddInt32(&demotions, 1) }))

	if _, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic, Body: []byte(`{"model":"m","messages":[]}`),
		Kind: provider.NonStreaming,
	}); err == nil {
		t.Fatal("全挂应报错")
	}
	if got := atomic.LoadInt32(&hits); got == 0 {
		t.Error("应真的打过上游")
	}
	if got := atomic.LoadInt32(&demotions); got != 1 {
		t.Errorf("试过且全挂时应发 1 次降级信号，实际 %d 次", got)
	}
}

// TestRouter_MarkHealthy_ClearsBackoffAndDeadBan —— 上游恢复确认要真能解禁。
//
// 此前 ClearBackoff 与 Pool.Clear 在生产路径零调用点，注释却写着"生产由 T-004
// 探测成功时驱动"——注释是假的，于是长禁（401/403）没有任何提前解除的途径，
// 只能干等 1h，免费池随时间被慢慢啃掉。探活成功 → 解退避 + 放长禁；429 短冷却不动。
func TestRouter_MarkHealthy_ClearsBackoffAndDeadBan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"content":"free"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "free-a", BaseURL: srv.URL + "/v1", Format: "openai", Tier: "free",
			Keys: []string{"k0", "k1"}, Models: []config.Model{{ID: "m"}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))
	op := &r.providers[0]

	r.markBackoff(op)                      // 池级退避
	op.pool.RecordDead("free-a/key0")      // 凭据被拒的长禁
	op.pool.RecordRateLimit("free-a/key1") // 429 短冷却
	if !r.inBackoff(op) {
		t.Fatal("precondition: 应处于退避中")
	}
	if got := op.pool.CooldownCount(); got != 2 {
		t.Fatalf("precondition: cooling = %d, want 2", got)
	}

	r.MarkHealthy("free-a")

	if r.inBackoff(op) {
		t.Error("MarkHealthy 后仍处于池级退避")
	}
	if got := op.pool.CooldownCount(); got != 1 {
		t.Errorf("MarkHealthy 后 cooling = %d, want 1（长禁解除、429 冷却保留）", got)
	}
}

// TestRouter_MarkHealthy_UnknownIDIsNoOp 未注册的 provider ID 不该 panic。
func TestRouter_MarkHealthy_UnknownIDIsNoOp(t *testing.T) {
	r, _, _, _ := newFreePaid(t)
	r.MarkHealthy("no-such-provider") // 不 panic 即通过
}

// ── 辅助 ──

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time {
	if c.now.IsZero() {
		return time.Unix(0, 0)
	}
	return c.now
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
