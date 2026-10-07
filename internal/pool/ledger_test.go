package pool

import (
	"strconv"
	"testing"
	"time"
)

// dailyBanPool 建带日账本的池（threshold 由参数控制；0 = 关）。
func dailyBanPool(n, ban int, ck *fakeClock) *Pool {
	keys := make([]Key, n)
	for i := range keys {
		keys[i] = Key{ID: strconv.Itoa(i)}
	}
	return New(keys, Options{CoolDur: 60 * time.Second, Now: ck.Now, DailyBan: ban})
}

// TestKeyPool_DailyBan —— T-040 核心判据：
// 某 key 当日 429 达到阈值 ⇒ 当日摘除（即使 60s 短冷却已过期它也不复活），
// 跨日（24:00）计数归零 ⇒ 自动复活。
func TestKeyPool_DailyBan(t *testing.T) {
	ck := &fakeClock{now: time.Unix(0, 0)}
	p := dailyBanPool(2, 4, ck)

	// 4 连 429（每次先拨过 60s 短冷却，模拟"冷却到期又撞墙"）。
	for i := 0; i < 4; i++ {
		p.RecordRateLimit("0")
		ck.Add(time.Hour) // 远超 60s 短冷却 ⇒ 短冷却必已过期
	}
	if ok := p.Ledger().Banned("0"); !ok {
		t.Fatal("4 连 429 后 key 0 应被当日摘除")
	}
	// 短冷却（~4h 前打的）早已过期，但日摘除仍在 ⇒ Select 只给 key 1。
	for i := 0; i < 4; i++ {
		k, ok := p.Select()
		if !ok {
			t.Fatalf("Select 应可用（key 1 还在）")
		}
		if k.ID != "1" {
			t.Fatalf("Select = %q, want 1（key 0 日摘除中）", k.ID)
		}
	}

	// 跨日（拨 25h 过 24:00）⇒ 计数归零，key 0 复活。
	ck.Add(25 * time.Hour)
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		k, ok := p.Select()
		if !ok {
			t.Fatalf("跨日后 Select 应全池可用")
		}
		seen[k.ID] = true
	}
	if !seen["0"] {
		t.Error("跨日 24:00 后 key 0 应复活（计数归零），10 次 Select 里没选上它")
	}
}

// TestKeyPool_DailyBanBelowThreshold —— 未达阈值不摘除（N=4 时 3 连 429 只走短冷却）。
func TestKeyPool_DailyBanBelowThreshold(t *testing.T) {
	ck := &fakeClock{now: time.Unix(0, 0)}
	p := dailyBanPool(2, 4, ck)
	for i := 0; i < 3; i++ {
		p.RecordRateLimit("0")
		ck.Add(time.Hour)
	}
	if p.Ledger().Banned("0") {
		t.Fatal("3 < 阈值 4，key 0 不应被摘除")
	}
	// 短冷却过期后 key 0 正常回池。
	ck.Add(time.Hour)
	if _, ok := p.Select(); !ok {
		t.Error("短冷却过期后池应可用")
	}
}

// TestKeyPool_DailyBanOff —— 回归闸：DailyBan=0 时行为与启用前完全一致
// （4 连 429 只走 60s 短冷却，冷却一过立刻回池，没有任何摘除）。
func TestKeyPool_DailyBanOff(t *testing.T) {
	ck := &fakeClock{now: time.Unix(0, 0)}
	p := dailyBanPool(2, 0, ck)
	for i := 0; i < 4; i++ {
		p.RecordRateLimit("0")
		ck.Add(90 * time.Second) // 过短冷却
	}
	if p.Ledger() != nil && p.Ledger().Banned("0") {
		t.Fatal("DailyBan=0 是回归闸：不该有任何摘除")
	}
	// 短冷却刚过期：key 0 立刻回池（对照 DailyBan 开时这里是 key 1）。
	ck.Add(time.Minute)
	k, ok := p.Select()
	if !ok {
		t.Fatal("Select 应可用")
	}
	// 不强制游标顺序，只断言"key 0 没被排除在外"：连选 8 次必须能选中 0。
	for i := 0; i < 8; i++ {
		k, _ = p.Select()
		if k.ID == "0" {
			return
		}
	}
	t.Error("DailyBan=0 时 key 0 在短冷却过后应可被选中，8 轮里一次都没有")
}

// TestKeyPool_DailyBanCountsDuringActiveCooldown —— 记账不因短冷却重叠而漏记：
// 已有更长冷却时 RecordRateLimit 提前返回，但 429 次数仍要累计（判据来自
// pool.go RecordRateLimit 的注释——日墙看"今天撞了几次"，与 60s 是否重叠无关）。
func TestKeyPool_DailyBanCountsDuringActiveCooldown(t *testing.T) {
	ck := &fakeClock{now: time.Unix(0, 0)}
	p := dailyBanPool(2, 2, ck)
	p.RecordRateLimit("0")
	p.RecordRateLimit("0") // 两次都在第一把 60s 冷却窗口内（不拨钟）⇒ 冷却不缩短，但计数=2
	if !p.Ledger().Banned("0") {
		t.Fatal("两次 429 = 阈值 2，即使处于短冷却中计数也必须累计")
	}
}
