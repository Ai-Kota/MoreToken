package metrics

import (
	"testing"
	"time"
)

// 滚动窗口指标的契约。
//
// 它服务的是一条判据：**"失败超过了自愈承诺的时间"才升级，不是"有失败就升级"**。
// 网关 9.5 小时里降级 7 次，而 7 次全是自愈能搞定的——按"有失败就叫"会白叫 7 次。

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestWindow(min int, ck *fakeClock) *Window { return New(min, ck.now) }

func TestWindow_AggregatesSuccessRateAndLatency(t *testing.T) {
	ck := &fakeClock{t: time.Unix(0, 0)}
	w := newTestWindow(60, ck)

	for i := 0; i < 9; i++ {
		w.Observe(ck.now(), "anthropic/reasoning", true, 100*time.Millisecond)
	}
	w.Observe(ck.now(), "anthropic/reasoning", false, 300*time.Millisecond)

	s := w.Snapshot()
	if s.Total != 10 || s.OK != 9 || s.Fail != 1 {
		t.Fatalf("total/ok/fail = %d/%d/%d, want 10/9/1", s.Total, s.OK, s.Fail)
	}
	if got := s.SuccessRate; got < 0.89 || got > 0.91 {
		t.Errorf("success_rate = %.3f, want ~0.9", got)
	}
	if s.AvgMs != 120 {
		t.Errorf("avg_ms = %d, want 120", s.AvgMs)
	}
	if s.MaxMs != 300 {
		t.Errorf("max_ms = %d, want 300", s.MaxMs)
	}
	if ks := s.ByKind["anthropic/reasoning"]; ks.Total != 10 || ks.Fail != 1 {
		t.Errorf("by_kind = %+v, want total=10 fail=1", ks)
	}
}

// TestWindow_DegradedSecIsTheTriggerInput —— 连续失败持续多久，是升级判据的输入。
//
// 【正证】中间一次成功 ⇒ 清零（那一波过去了）。
// 【反证】把 Observe 里 `w.degradedSince = time.Time{}` 那行删掉 ⇒ 清零断言变红。
func TestWindow_DegradedSecIsTheTriggerInput(t *testing.T) {
	ck := &fakeClock{t: time.Unix(0, 0)}
	w := newTestWindow(60, ck)

	w.Observe(ck.now(), "anthropic/reasoning", false, time.Millisecond)
	ck.advance(45 * time.Second)
	w.Observe(ck.now(), "anthropic/reasoning", false, time.Millisecond)

	if got := w.Snapshot().DegradedSec; got != 45 {
		t.Fatalf("degraded_sec = %d, want 45（这一波已经坏了 45 秒）", got)
	}

	// 一次成功 = 这一波结束
	w.Observe(ck.now(), "anthropic/reasoning", true, time.Millisecond)
	if got := w.Snapshot().DegradedSec; got != 0 {
		t.Errorf("degraded_sec = %d, want 0（成功即清零——它是「这一波」检测器，不是历史计数器）", got)
	}

	// 再坏一次：从此刻重新起算，不是接着上一波
	ck.advance(10 * time.Second)
	w.Observe(ck.now(), "anthropic/reasoning", false, time.Millisecond)
	if got := w.Snapshot().DegradedSec; got != 0 {
		t.Errorf("degraded_sec = %d, want 0（刚从此刻起算）", got)
	}
	if w.Snapshot().LastFailAt == "" {
		t.Error("last_fail_at 应保留（它是历史事实，不清）")
	}
}

// TestWindow_StaleBucketsAreExcluded —— 窗口外的旧数据不计。
//
// 桶是按分钟号取模复用的：走到第 N+1 分钟会覆写第 1 分钟的桶。但**还没被覆写**的那几格
// 仍躺在切片里——不按分钟号剔除，窗口就会悄悄变长，"最近 1 小时"变成"最近 2 小时"。
func TestWindow_StaleBucketsAreExcluded(t *testing.T) {
	ck := &fakeClock{t: time.Unix(0, 0)}
	w := newTestWindow(10, ck) // 10 分钟窗口

	w.Observe(ck.now(), "openai/any", true, time.Millisecond) // 第 0 分钟
	s := w.Snapshot()
	if s.Total != 1 {
		t.Fatalf("precondition: total = %d, want 1", s.Total)
	}

	ck.advance(11 * time.Minute) // 走出窗口
	if got := w.Snapshot().Total; got != 0 {
		t.Errorf("total = %d, want 0 —— 窗口外的旧桶不该被算进来", got)
	}
}

// TestWindow_Last5mIsASubSlice —— 最近 5 分钟切片。
func TestWindow_Last5mIsASubSlice(t *testing.T) {
	ck := &fakeClock{t: time.Unix(0, 0)}
	w := newTestWindow(60, ck)

	w.Observe(ck.now(), "openai/any", true, time.Millisecond) // 现在
	ck.advance(20 * time.Minute)
	w.Observe(ck.now(), "openai/any", false, time.Millisecond) // 20 分钟前那条应落在窗口内、5 分钟外

	s := w.Snapshot()
	if s.Total != 2 {
		t.Fatalf("窗口 total = %d, want 2", s.Total)
	}
	if s.Last5m == nil || s.Last5m.Total != 1 || s.Last5m.Fail != 1 {
		t.Errorf("last_5m = %+v, want total=1 fail=1（20 分钟前那条不算最近 5 分钟）", s.Last5m)
	}
}

// TestWindow_NoTrafficIsDistinguishableFromAllFail —— 没流量 ≠ 全失败。
//
// 订阅方必须能分开这两件事：前者是"没人用"（不该告警），后者是"全挂了"（该告警）。
// 判据是 Total==0（此时 SuccessRate 也是 0，光看它分不出来）。
func TestWindow_NoTrafficIsDistinguishableFromAllFail(t *testing.T) {
	ck := &fakeClock{t: time.Unix(0, 0)}

	empty := newTestWindow(60, ck).Snapshot()
	if empty.Total != 0 || empty.SuccessRate != 0 {
		t.Errorf("空窗口 = %+v，应有 total=0（调用方据此区分「没流量」与「全失败」）", empty)
	}

	w := newTestWindow(60, ck)
	w.Observe(ck.now(), "openai/any", false, time.Millisecond)
	allFail := w.Snapshot()
	if allFail.Total == 0 || allFail.SuccessRate != 0 {
		t.Errorf("全失败 = %+v，应 total>0 且 rate=0", allFail)
	}
}

// TestEscalate_OnlyWhenSelfHealingHasFailed —— 判据是"超过承诺"，不是"有失败"。
//
// 网关绝大多数失败是设计来自愈的（key 冷却 60s / 池退避 60s / 免费层恢复 ~180s /
// dev-fleet 拉进程 300s）。按"有失败就升级"，09-21 那 9.5 小时的 7 次降级会把 agent
// 叫来 7 次——而 7 次全是自愈能搞定的，每次都是去做一件已经在被做完的事。
func TestEscalate_OnlyWhenSelfHealingHasFailed(t *testing.T) {
	cases := []struct {
		name        string
		degradedSec int64
		want        bool
	}{
		{"此刻没在失败", 0, false},
		{"刚坏（远未到承诺）", 30, false},
		{"正好在承诺窗口内（实测正常恢复 3.1–3.5 分钟）", 200, false},
		{"超过 2× 承诺", 400, true},
	}
	for _, c := range cases {
		got, reason := Escalate(Snapshot{DegradedSec: c.degradedSec}, DefaultEscalateAfter)
		if got != c.want {
			t.Errorf("%s：degraded=%ds ⇒ escalate=%v, want %v", c.name, c.degradedSec, got, c.want)
		}
		if got && reason == "" {
			t.Errorf("%s：判升级必须给出理由（人要看得懂为什么被叫醒）", c.name)
		}
	}
}

// TestEscalate_DefaultThresholdFitsObservedDistribution —— 默认阈值要能挑出离群值、放过正常值。
//
// 实测 7 次降级的恢复耗时：3.5 / 6.3 / 3.3 / 3.5 / 3.3 / 3.1 / 8.1 分钟。
// 阈值取太松 = 该升级时没人来；取太紧 = 把"自愈正在正常工作"误报成故障，
// 而后者更贵——它训练人忽略告警（本仓在"假绿训练人忽略闸"上已经栽过一次）。
func TestEscalate_DefaultThresholdFitsObservedDistribution(t *testing.T) {
	// 正常的那 5 次（3.0–3.6 分钟）不得升级
	for _, sec := range []int64{186, 198, 210, 213, 216} {
		if esc, _ := Escalate(Snapshot{DegradedSec: sec}, DefaultEscalateAfter); esc {
			t.Errorf("degraded=%ds 是实测的正常恢复窗口，不该升级（阈值太紧了）", sec)
		}
	}
	// 两个离群值（6.3 / 8.1 分钟）必须升级
	for _, sec := range []int64{378, 486} {
		if esc, _ := Escalate(Snapshot{DegradedSec: sec}, DefaultEscalateAfter); !esc {
			t.Errorf("degraded=%ds 是实测的离群值，该升级（阈值太松了）", sec)
		}
	}
}

// TestWindow_NilSafe —— 没挂指标的网关（或测试里的零值）不许 panic。
func TestWindow_NilSafe(t *testing.T) {
	var w *Window
	w.Observe(time.Now(), "x", true, time.Millisecond) // 不 panic 即通过
	if s := w.Snapshot(); s.Total != 0 {
		t.Errorf("nil 窗口快照应为零值，实际 %+v", s)
	}
}
