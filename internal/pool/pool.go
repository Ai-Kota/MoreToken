// Package pool 多 key 轮换池（FreeLLMAPI 蓝本：账号级限流由多 key 吸收）。
//
// 职责（T-001 WU-2）：
//   - 健康 key 选择：RoundRobin 起步（无数据时的退化情形，对齐 FreeLLMAPI "no signal → no ranking"）
//   - 429 冷却：该 key 冷却期内不再被选（I8）
//   - 冷却到期自动恢复（恢复语义：到期即重新入池，不单独探测）
//
// 不在此包：请求转发（provider 包）、格式路由/层级状态机（T-002/T-004）。
// 并发安全：Pool 方法持互斥，可被多个请求 goroutine 共享。
package pool

import (
	"sync"
	"time"
)

// Key 池内一把 key 的标识（值本身不出现在日志里——密钥卫生）。
type Key struct {
	ID string
	// 真实 key 值由调用方（pool 持有者）保管，pool 只认 ID 选序；
	// 这样 429 冷却判定不接触密钥，决策日志也天然无泄密。
}

// bench 一把 key 的禁用记录。
//
// 记死因（dead）是为了让"上游恢复"能**精确解禁**：探活成功证明的是
// "这个池里还有能用的 key"，据此该解除的是一次性故障留下的长禁（401/403，1h），
// 而不是 429 的短冷却——后者是"这个账号现在没额度了"，一分钟后再来才合理，
// 提前解除只会立刻再撞一次墙。
type bench struct {
	until time.Time
	dead  bool // true = 凭据被拒的长禁；false = 429 短冷却
}

// Pool 一个 provider 的多 key 池。
type Pool struct {
	mu       sync.Mutex
	keys     []Key
	rrIndex  int              // round-robin 游标（"无数据"退化情形用）
	cooldown map[string]bench // keyID → 禁用记录（fake clock 下由测试拨钟）
	now      func() time.Time // 时钟注入（测试用 fake clock；默认 time.Now）
	coolDur  time.Duration    // 单次 429 的冷却时长
	deadDur  time.Duration    // 凭据被拒（401/403）的长禁时长
}

// Options 构造选项。
type Options struct {
	// CoolDur 单次 429 的冷却时长（默认 60s，对齐 FreeLLMAPI 启发式冷却量级）。
	CoolDur time.Duration
	// DeadDur 凭据被拒（401/403）的长禁时长（默认 1h）。
	DeadDur time.Duration
	// Now 时钟注入（默认 time.Now）。测试用 fake clock 精确控制"第 N 分钟"。
	Now func() time.Time
}

// New 建池。keys 至少 1 把（config 层已校验；这里防御性处理 0 把 = 空池）。
func New(keys []Key, opts Options) *Pool {
	if opts.CoolDur <= 0 {
		opts.CoolDur = 60 * time.Second
	}
	if opts.DeadDur <= 0 {
		opts.DeadDur = time.Hour
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	p := &Pool{
		keys:     append([]Key(nil), keys...),
		cooldown: make(map[string]bench),
		now:      opts.Now,
		coolDur:  opts.CoolDur,
		deadDur:  opts.DeadDur,
	}
	return p
}

// SetClock 替换时钟（测试拨钟入口；生产路径不用）。
func (p *Pool) SetClock(fn func() time.Time) { p.mu.Lock(); p.now = fn; p.mu.Unlock() }

// Select 选一把当下可用的 key：RoundRobin 跳过冷却中的 key。
// 返回 key 与是否可用；全池冷却时 ok=false（调用方按 I2 语义落到下一 provider / 付费链）。
func (p *Pool) Select() (Key, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	n := len(p.keys)
	if n == 0 {
		return Key{}, false
	}
	// 从 rrIndex 起扫一圈，找第一把不在冷却的。
	for i := 0; i < n; i++ {
		k := p.keys[(p.rrIndex+i)%n]
		b, benched := p.cooldown[k.ID]
		if benched && now.Before(b.until) {
			continue
		}
		// 选中：推进游标（FreeLLMAPI：即使本轮没选上也推进，避免反复撞同一把挂 key）。
		p.rrIndex = (p.rrIndex + i + 1) % n
		return k, true
	}
	// 全冷却：推进游标避免下次又撞同一把，返回不可用。
	p.rrIndex = (p.rrIndex + 1) % n
	return Key{}, false
}

// RecordRateLimit 把某 key 打入冷却（429 命中时调用，I8）。
// 不缩短已有更长的冷却（对齐 FreeLLMAPI "never shorten an existing bench"）。
func (p *Pool) RecordRateLimit(keyID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	until := p.now().Add(p.coolDur)
	if b, ok := p.cooldown[keyID]; ok && b.until.After(until) {
		return // 已有更长的冷却，保留
	}
	p.cooldown[keyID] = bench{until: until}
}

// RecordDead 把某 key 打入**长**禁（凭据被拒：401/403 时调用）。
//
// 与 429 的区别是恢复预期：429 是"额度暂时用尽"，60s 后值得再试；
// 凭据被拒是"这把 key 已失效/被禁"（xkiro 的失效信号就是
// `Invalid or disabled ClientApiKey`），分钟级重试没有意义。
// 用远超 429 的量级长禁，免得每轮请求都拿同一把死 key 撞一次墙。
// 同样不缩短已有更长的冷却。
func (p *Pool) RecordDead(keyID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	until := p.now().Add(p.deadDur)
	if b, ok := p.cooldown[keyID]; ok && b.until.After(until) {
		return // 已有更长的冷却，保留
	}
	p.cooldown[keyID] = bench{until: until, dead: true}
}

// Clear 把某 key 的冷却提前解除（钥匙级精确恢复；探活侧若将来能报"哪把 key 通了"，用它）。
func (p *Pool) Clear(keyID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.cooldown, keyID)
}

// ClearDead 解除全池的**长禁**（401/403），保留 429 短冷却。
//
// 上游恢复确认时调用（router.MarkHealthy）：探活只用一个 key 就成功，
// 证明的是"这个池还有能用的 key"，据此该放的是那次凭据故障留下的长禁——
// 没有它，被误判成 401/403 的 key 要干等 1h，免费池会被时间一点点啃掉。
// 429 短冷却不动：那代表"这个账号当下没额度"，提前解除只会立刻再撞一次。
func (p *Pool) ClearDead() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, b := range p.cooldown {
		if b.dead {
			delete(p.cooldown, id)
		}
	}
}

// CooldownCount 当下处于冷却的 key 数（诊断/决策日志用）。
func (p *Pool) CooldownCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	c := 0
	for _, b := range p.cooldown {
		if now.Before(b.until) {
			c++
		}
	}
	return c
}

// Len 池内 key 总数。
func (p *Pool) Len() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.keys) }
