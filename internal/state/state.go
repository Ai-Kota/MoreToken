// Package state 免费层可用性的**观测器**（原 T-004 状态机，2026-09-20 降级）。
//
// 观测档位：FREE ──观测到免费层被证伪──→ PAID
//              ↑                          │
//              └──免费恢复健康 ≥T 分钟（平滑）─┘
//
// ⚠️ 它**不再是路由的决策者**。曾经是：router 的层门控按这个值过滤候选，
// 后果见 router.go「层门控为何被删除」（付费档缺 reasoning → auto:reasoning 全线 503；
// 且"兜底"顶了三分钟主力流量）。路由现在每请求独立决策，候选顺序恒为 free→paid。
//
// 本包只剩两件有用的事：
//  1. **定向探活**：对**被隔离的池**（池级退避中 / 有 key 冷却）发最小探测，成功即调
//     router.MarkHealthy 解退避 + 放长禁——给被隔离的池一条复活通道。
//  2. **平滑上报**：把观测档位与事件发出去供人看（NATS / WorkBoard）。
//     防抖（I5）留在这里——对"给人看的信号"降噪是对的；但它不再有权拦住任何一个请求。
//
// 纯标准库零依赖；时钟可注入（测试 fake clock 秒级跑通"第 N 分钟"）。
package state

import (
	"context"
	"sync"
	"time"

	"moretoken/internal/config"
)

// Tier 当前应服务的层。
type Tier int

const (
	TierFree Tier = iota
	TierPaid
)

// ProbeFn 对单个免费 provider 发一次最小探测（成本近 0），返回是否健康。
// 探测请求是安全操作（I7：可重放），429/超时都算"不健康"但不惩罚探测本身。
type ProbeFn func(ctx context.Context, p config.Provider) (bool, error)

// Options 状态机构造选项。
type Options struct {
	// HealthyFor 确定性回归阈值：免费层连续健康需 ≥ 此时长才切回（默认 3 分钟，ADR-003 "默认 3-5"）。
	// I4 的"≥T 分钟"就是这个量。
	HealthyFor time.Duration
	// ProbeInterval 后台探测周期（默认 30s；宁慢勿抖，探测频率本身是防抖的一部分）。
	ProbeInterval time.Duration
	// Now 时钟注入（默认 time.Now）。测试用 fake clock 秒级驱动。
	Now func() time.Time
	// FreeProviders 参与探测/回归判定的免费层 provider（从 config 过滤）。
	FreeProviders []config.Provider
	// Probe 探测函数（生产用 provider.Invoke 最小 body；测试用 fake 精确控制"第 N 分钟"）。
	Probe ProbeFn
	// Sidelined 返回当前**被隔离**的免费 provider（池级退避中 / 有 key 在冷却）。
	//
	// 它决定后台探测的**时机与范围**。探活的意义是给被隔离的池一条复活通道
	// （MarkHealthy 解退避 + 放长禁），所以"哪些池被隔离了就探哪些"才是一手需求。
	// 这里刻意**不接"当前档位"**——档位是全局的二手指标，不参与探测决策。
	// 未注入 = 不探测。
	Sidelined func() []config.Provider
}

// State 免费层可用性观测器（线程安全，可被多请求 + 后台探测协程共享）。
type State struct {
	mu    sync.Mutex
	cfg   Options

	tier        Tier      // 当前层
	healthyFor  time.Duration // 免费层连续健康累积时长（回归判定依据）
	lastProbeAt time.Time   // 上次成功探测时刻（累积用）
	hasProbeOk  bool        // 是否已有过一次成功探测（健康计时起点）

	// 事件回调（T-003 /decisions + T-005 WorkBoard 的输入；密钥安全——只记层/时刻/原因）。
	onEvent func(evt Event)
}

// Event 一次层切换/回归事件（决策留痕）。
type Event struct {
	At      time.Time
	From    Tier
	To      Tier
	Reason  string // "all-free-unavailable" / "free-healthy>=T" / "jitter-aborted"
}

// New 建状态机。默认：HealthyFor=3m、ProbeInterval=30s、Now=time.Now、tier=Free。
func New(opts Options) *State {
	if opts.HealthyFor <= 0 {
		opts.HealthyFor = 3 * time.Minute
	}
	if opts.ProbeInterval <= 0 {
		opts.ProbeInterval = 30 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.FreeProviders == nil {
		opts.FreeProviders = nil
	}
	return &State{cfg: opts, tier: TierFree}
}

// SetEventSink 注册事件回调（切层/回归时调用；nil 安全）。
func (s *State) SetEventSink(fn func(Event)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onEvent = fn
}

// Tier 当前的**观测档位**——一个平滑后的判定，供人读、供上报。
//
// ⚠️ 它不是路由输入（2026-09-20 起）。曾经的 IntTier（router.TierGate 的实现）
// 已随层门控一并删除：路由的兜底是每请求的，候选顺序恒为 free→paid。
//
// 正因为它是平滑值，它会**滞后**于实际服务情况：免费恢复后仍可能显示 paid，
// 直到连续健康累计满 HealthyFor。要看一手的实时状态，看同一份快照里的 pools
// （逐池的 backed_off / cooling）——那才是事实，这个是二手指标。
func (s *State) Tier() Tier {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tier
}

// AllFreeUnavailable 记录一次"免费层被证伪"的观测（由 router 的 sink 驱动）。
//
// ⚠️ 它**不再改变路由行为**。写下这个值只是把观测档位摆到 paid、清健康计时并广播事件；
// 免费流量是否受影响，完全由各免费池自身的退避/冷却决定，与这个值无关。
// 若已在付费，保持（幂等）；若从免费切来，清健康计时（防抖：重新挂 = 计时归零）。
func (s *State) AllFreeUnavailable(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tier == TierPaid {
		return
	}
	s.tier = TierPaid
	s.healthyFor = 0
	s.hasProbeOk = false
	s.lastProbeAt = s.cfg.Now()
	s.emitLocked(Event{At: s.cfg.Now(), From: TierFree, To: TierPaid, Reason: "all-free-unavailable"})
}

// ProbeFree 探测免费层（后台周期调用 + 测试手动驱动）。
// **任一**免费 provider 探测成功 → 免费层可用 → 累积健康时间；全部失败 → 不累积但也不清零
// （对齐 FreeLLMAPI 血泪：一次探测失败不该抹掉已累积的健康窗口，否则会反复抖动）。
// 达到 HealthyFor → 确定性回归免费（I4）；未达且免费又挂了 → 记 jitter-aborted（I5）。
//
// 为什么是"任一"而不是"全部"：免费池是主力，几十把 key 分散在多个 provider 上。
// 要求全绿才肯回归，等于让单个 provider 的长期故障（比如某家 key 被集体吊销）
// 把整个系统**永久**钉在付费兜底上——哪怕其余 provider 几十把 key 全是好的。
// 判"这一层还能不能用"的口径是"有没有能用的"，不是"是不是样样都好"。
// 注意：这里**不短路**，每个 provider 都要探——探活侧要拿逐个结果去解禁
// （router.MarkHealthy），短路会让排在后面的 provider 永远等不到解禁。
func (s *State) ProbeFree(ctx context.Context) Tier {
	s.mu.Lock()
	defer s.mu.Unlock()

	anyHealthy := false
	for _, p := range s.cfg.FreeProviders {
		ok, _ := s.cfg.Probe(ctx, p)
		if ok {
			anyHealthy = true
		}
	}
	now := s.cfg.Now()

	if anyHealthy {
		// 连续健康窗口（wall-clock）：本次探测把 [上次探测, 本次] 间隔计入。
		// 探测是周期的，健康"持续"就是相邻两次成功探测间隔都被填满（I4 "≥T 分钟"）。
		s.healthyFor += now.Sub(s.lastProbeAt)
		s.lastProbeAt = now
		s.hasProbeOk = true
		if s.tier == TierPaid && s.healthyFor >= s.cfg.HealthyFor {
			// I4 确定性回归：最近连续健康已满 T。
			s.tier = TierFree
			s.healthyFor = 0
			s.hasProbeOk = false
			s.lastProbeAt = now
			s.emitLocked(Event{At: now, From: TierPaid, To: TierFree, Reason: "free-healthy>=T"})
		}
		return s.tier
	}

	// 全部 provider 都不健康：连续窗口被打断 → 清零重新计时（I5 防抖：抖动太短不回归）。
	// 对齐 FreeLLMAPI health.js 的 consecutive-failure 语义：一次失败就重置连续计数。
	if s.hasProbeOk {
		s.healthyFor = 0
		s.hasProbeOk = false
		s.emitLocked(Event{At: now, From: s.tier, To: s.tier, Reason: "jitter-aborted"})
	}
	// 无论付费/免费，lastProbeAt 推进到 now（下段健康从此刻重新起算）。
	s.lastProbeAt = now
	return s.tier
}

// Run 后台探测循环：每 ProbeInterval 走一个周期，直到 ctx 取消。
// 调用方自己 go Run(ctx)。
func (s *State) Run(ctx context.Context) {
	tick := time.NewTicker(s.cfg.ProbeInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.TickOnce(ctx)
		}
	}
}

// TickOnce 一个探测周期（Run 的循环体，拆出来是为了可测——Run 自带真实 ticker，测不了时机）。
//
// 探测时机与范围由 **Sidelined** 决定：有被隔离的池才探。
//
// 原来是"停在付费档才探全部"。换成的口径有两个好处：
//   - 免费层正常时一个探测都不发（"在用就是健康"，省下本就紧张的免费额度）；
//   - 某个池被隔离、但整体仍停在免费档时（多 provider 场景）也能被探活解禁。
//     旧口径下这种池只能干等退避到期；若吃的是 401/403 长禁（1h），
//     则根本没有提前解除的途径——免费池随时间被慢慢啃掉。
func (s *State) TickOnce(ctx context.Context) {
	if len(s.sidelined()) == 0 {
		return
	}
	s.ProbeFree(ctx)
}

// sidelined 当前被隔离的免费 provider（Options.Sidelined 未注入时为 nil）。
// 只读外部快照，不持本包锁——避免"持 state.mu 去问 router"的锁序纠缠。
func (s *State) sidelined() []config.Provider {
	if s.cfg.Sidelined == nil {
		return nil
	}
	return s.cfg.Sidelined()
}

func (s *State) emitLocked(e Event) {
	if s.onEvent != nil {
		s.onEvent(e)
	}
}
