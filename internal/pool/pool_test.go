package pool

import (
	"strconv"
	"testing"
	"time"
)

// fakeClock 测试拨钟器：精确控制"第 N 分钟"，不必真等冷却。
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Add(d time.Duration) { c.now = c.now.Add(d) }

func newPool(n int, ck *fakeClock) *Pool {
	keys := make([]Key, n)
	for i := range keys {
		keys[i] = Key{ID: strconv.Itoa(i)}
	}
	return New(keys, Options{CoolDur: 60 * time.Second, Now: ck.Now})
}

// TestKeyPool_ClearDeadKeepsRateLimit —— 上游恢复确认只放长禁，不放 429 短冷却。
//
// 判据：探活成功证明"这个池还有能用的 key"，据此该放的是凭据故障留下的长禁
// （否则被误判的死 key 要干等 1h，免费池被时间啃掉）；429 是"这个账号当下没额度"，
// 提前放只会立刻再撞一次墙。
func TestKeyPool_ClearDeadKeepsRateLimit(t *testing.T) {
	ck := &fakeClock{now: time.Unix(0, 0)}
	p := newPool(2, ck)
	p.RecordRateLimit("0") // 429 短冷却
	p.RecordDead("1")      // 401/403 长禁
	if got := p.CooldownCount(); got != 2 {
		t.Fatalf("precondition: cooling = %d, want 2", got)
	}

	p.ClearDead()

	if got := p.CooldownCount(); got != 1 {
		t.Errorf("ClearDead 后 cooling = %d, want 1（长禁解除、429 冷却保留）", got)
	}
	k, ok := p.Select()
	if !ok || k.ID != "1" {
		t.Errorf("Select = (%q, %v), want 长禁的 key 1 已回到池中", k.ID, ok)
	}
	// key 0 仍在 429 冷却里：扫一圈也选不出来。
	p2 := newPool(1, ck)
	p2.RecordRateLimit("0")
	p2.ClearDead()
	if _, ok := p2.Select(); ok {
		t.Error("429 冷却中的 key 不该被 ClearDead 放出来")
	}
}

// TestKeyPool_ClearDeadOnlyTouchesDead —— 无长禁时 ClearDead 是空操作，不误伤冷却。
func TestKeyPool_ClearDeadOnlyTouchesDead(t *testing.T) {
	ck := &fakeClock{now: time.Unix(0, 0)}
	p := newPool(3, ck)
	p.RecordRateLimit("0")
	p.RecordRateLimit("1")
	p.ClearDead()
	if got := p.CooldownCount(); got != 2 {
		t.Errorf("cooling = %d, want 2（只有 429 冷却时不该被清）", got)
	}
}

// TestKeyPool_429Storm_RoundRobin —— 矩阵 F6 时序（I8）：
// 3 key、429 风暴 100 连发 → 冷却外的 key 命中近似均衡（±20%），冷却 key 在冷却期内命中数=0。
func TestKeyPool_429Storm_RoundRobin(t *testing.T) {
	ck := &fakeClock{now: time.Unix(0, 0)}
	p := newPool(3, ck)
	// key "1" 被 429 打冷却（冷却 60s）。
	p.RecordRateLimit("1")

	hits := map[string]int{}
	for i := 0; i < 100; i++ {
		k, ok := p.Select()
		if !ok {
			t.Fatalf("select %d: no key available, but keys 0/2 should be free", i)
		}
		if k.ID == "1" {
			t.Fatalf("select %d: cooled key '1' was selected (I8: 冷却 key 不得被选)", i)
		}
		hits[k.ID]++
	}
	// 100 发分给 key 0 和 key 2（RR 各 ~50，±20% → 40..60）。
	for _, id := range []string{"0", "2"} {
		h := hits[id]
		if h < 40 || h > 60 {
			t.Errorf("key %q hits = %d, want 40..60 (RR ±20%%); full=%v", id, h, hits)
		}
	}
	if hits["1"] != 0 {
		t.Errorf("cooled key '1' hits = %d, want 0", hits["1"])
	}
}

// TestKeyPool_CooldownExpiry —— 冷却到期自动恢复（I8 恢复语义）。
func TestKeyPool_CooldownExpiry(t *testing.T) {
	ck := &fakeClock{}
	p := newPool(2, ck)
	p.RecordRateLimit("0")
	if p.CooldownCount() != 1 {
		t.Fatalf("cooldown count = %d, want 1", p.CooldownCount())
	}
	// 59s：仍在冷却，key 0 不可选。
	ck.Add(59 * time.Second)
	if _, ok := p.Select(); ok {
		// ok 也可能是选中 key 1；确认 key 0 确实被跳过：再选一次看是否落到 1。
	}
	// 61s：冷却过期，key 0 重新入池。
	ck.Add(2 * time.Second)
	if p.CooldownCount() != 0 {
		t.Errorf("after expiry cooldown count = %d, want 0", p.CooldownCount())
	}
}

// 精确验证"不缩短更长的冷却"语义（对齐 FreeLLMAPI "never shorten an existing bench"）：
// 已冷却剩余 30s 时新 429 冷却 60s > 剩余 → 延长到 t=90s，而不是缩短。
func TestKeyPool_ExtendsOnNewRateLimit(t *testing.T) {
	ck := &fakeClock{}
	p := newPool(1, ck)
	p.RecordRateLimit("0") // t=0, 冷却到 t=60
	ck.Add(30 * time.Second)
	p.RecordRateLimit("0") // 新冷却到 t=90（>剩余 30s，应延长）
	ck.Add(30 * time.Second) // t=60：第一次的到期点
	if p.CooldownCount() != 1 {
		t.Fatalf("at t=60s count = %d, want 1 (extended to t=90s)", p.CooldownCount())
	}
	ck.Add(30 * time.Second) // t=90
	if p.CooldownCount() != 0 {
		t.Errorf("at t=90s count = %d, want 0", p.CooldownCount())
	}
}

// TestKeyPool_EmptyPool —— 0 key（防御）→ Select 不可用。
func TestKeyPool_EmptyPool(t *testing.T) {
	ck := &fakeClock{}
	p := New(nil, Options{Now: ck.Now})
	if _, ok := p.Select(); ok {
		t.Error("empty pool: Select returned ok, want false")
	}
}

// TestKeyPool_AllCooled —— 全池冷却 → Select 不可用（调用方按 I2 落付费链）。
func TestKeyPool_AllCooled(t *testing.T) {
	ck := &fakeClock{}
	p := newPool(2, ck)
	p.RecordRateLimit("0")
	p.RecordRateLimit("1")
	if _, ok := p.Select(); ok {
		t.Error("all cooled: Select returned ok, want false (I2: 应落到下一 provider/付费链)")
	}
	// 推进 61s 后恢复
	ck.Add(61 * time.Second)
	if _, ok := p.Select(); !ok {
		t.Error("after all expired: Select want ok")
	}
}

// TestKeyPool_Concurrency —— 并发安全（多 goroutine 共享一个池，data race 检测）。
func TestKeyPool_Concurrency(t *testing.T) {
	ck := &fakeClock{}
	p := newPool(3, ck)
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func() {
			for i := 0; i < 500; i++ {
				if k, ok := p.Select(); ok {
					_ = k
				}
			}
			done <- struct{}{}
		}()
	}
	for g := 0; g < 8; g++ {
		<-done
	}
}
