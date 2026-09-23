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

// 本文件锁定 T-015 的契约：**兜底是每请求的应急，不是一种模式。**
//
// 起因（2026-09-20 15:31:49–15:34:49 的真实事故）：
//   - 路由器曾把"层"实现成**过滤器**（paidOnly）：付费层期间把免费候选从链上删掉。
//   - 生产配置里付费档没有 reasoning 模型（agnes-paid-anthropic 只持 agnes-3.0-flash，
//     kinds=[coding,general]），于是门控一开，auto:reasoning 的候选被删成空集 →
//     一个候选都不试 → 报 "all anthropic candidates cooling (tried 0, limit 4)" → 全线 503。
//   - 而同一刻免费池在出 200（15:31:48.197 与 15:32:08.405 各一条）。判"这一层挂了"的依据不成立。
//
// 定性（用户）：**免费池是主力，付费只是暂时应急，不能当做主力使用。**
// 由此三条设计约束：
//  1. 触发：本请求真的需要（不是"系统进入了某个状态"）
//  2. 作用域：止于本请求（不粘连后续请求）
//  3. 持续：由主力真实状态决定，没有最短占用期
//
// 结论：**"层"不是路由输入**。候选顺序恒为 free → paid，防抖由池级退避承担
// （60s / per-provider / 到期自愈）——比全局档位细一级的粒度，判错只赔一次尝试，
// 不赔整层能力。曾经的 paidOnly 及其门控接口已删除，回归测试见下。

// advance 把 fake clock 推后 d。
//
// 起点必须显式落在 time.Unix(0,0)：fakeClock 零值时 Now() 返回 Unix(0,0)，
// 而 markBackoff 以"当时的 Now()+60s"为到期点。直接对零值 time.Time 做 Add
// 会落回公元 1 年（早于 1970），退避永远解不开——测试会假红。
func (c *fakeClock) advance(d time.Duration) {
	if c.now.IsZero() {
		c.now = time.Unix(0, 0)
	}
	c.now = c.now.Add(d)
}

// TestRoute_ReasoningServedWhenPaidLacksKind —— 2026-09-20 事故的逐字复现。
//
// 配置照抄生产子集：免费 anthropic 持 reasoning；付费 anthropic 只持 coding/general。
// 事故现场（旧代码 + 门控停在付费档）报的正是：
//
//	router: all anthropic candidates cooling (tried 0, limit 4)
//
// 本测试断言这种配置下 auto:reasoning **必须成功**，且落在免费池。
func TestRoute_ReasoningServedWhenPaidLacksKind(t *testing.T) {
	var freeHits, paidHits int32
	freeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&freeHits, 1)
		w.Write([]byte(`{"content":"free"}`))
	}))
	paidSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&paidHits, 1)
		w.Write([]byte(`{"content":"paid"}`))
	}))
	t.Cleanup(func() { freeSrv.Close(); paidSrv.Close() })

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "agnes-anthropic", BaseURL: freeSrv.URL, Format: "anthropic", Tier: "free",
			Keys: []string{"k1"},
			Models: []config.Model{{ID: "agnes-2.5-flash",
				Kinds: []string{"reasoning", "coding", "general", "fast"}}}},
		{ID: "agnes-paid-anthropic", BaseURL: paidSrv.URL, Format: "anthropic", Tier: "paid",
			Keys: []string{"kp"},
			Models: []config.Model{{ID: "agnes-3.0-flash",
				Kinds: []string{"coding", "general"}}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic,
		Body: []byte(`{"model":"auto:reasoning","max_tokens":16,` +
			`"messages":[{"role":"user","content":"hi"}]}`),
		Kind:         provider.NonStreaming,
		VirtualModel: true,
		WantKind:     "reasoning",
	})
	if err != nil {
		t.Fatalf("有免费 reasoning provider 时 auto:reasoning 不该失败：%v\n"+
			"（旧代码在付费档下报 \"all anthropic candidates cooling (tried 0, limit 4)\" —— 事故症状）", err)
	}
	if res.ProviderID != "agnes-anthropic" {
		t.Errorf("provider = %q, want agnes-anthropic（付费档没有 reasoning，不该落它）", res.ProviderID)
	}
	if res.Model != "agnes-2.5-flash" {
		t.Errorf("虚拟模型应改写为 agnes-2.5-flash，实际 %q", res.Model)
	}
	if got := atomic.LoadInt32(&freeHits); got != 1 {
		t.Errorf("free hits = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&paidHits); got != 0 {
		t.Errorf("paid hits = %d, want 0（兜底不该被触碰）", got)
	}
}

// TestRoute_PaidNeverPreemptsHealthyFree —— "兜底不能当主力"。
//
// 免费健康时，付费池一次都不许被碰 —— 与系统此前观测到什么无关
// （旧的层门控会让付费层期间**优先**走付费，正是被禁止的那件事）。
func TestRoute_PaidNeverPreemptsHealthyFree(t *testing.T) {
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
		{ID: "anth-free", BaseURL: freeSrv.URL, Format: "anthropic", Tier: "free",
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
		{ID: "anth-paid", BaseURL: paidSrv.URL, Format: "anthropic", Tier: "paid",
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))

	for i := 0; i < 20; i++ {
		if _, err := r.Route(context.Background(), RouteRequest{
			Format: config.FormatAnthropic, Body: []byte(`{"model":"m","messages":[]}`),
			Kind: provider.NonStreaming,
		}); err != nil {
			t.Fatalf("route %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&freeHits); got != 20 {
		t.Errorf("free hits = %d, want 20", got)
	}
	if got := atomic.LoadInt32(&paidHits); got != 0 {
		t.Errorf("paid hits = %d, want 0 —— 兜底被当主力用了", got)
	}
}

// TestRoute_FallbackIsPerRequest_NotSticky —— 应急只作用于**那一次**请求。
//
// 免费挂 → 本次落付费；池级退避一到期且免费恢复 → 下一条请求**立刻**回落免费。
// 不得有任何"付费档"粘性，也不得有最短占用期（旧实现是 ≥3 分钟）。
func TestRoute_FallbackIsPerRequest_NotSticky(t *testing.T) {
	var paidHits int32
	var freeDown int32 = 1
	freeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.LoadInt32(&freeDown) == 1 {
			w.WriteHeader(http.StatusBadGateway) // 5xx → 可重试 → 本次应急落付费
			return
		}
		w.Write([]byte(`{"content":"anth-free"}`))
	}))
	paidSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&paidHits, 1)
		w.Write([]byte(`{"content":"anth-paid"}`))
	}))
	t.Cleanup(func() { freeSrv.Close(); paidSrv.Close() })

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "anth-free", BaseURL: freeSrv.URL, Format: "anthropic", Tier: "free",
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
		{ID: "anth-paid", BaseURL: paidSrv.URL, Format: "anthropic", Tier: "paid",
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
	}}
	ck := &fakeClock{}
	r := New(cfg, WithClient(http.DefaultClient), WithClock(ck.Now))
	req := RouteRequest{
		Format: config.FormatAnthropic, Body: []byte(`{"model":"m","messages":[]}`),
		Kind: provider.NonStreaming,
	}

	// 请求 1：免费挂 → 这一次用付费（应急）。
	res, err := r.Route(context.Background(), req)
	if err != nil {
		t.Fatalf("route1: %v", err)
	}
	if res.ProviderID != "anth-paid" {
		t.Fatalf("route1 provider = %q, want anth-paid（应急）", res.ProviderID)
	}

	// 免费恢复 + 池级退避（60s）到期。
	atomic.StoreInt32(&freeDown, 0)
	ck.advance(61 * time.Second)

	// 请求 2：必须立刻回落免费，不许有"付费档"粘性。
	res, err = r.Route(context.Background(), req)
	if err != nil {
		t.Fatalf("route2: %v", err)
	}
	if res.ProviderID != "anth-free" {
		t.Errorf("route2 provider = %q, want anth-free —— 兜底粘住了（主力已恢复却继续用兜底）", res.ProviderID)
	}
	if got := atomic.LoadInt32(&paidHits); got != 1 {
		t.Errorf("paid hits = %d, want 1（只有第一次应急该碰付费）", got)
	}
}

// TestRoute_SinkRequiresFreeAttempt —— 降级信号必须以"免费层真的被试过"为证据。
//
// 洞（旧）：免费候选若都落在池级退避窗口内被 continue 跳过，只有付费候选被真的试过，
// 付费一挂 attempted==1 → sink 照样触发 —— 判"免费耗尽"，而免费层一个请求都没发过。
// 555e60c 立的规矩（退避不是可用性证据）当时只用在了 attempted==0 那个出口。
func TestRoute_SinkRequiresFreeAttempt(t *testing.T) {
	var paidHits, demotions int32
	paidSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&paidHits, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(paidSrv.Close)

	freeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"content":"free"}`))
	}))
	t.Cleanup(freeSrv.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "free-a", BaseURL: freeSrv.URL, Format: "anthropic", Tier: "free",
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
		{ID: "free-b", BaseURL: freeSrv.URL, Format: "anthropic", Tier: "free",
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
		{ID: "anth-paid", BaseURL: paidSrv.URL, Format: "anthropic", Tier: "paid",
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient),
		WithFreeExhaustedSink(func() { atomic.AddInt32(&demotions, 1) }))

	// 两个免费候选都在退避窗口内 → 一个请求都不会发给它们。
	r.markBackoff(&r.providers[0])
	r.markBackoff(&r.providers[1])

	if _, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic, Body: []byte(`{"model":"m","messages":[]}`),
		Kind: provider.NonStreaming,
	}); err == nil {
		t.Fatal("全挂应报错")
	}
	if got := atomic.LoadInt32(&paidHits); got != 1 {
		t.Errorf("paid hits = %d, want 1（只有付费候选被真的试过）", got)
	}
	if got := atomic.LoadInt32(&demotions); got != 0 {
		t.Errorf("免费层一个请求都没发过就报'免费耗尽'（sink 被调用 %d 次）= 误判", got)
	}
}
