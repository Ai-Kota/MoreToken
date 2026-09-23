// Package metrics 对外服务质量的**滚动窗口统计**。
//
// 第一性原理（为什么是"统计"而不是"探测"）：
// 网关每次真实请求**本来就知道**成功没成功、落到哪个模型、花了多久。这些是零成本的事实。
// 另起一个进程去探测同一件事，是在烧它本该保护的那份免费配额
// （model-watchdog 的前车之鉴：30–45s 探针 × 每模型 ≈ 2000+ req/天/模型，
//  对着 OpenRouter 免费档的 50 req/天/模型）。
//
// 所以本包只做一件事：把已经流过请求路径的事实**攒成可判定指标**。
//
// 产出两条对外信号（见 Snapshot）：
//   - 滚动窗口的成功率 / 请求量 / 延迟 —— 看"最近好不好"
//   - **DegradedSec —— 当前这轮连续失败持续了多久** —— 看"自愈失没失效"
//
// 第二条是关键：网关的绝大多数失败**是设计来自愈的**（key 冷却 60s / 池退避 60s /
// 免费层恢复 ~3min / dev-fleet 拉进程 5min）。所以"有失败"不是异常，"**失败超过了
// 自愈承诺的时间**"才是。DegradedSec 就是那个判据的输入——订阅方拿它跟承诺比，
// 超了才升级，不超就不打扰。（否则 9.5 小时内的 7 次降级会把 agent 叫来 7 次，
// 而 7 次全是自愈能搞定的。）
package metrics

import (
	"fmt"
	"sync"
	"time"
)

// KindStat 一个 (format, kind) 维度的计数。
type KindStat struct {
	Total int `json:"total"`
	Fail  int `json:"fail"`
}

// bucket 一分钟的计数桶。
type bucket struct {
	minute int64 // Unix 分钟
	total  int
	ok     int
	fail   int
	sumMs  int64
	maxMs  int64
	kinds  map[string]KindStat
}

func (b *bucket) reset(minute int64) {
	b.minute = minute
	b.total, b.ok, b.fail, b.sumMs, b.maxMs = 0, 0, 0, 0, 0
	b.kinds = nil
}

// Window 固定分钟数的滚动窗口（每分钟一桶，按分钟取模复用）。
//
// 为什么按时钟分钟而不是"最近 N 次请求"：判据要能与**时间**比较
//（"自愈承诺 3 分钟"），按请求数计的窗口换算成时间会随流量漂。
type Window struct {
	mu      sync.Mutex
	buckets []bucket
	now     func() time.Time

	// degradedSince 当前这轮连续失败的起点（零值 = 此刻没有在失败）。
	// 一次成功即清除——它是"这一波坏掉了"的检测器，不是"历史上坏过"。
	degradedSince time.Time
	lastFailAt    time.Time
}

// New 建一个 windowMin 分钟的滚动窗口（<=0 → 60）。now 为 nil → time.Now。
func New(windowMin int, now func() time.Time) *Window {
	if windowMin <= 0 {
		windowMin = 60
	}
	if now == nil {
		now = time.Now
	}
	return &Window{buckets: make([]bucket, windowMin), now: now}
}

// Observe 记一次请求结果。ok=true 表示这一条算成功。
//
// kind 由调用方归一化（见 proxy 的 kindLabel）：**不要**把字面模型名放进来，
// 那会让维度无限膨胀。虚拟模型用它的 kind，字面模型统一记 "literal"。
func (w *Window) Observe(at time.Time, kind string, ok bool, d time.Duration) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	m := at.Unix() / 60
	b := &w.buckets[int(m%int64(len(w.buckets)))]
	if b.minute != m {
		b.reset(m)
	}
	b.total++
	ms := d.Milliseconds()
	b.sumMs += ms
	if ms > b.maxMs {
		b.maxMs = ms
	}
	if b.kinds == nil {
		b.kinds = map[string]KindStat{}
	}
	ks := b.kinds[kind]
	ks.Total++
	if ok {
		b.ok++
		// 一次成功 = 这一波过去了，清掉"正在降级"。
		// 不清 lastFailAt —— 它是历史事实（"最后一次失败发生在何时"）。
		w.degradedSince = time.Time{}
	} else {
		b.fail++
		ks.Fail++
		w.lastFailAt = at
		if w.degradedSince.IsZero() {
			w.degradedSince = at
		}
	}
	b.kinds[kind] = ks
}

// Snapshot 当前窗口的聚合。
type Snapshot struct {
	WindowMin int `json:"window_min"`
	Total     int `json:"total"`
	OK        int `json:"ok"`
	Fail      int `json:"fail"`
	// SuccessRate 0..1；窗口内无请求时为 0（配合 Total==0 区分"全失败"与"没流量"）。
	SuccessRate float64             `json:"success_rate"`
	AvgMs       int64               `json:"avg_ms"`
	MaxMs       int64               `json:"max_ms"`
	ByKind      map[string]KindStat `json:"by_kind,omitempty"`

	// DegradedSec 当前连续失败已持续多少秒（0 = 此刻没在失败）。
	//
	// 订阅方的判据：拿它跟**自愈承诺**比——
	//   key 冷却 60s / 池退避 60s / 免费层恢复 ~180s / dev-fleet 拉进程 300s
	// 超过 N 倍仍不清零 ⇒ 自愈失效 ⇒ 该升级。
	// 没过阈值就**不要**打扰——降级是这台机器的常态，不是异常。
	DegradedSec int64 `json:"degraded_sec,omitempty"`
	LastFailAt  string `json:"last_fail_at,omitempty"`

	// Last5m 最近 5 分钟的切片（人/看板看的那个数）。
	Last5m *Slice `json:"last_5m,omitempty"`
}

// DefaultEscalateAfter 升级阈值：**2 倍的免费层自愈承诺**。
//
// 依据（2026-09-21 实测，9.5 小时窗口）：
//   - 免费层的自愈路径是「探活解禁 → 回归免费」，承诺 = HealthyFor(180s) + 探测周期余量
//   - 实测 7 次降级的恢复耗时：3.5 / 6.3 / 3.3 / 3.5 / 3.3 / 3.1 / 8.1 分钟
//     ⇒ 绝大多数落在 3.1–3.5 分钟（就是承诺 + 抖动），**两个离群值 6.3 与 8.1**
//   - 取 2×（6 分钟）：恰好把离群值挑出来，正常那 5 次不打扰
//
// 阈值取太松 = 该升级时没人来；取太紧 = 把"自愈正在正常工作"误报成故障。
// 后者更贵：它训练人忽略告警，而本仓已经在"假绿训练人忽略闸"上栽过一次。
const DefaultEscalateAfter = 6 * time.Minute

// Escalate 升级判据：**失败超过了自愈承诺的时间**才升级。
//
// 刻意不做的事：不按"有没有失败"判。网关绝大多数失败是设计来自愈的
//（key 冷却 60s / 池退避 60s / 免费层恢复 ~180s / dev-fleet 拉进程 300s），
// "有失败"是这台机器的常态，不是异常。
//
// 判据只用 DegradedSec ——它由**请求失败**驱动，所以适用的承诺恒为"免费层恢复"那一条；
// 池级退避（60s）之类不会单独把它顶起来（池退了但请求没失败时 DegradedSec 仍是 0）。
//
// 返回 (是否升级, 给人看的理由)。阈值 <=0 时用 DefaultEscalateAfter。
func Escalate(s Snapshot, after time.Duration) (bool, string) {
	if after <= 0 {
		after = DefaultEscalateAfter
	}
	if s.DegradedSec <= 0 {
		return false, ""
	}
	if time.Duration(s.DegradedSec)*time.Second <= after {
		return false, ""
	}
	return true, fmt.Sprintf(
		"连续失败已 %ds，超过自愈承诺上限 %s —— 自愈没生效，该叫人了（窗口成功率 %.1f%%，%d/%d 失败）",
		s.DegradedSec, after, s.SuccessRate*100, s.Fail, s.Total)
}

// Slice 窗口内一段子区间的计数。
type Slice struct {
	Total       int     `json:"total"`
	OK          int     `json:"ok"`
	Fail        int     `json:"fail"`
	SuccessRate float64 `json:"success_rate"`
	AvgMs       int64   `json:"avg_ms"`
}

// Snapshot 取当前快照。
func (w *Window) Snapshot() Snapshot {
	if w == nil {
		return Snapshot{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	now := w.now()
	cur := now.Unix() / 60
	n := int64(len(w.buckets))

	snap := Snapshot{WindowMin: len(w.buckets), ByKind: map[string]KindStat{}}
	var sumMs int64
	var recentTotal, recentOK, recentFail int
	var recentSumMs int64

	for i := range w.buckets {
		b := &w.buckets[i]
		// 陈旧桶（分钟号落在窗口之外）不计——它们还没被新数据覆盖。
		if b.minute > cur || cur-b.minute >= n {
			continue
		}
		snap.Total += b.total
		snap.OK += b.ok
		snap.Fail += b.fail
		sumMs += b.sumMs
		if b.maxMs > snap.MaxMs {
			snap.MaxMs = b.maxMs
		}
		for k, ks := range b.kinds {
			agg := snap.ByKind[k]
			agg.Total += ks.Total
			agg.Fail += ks.Fail
			snap.ByKind[k] = agg
		}
		if cur-b.minute < 5 {
			recentTotal += b.total
			recentOK += b.ok
			recentFail += b.fail
			recentSumMs += b.sumMs
		}
	}

	if snap.Total > 0 {
		snap.SuccessRate = float64(snap.OK) / float64(snap.Total)
		snap.AvgMs = sumMs / int64(snap.Total)
	}
	if recentTotal > 0 {
		snap.Last5m = &Slice{
			Total:       recentTotal,
			OK:          recentOK,
			Fail:        recentFail,
			SuccessRate: float64(recentOK) / float64(recentTotal),
			AvgMs:       recentSumMs / int64(recentTotal),
		}
	}
	if !w.degradedSince.IsZero() {
		snap.DegradedSec = int64(now.Sub(w.degradedSince).Seconds())
		if snap.DegradedSec < 0 {
			snap.DegradedSec = 0
		}
	}
	if !w.lastFailAt.IsZero() {
		snap.LastFailAt = w.lastFailAt.Format(time.RFC3339)
	}
	return snap
}
