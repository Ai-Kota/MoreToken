// Package pool（T-040）——key 级 429 日账本。
//
// 背景：RecordRateLimit 的 60s 短冷却只挡"这一分钟的墙"；免费档的额度墙是
// **日级**的（xkiro 429 原文自证："reached today's free-model token quota,
// wait for the daily reset"，见 TEST/model-evaluator/admitted.json）。
// 对"今天没了"的 key，60s 后解禁只换来再撞一次墙 + 再记一次 60s 冷却的循环。
//
// Ledger 记每把 key **当日 429 次数**：达到阈值 ⇒ 当日摘除（到 24:00 自动归零）。
// 计数只增不跨日，重启清零是安全方向——宁可多试一次，不可把还有额度的 key 封死。
package pool

import (
	"sync"
	"time"
)

// dayEntry 一把 key 的当日 429 计数。
type dayEntry struct {
	day   string    // 记账日（本地日期）；跨日即重置
	count int       // 当日 429 次数
	last  time.Time // 最近一次 429（诊断用）
}

// Ledger key 级 429 日账本（时钟可注入，测试拨钟过 24:00）。
type Ledger struct {
	mu      sync.Mutex
	entries map[string]*dayEntry
	now     func() time.Time
	// threshold 当日摘除阈值（0 = 关，Ledger 退化为纯记账）。
	threshold int
}

// NewLedger 建账本。threshold 缺省（传 0）不启用摘除，只记账——
// 调用方想用摘除时传 >0 的值。
func NewLedger(now func() time.Time, threshold int) *Ledger {
	if now == nil {
		now = time.Now
	}
	return &Ledger{entries: make(map[string]*dayEntry), now: now, threshold: threshold}
}

// Record 记一次 429（在 Pool.RecordRateLimit 命中时调用，I8 的记账延伸）。
// 跨日自动重置计数（判据：last 记账日 ≠ 今日本地日期）。
func (l *Ledger) Record(keyID string) {
	if l == nil || l.threshold == 0 {
		// threshold=0 是"关"：连记账也不做，Select 侧纯走短冷却现状。
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	day := l.now().Format("2006-01-02")
	e := l.entries[keyID]
	if e == nil {
		e = &dayEntry{}
		l.entries[keyID] = e
	}
	if e.day != day {
		*e = dayEntry{} // 跨日归零（24:00 解锁，无需人工）
	}
	e.day = day
	e.count++
	e.last = l.now()
}

// Banned 该 key 当下是否被当日摘除（计数 ≥ 阈值且仍是今天）。
func (l *Ledger) Banned(keyID string) bool {
	if l == nil || l.threshold == 0 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[keyID]
	if e == nil || e.day != l.now().Format("2006-01-02") {
		return false
	}
	return e.count >= l.threshold
}

// BannedCount 当下处于当日摘除的 key 数（诊断/决策日志用，与 CooldownCount 同型）。
func (l *Ledger) BannedCount() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	day := l.now().Format("2006-01-02")
	c := 0
	for _, e := range l.entries {
		if e.day == day && e.count >= l.threshold {
			c++
		}
	}
	return c
}
