package state

import (
	"context"
	"testing"
	"time"

	"moretoken/internal/config"
)

// ── fake clock + fake probe 基础设施 ──

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// probeSim 让测试精确控制"免费层在第 N 分钟"的健康状态，不必真发请求。
type probeSim struct {
	healthy bool
	hits    int
}

func (ps *probeSim) fn(ctx context.Context, p config.Provider) (bool, error) {
	ps.hits++
	return ps.healthy, nil
}

// newState 构造一个 HealthyFor=10 分钟、探测间隔 1 分钟的被测状态机，初始在免费层。
func newState(ck *fakeClock, ps *probeSim) *State {
	cfg := config.Provider{ID: "f", BaseURL: "http://x", Format: "openai", Tier: "free", Keys: []string{"k"}}
	s := New(Options{
		HealthyFor:    10 * time.Minute,
		ProbeInterval: 1 * time.Minute,
		Now:           ck.Now,
		FreeProviders: []config.Provider{cfg},
		Probe:         ps.fn,
	})
	// 起点已停付费（模拟"免费全挂 → 已切付费"的入场态）。
	s.AllFreeUnavailable(context.Background())
	return s
}

// probeByID 按 provider ID 给不同结论的探测桩（验证"任一健康"语义与逐池探活）。
type probeByID struct {
	healthy map[string]bool
	calls   []string
}

func (p *probeByID) fn(ctx context.Context, pr config.Provider) (bool, error) {
	p.calls = append(p.calls, pr.ID)
	return p.healthy[pr.ID], nil
}

// newStateMulti 同 newState，但挂 ids 这几个免费 provider（起点：已停付费）。
func newStateMulti(ck *fakeClock, fn ProbeFn, ids ...string) *State {
	provs := make([]config.Provider, len(ids))
	for i, id := range ids {
		provs[i] = config.Provider{ID: id, BaseURL: "http://x", Format: "openai", Tier: "free", Keys: []string{"k"}}
	}
	s := New(Options{
		HealthyFor:    10 * time.Minute,
		ProbeInterval: 1 * time.Minute,
		Now:           ck.Now,
		FreeProviders: provs,
		Probe:         fn,
	})
	s.AllFreeUnavailable(context.Background())
	return s
}

// TestState_AnyFreeProviderHealthyStillRecovers —— 回归：单个 provider 长期挂，
// 不该把整个系统永久钉在付费兜底上。
//
// 免费池是主力，几十把 key 分散在多个 provider 上；要求"全绿才肯回归"意味着
// 一家 key 被集体吊销 = 永远回不到免费，哪怕其余几十把全是好的。
// 判"这一层还能不能用"的口径是"有没有能用的"，不是"是不是样样都好"。
func TestState_AnyFreeProviderHealthyStillRecovers(t *testing.T) {
	ck := &fakeClock{now: time.Unix(0, 0)}
	ps := &probeByID{healthy: map[string]bool{"alive": true, "dead": false}}
	s := newStateMulti(ck, ps.fn, "alive", "dead")
	ctx := context.Background()

	if got := s.Tier(); got != TierPaid {
		t.Fatalf("entry tier = %v, want Paid", got)
	}
	// 只要有一个 provider 健康，连续窗口照样累积 → 满 T 后回归免费。
	for i := 0; i < 10; i++ {
		ck.Advance(1 * time.Minute)
		s.ProbeFree(ctx)
	}
	if got := s.Tier(); got != TierFree {
		t.Errorf("tier = %v, want Free（还有一个 provider 健康就该回归）", got)
	}
}

// TestState_AllFreeProvidersDeadStaysPaid —— 反面对照：全部 provider 都挂 → 不回归。
// 同时钉住"不短路"：探活侧要拿逐个结果去解禁（router.MarkHealthy），
// 短路会让排在后面的 provider 永远等不到解禁。
func TestState_AllFreeProvidersDeadStaysPaid(t *testing.T) {
	ck := &fakeClock{now: time.Unix(0, 0)}
	ps := &probeByID{healthy: map[string]bool{"a": false, "b": false}}
	s := newStateMulti(ck, ps.fn, "a", "b")
	ctx := context.Background()

	for i := 0; i < 12; i++ {
		ck.Advance(1 * time.Minute)
		s.ProbeFree(ctx)
	}
	if got := s.Tier(); got != TierPaid {
		t.Errorf("tier = %v, want Paid（全挂不该回归）", got)
	}
	// 每轮两个 provider 都要被探到（不短路）。
	want := 12 * 2
	if len(ps.calls) != want {
		t.Errorf("探测调用 %d 次, want %d（每轮每个 provider 都要探，短路会让后面的池等不到解禁）",
			len(ps.calls), want)
	}
}

// TestState_FaultInjection_FreeRecovery —— 矩阵 F3 时序（I4 核心注入序列）：
// t0 免费全挂→付费；t1 免费全恢复（健康计时起）；t1..T 期间持续探测→付费；
// t1+T 后下一次探测→必 free。断言 [t1, t1+T) 窗口 tier 恒 Paid，之后首探测 tier=Free。
func TestState_FaultInjection_FreeRecovery(t *testing.T) {
	ck := &fakeClock{now: time.Unix(0, 0)}
	ps := &probeSim{}
	s := newState(ck, ps)
	ctx := context.Background()

	if got := s.Tier(); got != TierPaid {
		t.Fatalf("entry tier = %v, want Paid (all free unavailable)", got)
	}

	// 免费恢复：持续探测但尚未满 10 分钟 → 仍停在付费（I4：≥T 分钟才切回）。
	ps.healthy = true
	for i := 0; i < 9; i++ {
		ck.Advance(1 * time.Minute)
		got := s.ProbeFree(ctx)
		if got != TierPaid {
			t.Fatalf("probe %d (t=%d min): tier = %v, want Paid (<T min)", i+1, i+1, got)
		}
	}
	// 第 10 分钟：满 T → 确定性切回免费（I4）。
	ck.Advance(1 * time.Minute)
	got := s.ProbeFree(ctx)
	if got != TierFree {
		t.Fatalf("at t=10 min: tier = %v, want Free (I4: free healthy >= T)", got)
	}
	// 切回后不再付费；再探测维持免费。
	ck.Advance(1 * time.Minute)
	if got := s.ProbeFree(ctx); got != TierFree {
		t.Errorf("after recovery tier = %v, want Free", got)
	}
	if ps.hits == 0 {
		t.Error("probe was never called")
	}
}

// TestState_FaultInjection_JitterBelowThreshold —— 矩阵 F4 时序（I5 防抖）：
// 免费恢复 (T-ε) 分钟即再次全挂 → 不回归，停留付费；第二次恢复满 T → 回归。
// 断言：抖动间隔 <T 的恢复事件后 tier 恒 Paid。
func TestState_FaultInjection_JitterBelowThreshold(t *testing.T) {
	ck := &fakeClock{now: time.Unix(0, 0)}
	ps := &probeSim{}
	s := newState(ck, ps)
	ctx := context.Background()

	// 第一次"恢复"：只健康 4 分钟（< 10）就再次全挂。
	ps.healthy = true
	for i := 0; i < 4; i++ {
		ck.Advance(1 * time.Minute)
		if got := s.ProbeFree(ctx); got != TierPaid {
			t.Fatalf("healthy probe %d: tier=%v, want Paid (<T)", i+1, got)
		}
	}
	// 抖动：免费再次全挂 → 健康计时清零，停留付费（I5：不抖动）。
	ps.healthy = false
	ck.Advance(1 * time.Minute)
	if got := s.ProbeFree(ctx); got != TierPaid {
		t.Fatalf("jitter probe: tier = %v, want Paid (I5)", got)
	}
	if s.healthyFor != 0 {
		t.Fatalf("after jitter healthyFor = %v, want 0 (cleared)", s.healthyFor)
	}

	// 第二次"恢复"：连续健康窗口从抖动失败时刻重新起算，需满 10 分钟才回归。
	ps.healthy = true
	for i := 0; i < 9; i++ {
		ck.Advance(1 * time.Minute)
		got := s.ProbeFree(ctx)
		if got != TierPaid {
			t.Fatalf("recovery probe %d: tier=%v, want Paid (<T)", i+1, got)
		}
	}
	// 第 10 次探测：连续 10min（lastProbeAt=抖动失败时刻 t=5）→ 满 T → 回归免费（I4）。
	ck.Advance(1 * time.Minute)
	if got := s.ProbeFree(ctx); got != TierFree {
		t.Fatalf("recovery probe 10: tier=%v, want Free (>=T)", got)
	}
}

// TestState_ProbeFailureResetsWindow —— 一次探测失败把"最近连续健康"清零（I5 防抖）：
// 免费恢复 6 分钟 + 一次失败 + 再恢复 → 需重新满 T 才回归（不是"保留 6min 再补 4min"）。
// 对齐 FreeLLMAPI health.js 的 consecutive 语义 + ADR-003 "宁慢勿抖"。
func TestState_ProbeFailureResetsWindow(t *testing.T) {
	ck := &fakeClock{now: time.Unix(0, 0)}
	ps := &probeSim{}
	s := newState(ck, ps)
	ctx := context.Background()

	// 健康 6 分钟（< T=10）。
	ps.healthy = true
	for i := 0; i < 6; i++ {
		ck.Advance(1 * time.Minute)
		if got := s.ProbeFree(ctx); got != TierPaid {
			t.Fatalf("healthy probe %d: tier=%v, want Paid (<T)", i+1, got)
		}
	}
	// 一次失败：连续窗口清零。
	ps.healthy = false
	ck.Advance(1 * time.Minute)
	s.ProbeFree(ctx)
	// 再恢复 6 分钟（仍需满 10 才回归——第 6 分钟才到 6min，未到 T）。
	ps.healthy = true
	for i := 0; i < 6; i++ {
		ck.Advance(1 * time.Minute)
		if got := s.ProbeFree(ctx); got != TierPaid {
			t.Fatalf("re-recovery probe %d: tier=%v, want Paid (window reset, <T)", i+1, got)
		}
	}
	// 再补 4 分钟（6+4=10）→ 回归。
	for i := 0; i < 4; i++ {
		ck.Advance(1 * time.Minute)
		got := s.ProbeFree(ctx)
		if i < 3 {
			if got != TierPaid {
				t.Fatalf("probe %d: tier=%v, want Paid", i+1, got)
			}
		}
	}
	// 最后一次刚好满 10 → 已切回（上面 i=3 那次）。
	if got := s.Tier(); got != TierFree {
		t.Errorf("final tier = %v, want Free", got)
	}
}

// TestState_Events —— 层切换留痕（T-003 /decisions + T-005 的输入）。
func TestState_Events(t *testing.T) {
	ck := &fakeClock{now: time.Unix(0, 0)}
	ps := &probeSim{}
	s := newState(ck, ps)
	var evts []Event
	s.SetEventSink(func(e Event) { evts = append(evts, e) })

	ctx := context.Background()
	ps.healthy = true
	for i := 0; i < 10; i++ {
		ck.Advance(1 * time.Minute)
		s.ProbeFree(ctx)
	}
	// 至少应有一条 free-healthy>=T 回归事件。
	found := false
	for _, e := range evts {
		if e.Reason == "free-healthy>=T" && e.From == TierPaid && e.To == TierFree {
			found = true
		}
	}
	if !found {
		t.Errorf("missing free-healthy>=T event; got %d events: %+v", len(evts), evts)
	}
}

// ── 探测时机：由**隔离状态**驱动，不由档位驱动（2026-09-20，T-015）──

// newStateSidelined 挂一个可控的 Sidelined + 计数探针。
func newStateSidelined(ck *fakeClock, ps *probeSim, sidelined func() []config.Provider) *State {
	free := config.Provider{ID: "f", BaseURL: "http://x", Format: "openai", Tier: "free", Keys: []string{"k"}}
	return New(Options{
		HealthyFor:    10 * time.Minute,
		ProbeInterval: 1 * time.Minute,
		Now:           ck.Now,
		FreeProviders: []config.Provider{free},
		Probe:         ps.fn,
		Sidelined:     sidelined,
	})
}

// TestState_TickOnce_NoProbeWhenNothingSidelined —— 免费层正常时一个探测都不发。
//
// "在用就是健康"：免费池没被隔离说明它正在服务，没必要花本就紧张的免费额度去探它。
func TestState_TickOnce_NoProbeWhenNothingSidelined(t *testing.T) {
	ck := &fakeClock{now: time.Unix(0, 0)}
	ps := &probeSim{healthy: true}
	s := newStateSidelined(ck, ps, func() []config.Provider { return nil })

	s.TickOnce(context.Background())

	if ps.hits != 0 {
		t.Errorf("无被隔离的池时不该探测，实际探了 %d 次", ps.hits)
	}
}

// TestState_TickOnce_ProbesWhenSidelined —— 有池被隔离就探（给它一条复活通道）。
//
// 旧口径是"停在付费档才探全部"，于是**整体还在免费档、只有某个池被隔离**时，
// 那个池只能干等退避到期；吃 401/403 长禁（1h）的更是没有提前解除的途径，
// 免费池随时间被慢慢啃掉。口径换成隔离状态驱动后才修好。
func TestState_TickOnce_ProbesWhenSidelined(t *testing.T) {
	ck := &fakeClock{now: time.Unix(0, 0)}
	ps := &probeSim{healthy: true}
	one := []config.Provider{{ID: "f", BaseURL: "http://x", Format: "openai", Tier: "free", Keys: []string{"k"}}}
	s := newStateSidelined(ck, ps, func() []config.Provider { return one })

	// 注意：此处**不**调 AllFreeUnavailable —— 观测档位仍是 FREE，
	// 但被隔离的池照样要探。这正是旧口径漏掉的那一格。
	if got := s.Tier(); got != TierFree {
		t.Fatalf("precondition: 观测档位应为 FREE，实际 %v", got)
	}
	s.TickOnce(context.Background())

	if ps.hits == 0 {
		t.Error("有池被隔离时应探测（否则那个池没有复活通道）")
	}
}
