// Package router 路由核心（T-002）：把 config + provider + pool 拼成可路由决策。
//
// 三条硬规则（ADR-003 四支柱的路由投影）：
//  1. 格式隔离（I3）：/v1/messages(anthropic) 与 /v1/chat/completions(openai)
//     各自只在同格式 provider 内选择，互不串选。
//  2. tier 优先（I1/I2）：任一 free provider 池有可用 key → 全走 free 层；
//     全部 free 池不可用（全 key 冷却）→ 才落 paid 链（显式顺序，不评分）。
//  3. 请求级 fallback（I7）：选中 provider 失败且失败发生在响应字节前
//     （provider.Fail.IsRetryable）→ 同请求内落到链上下一候选；
//     流已开（FailStreamMid）不重放。
//
// 不在本包：协议端点/NATS（T-003）、后台健康探测与 I4 确定性回归（T-004）。
// 冷却恢复由 pool 到期自动生效（T-001），故 fallback 后下一请求天然"回归免费"。
package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"moretoken/internal/config"
	"moretoken/internal/pool"
	"moretoken/internal/provider"
)

// RouteRequest 一次路由请求（入站，已被 T-003 的端点剥好协议壳）。
type RouteRequest struct {
	Format   config.Format
	Body     []byte
	Kind     provider.Kind
	StreamCB func([]byte) // Kind=Streaming 时透传给 provider

	// VirtualModel 为 true 时，Body 里的 "model" 是虚拟声明（auto / auto:<kind>）：
	// 路由按 WantKind 过滤候选，并在发送前把 model 改写成该候选**真实持有**的模型。
	// 为 false（客户端给的是字面模型）时 Body 一个字节都不动——既有行为完全不变。
	VirtualModel bool
	WantKind     string // 虚拟模型要求的类型；"" = 任意类型
}

// ErrContextTooLong 请求超出上游上下文上限——**确定性**失败，重试毫无意义。
//
// 由 proxy 映射成 413 + 可操作提示（"压缩会话或开新会话"），而不是一个含糊的 503：
// 智能体需要知道"是我送太多了"，而不是"服务挂了"。后者会让它重试，
// 而重试一个确定性失败只是白等（实测每个候选 60–80 秒）。
var ErrContextTooLong = errors.New("context window exceeded")

// ErrModelNotFound 请求的模型**任何候选都没有**——同样是确定性失败。
//
// 由 proxy 映射成 Anthropic 的 `404 not_found_error` + message `model: <name>`：
// Claude Code 据此归类为 `model_not_found`，显示"模型可能不存在或你没有权限，
// 运行 /model 换一个"——**这就把"卡死"变成了"人照做一句话"**。
var ErrModelNotFound = errors.New("model not found")

// Attempt 一次候选尝试的留痕（只记**没成功**的那些）。
//
// 为什么粒度要到候选这一层：503 的聚合原因（`exhausted ... (tried 2, limit 4)`）
// 回答不了"**是谁坏的、怎么坏的**"。
//
// 现场（2026-09-21 实测）：9.5 小时里 7 次突发降级、16 条请求失败，其中一次是
// **同一秒内 7 条一起挂**、`tried 3` 意味着 agnes/xkiro/付费**三个候选同时失败**。
// 三个不同上游同时挂，指向的往往不是上游而是本机（DNS/网络，见 TROUBLESHOOTING §5）——
// 但这是【推断】：当时的留痕**一个字都没记**，所以那个问题至今无法回答。
type Attempt struct {
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	// Kind 失败类别（provider.FailKind 的稳定短名）；"skipped_backoff" = 池级退避中被跳过，
	// **一次请求都没发**——它和"发了但失败"是两件事，混在一起会把误判洗成事实。
	Kind   string `json:"kind"`
	Status int    `json:"status,omitempty"`
	Reason string `json:"reason,omitempty"`

	// Stream 该请求是否流式（只在为真时出现）。**这是排障的关键一维**：
	// 非流式请求的"响应头"要等**整个生成完**才发，所以 `ResponseHeaderTimeout(90s)`
	// 对流式≈首字节、对非流式≈整段生成时长——两者语义完全不同。
	// 2026-09-21 排查 14 次 header 超时时，最大的障碍就是**不知道那条请求是不是流式**。
	Stream bool `json:"stream,omitempty"`
	// ElapsedMs 这个候选从发起到放弃花了多久（0 = 退避跳过，没发请求）。
	// 撞上 90s 头超时时这里会显示 ~90000，一眼可辨。
	ElapsedMs int `json:"elapsed_ms,omitempty"`
	// BodyBytes 请求体大小（排查"是不是大上下文把上游拖死了"）。
	BodyBytes int `json:"body_bytes,omitempty"`
}

// Result 路由结果（成功即 provider 的响应；4xx 透传时 Kind!=FailNone）。
type Result struct {
	provider.Result
	Fail       provider.Fail
	ProviderID string // 最终服务/失败的 provider（决策日志输入）
	Retried    bool   // 是否经过 fallback
	// Model 虚拟模型实际落到的具体模型 id（空 = 客户端给的是字面模型，原样透传）。
	// 这是"切换可见"的载体：智能体可据此发现上游模型变了，决定是否重述关键约束。
	Model string
	// Attempts 逐个候选的失败留痕，按尝试顺序。**失败路径才有**（成功路径为空）。
	// Route 在返回 error 时也会带上它，调用方据此留痕。
	Attempts []Attempt `json:"attempts,omitempty"`
}

// Router 持有全部 provider 的池与退避冷却，线程安全（可被多请求共享）。
type Router struct {
	mu       sync.Mutex // 保护 providers 的退避字段（inBackoff/markBackoff/ClearBackoff）
	client   *http.Client
	now      func() time.Time
	coolDur  time.Duration // provider 级退避时长
	maxRetry int           // 同请求 fallback 跳数上限

	providers []orderedProvider // 按 config 顺序（层内顺序即 fallback 顺序）
	// exhaustedSink 观测回调：免费层**真的被试过**且全挂时触发（见 Route）。
	// 它**不驱动路由** —— 兜底是每请求的，档位不参与决策。
	exhaustedSink FreeExhaustedSink
}

// 关于"层门控"（paidOnly / TierGate）为何被删除（2026-09-20，T-015）：
//
// 它曾把"层"实现成**过滤器**——付费层期间把 free 候选从链上删掉。三个后果：
//
//  1. 兜底变成**子集**。生产配置里付费档只有 agnes-3.0-flash（kinds=[coding,general]），
//     而主力工作负载是 auto:reasoning。门控一开，reasoning 的候选被删成空集 →
//     一个候选都不试 → 报 "all anthropic candidates cooling (tried 0, limit 4)" → 全线 503。
//     而同一刻免费池在出 200（2026-09-20 15:31:48.197 / 15:32:08.405）。
//  2. 兜底变成**主力**。付费层期间付费被**优先**走，且至少有 HealthyFor=3 分钟的最短占用期——
//     一次瞬态抖动就能换来三分钟的"兜底顶主力"，与"付费只是应急"的定性相反。
//  3. 连错误信息都说谎：门控清空候选后走到函数尾，报"候选都在冷却"，掩盖了"这个档位接不了这个活"。
//
// 兜底**本来就不需要档位**：candidates() 返回 free→paid 的有序链，循环遇不可用候选
// 就 continue / markBackoff 往后走——"这一次免费挂了就这一次用付费"是原生行为。
// 防抖由池级退避承担（60s / per-provider / 到期自愈），比全局档位细一级：
// 判错只赔一次尝试，不赔整层能力。

// orderedProvider 一个 provider 的路由运行时态。
type orderedProvider struct {
	pv         config.Provider
	pool       *pool.Pool
	keyVal     []string  // pool key ID 与之一一对应的真实 key 值
	unresolved int       // 未解析的占位符 key 数（供 /health 如实上报，见 PoolHealth.Unresolved）
	backoff    time.Time // 池级退避到期时刻（zero = 未退避）
	backoffSet bool
}

// Option Router 构造选项。
type Option func(*Router)

// WithClient 注入 HTTP client（生产默认 30s 超时）。
func WithClient(c *http.Client) Option {
	return func(r *Router) {
		if c != nil {
			r.client = c
		}
	}
}

// WithClock 注入时钟（测试 fake clock；生产 time.Now）。
func WithClock(fn func() time.Time) Option {
	return func(r *Router) {
		if fn != nil {
			r.now = fn
		}
	}
}

// WithBackoffDur 覆盖 provider 级退避时长（默认 60s，"防抖优先宁慢勿抖"）。
func WithBackoffDur(d time.Duration) Option {
	return func(r *Router) {
		if d > 0 {
			r.coolDur = d
		}
	}
}

// WithMaxRetry 覆盖同请求 fallback 跳数（默认 4，防全池挂时空转过久）。
func WithMaxRetry(n int) Option {
	return func(r *Router) {
		if n > 0 {
			r.maxRetry = n
		}
	}
}

// WithFreeExhaustedSink 注入"免费层被证伪"的观测回调（告警/留痕用，不驱动路由）。
//
// 触发门槛（两条缺一不可，见 Route）：
//   - attempted > 0：候选不能全落在池级退避窗口内（那是一个请求都没发）；
//   - freeAttempted > 0：**免费层必须真的被试过**——只试探过付费就报"免费耗尽"，
//     等于拿自我保护的余波当可用性证据。
//
// 回调侧自行幂等 + 防抖。
func WithFreeExhaustedSink(fn FreeExhaustedSink) Option {
	return func(r *Router) { r.exhaustedSink = fn }
}

// New 从 config 建 Router：每 provider 一个 key 池（RR + 429 key 冷却）。
func New(cfg *config.Config, opts ...Option) *Router {
	r := &Router{
		client:   &http.Client{Timeout: 30 * time.Second},
		now:      time.Now,
		coolDur:  60 * time.Second,
		maxRetry: 4,
	}
	for _, o := range opts {
		o(r)
	}
	r.providers = make([]orderedProvider, len(cfg.Providers))
	for i, p := range cfg.Providers {
		// 未解析的占位 key（env:/vault: 都没取到值）不进池（无凭据 ≠ 一把会 401 的假 key）：
		// 池只收真实 key，池空则该 provider 在 F2 空格下按"全池无可用"处理 → 503。
		ids, vals, unresolved := filterRealKeys(p)
		// 明文副本只留在 keyVal：这里置空的是**副本的 slice header**，不是底层数组——
		// 就地清空（p.Keys[j]=""）会同时清掉 config 本体与 state.FreeProviders() 那一份，
		// 导致 firstRealKey 恒空 → 探测恒不健康 → 永久卡在付费层（症状离病灶极远）。
		pv := p
		pv.Keys = nil
		r.providers[i] = orderedProvider{
			pv:         pv,
			pool:       pool.New(poolKeys(ids), pool.Options{CoolDur: r.coolDur, Now: r.now}),
			keyVal:     vals,
			unresolved: unresolved,
		}
	}
	return r
}

// filterRealKeys 过滤掉未解析的占位 key，只留真实凭据。
// 返回池 key ID（"provID/keyN"，N 为在真实 key 中的下标）、对应真实 key 值，
// 以及被过滤掉的占位符数（供 /health 如实上报"这个池里有几把是空的"）。
func filterRealKeys(p config.Provider) (ids, vals []string, unresolved int) {
	for _, k := range p.Keys {
		// 判据统一走 config.IsPlaceholder。旧写法在三处写死 "env:"，加 vault: 后必然漏改，
		// 漏改的后果是占位符被当真 key 发上游（Authorization: Bearer vault:… → 401）。
		if config.IsPlaceholder(k) {
			unresolved++
			continue // 无凭据 → 不进池
		}
		n := len(vals)
		ids = append(ids, p.ID+"/key"+strconv.Itoa(n))
		vals = append(vals, k)
	}
	return ids, vals, unresolved
}

func poolKeys(ids []string) []pool.Key {
	out := make([]pool.Key, len(ids))
	for i, id := range ids {
		out[i] = pool.Key{ID: id}
	}
	return out
}

// FreeExhaustedSink 可选的**观测回调**：免费层真的被试过且全挂时触发。
//
// 注意口径：它回答的是"免费层是否被证伪"，所以证据里必须有**至少一次免费的尝试**
// （见 Route 的 freeAttempted）。它不再驱动任何路由决策——付费/免费的选择是每请求的。
// 不注入 = 纯评测观测。
type FreeExhaustedSink func()

// Route 执行路由决策 + 调用。候选顺序**恒为**同格式内 free 层（config 顺序）→ paid 层；
// 失败可重试则 fallback 到下一候选（上限 maxRetry）；不可重试则返回本次响应透传。
//
// 兜底是**每请求**的：免费候选这一次不可用，这一次就落到付费；下一条请求照常先试免费。
// 没有"当前处于哪个层"这种状态参与决策——档位是观测指标，不是路由输入。
// 理由见上面「层门控为何被删除」。
func (r *Router) Route(ctx context.Context, req RouteRequest) (Result, error) {
	cands := r.candidates(req.Format, req.WantKind)
	if len(cands) == 0 {
		if req.VirtualModel && req.WantKind != "" {
			// 明确报错，不退而用一个不相干的模型——"要推理型"静默变成别的比失败更难查。
			return Result{}, fmt.Errorf(
				"router: no %s provider offers kind %q", req.Format, req.WantKind)
		}
		return Result{}, fmt.Errorf("router: no %s provider in config", req.Format)
	}
	hops := 0      // 已发生的 fallback 跳数（候选间）
	attempted := 0 // 真正发过请求的候选数（退避跳过的不算）
	// freeAttempted 真正发过请求的**免费**候选数——sink 的证据口径。
	// 只数"试过且失败"的免费候选：免费层一个请求都没发过，就谈不上"免费层被证伪"。
	freeAttempted := 0
	// attempts 逐个候选的失败留痕：503 的聚合原因回答不了"是谁坏的、怎么坏的"。
	var attempts []Attempt
	for i := range cands {
		if i > r.maxRetry {
			break
		}
		op := &cands[i] // 固定候选，供 tryCandidate 内换 key
		if r.inBackoff(op) {
			// 记一笔"跳过"——它和"发了但失败"是两件事。不记的话，事后看到
			// `tried 2` 却不知道第 3 个候选当时在退避，会把"自我保护"读成"没试"。
			attempts = append(attempts, Attempt{Provider: op.pv.ID, Kind: "skipped_backoff"})
			continue // 池级退避中；到期自动恢复（I4 回归由 pool/backoff 到期驱动）
		}
		attempted++
		if op.pv.Tier == config.TierFree {
			freeAttempted++
		}
		t0 := r.now()
		res, fail, done, model := r.tryCandidate(ctx, op, req)
		elapsed := r.now().Sub(t0)
		if done {
			return Result{
				Result:     res,
				Fail:       fail,
				ProviderID: op.pv.ID,
				Retried:    hops > 0,
				Model:      model,
			}, nil
		}
		if fail.Kind == provider.FailContextTooLong {
			// **确定性失败**：所有候选、所有 key 都会给同一个答案。
			// 立刻停 —— 继续试只是让调用方白等（实测每个候选 60–80 秒），
			// 而且最终仍是一个注定的失败。把原因原样上抛，让 proxy 给出可操作的提示。
			return Result{Attempts: attempts}, fmt.Errorf("%w: %s", ErrContextTooLong, fail.Reason)
		}
		if fail.Kind == provider.FailModelNotFound && !req.VirtualModel {
			// **字面模型**点名要的东西没人有 —— 换谁都是同一个答案。
			// 上抛让 proxy 翻成客户端认得的 `404 not_found_error`（它会显示
			// "运行 /model 换一个"，人照做一句话即可）。
			//
			// 虚拟模型走不到这里：它的模型名由我们解析，撤了就换同 provider 的下一个（T-018）。
			return Result{Attempts: attempts}, fmt.Errorf("%w: %s", ErrModelNotFound, fail.Reason)
		}
		if !fail.IsRetryable() {
			// 4xx（非 429）/ 流中断：重放无意义，透传本次响应（I7 边界）。
			return Result{
				Result:     res,
				Fail:       fail,
				ProviderID: op.pv.ID,
				Retried:    hops > 0,
				Model:      model,
				// 透传路径**也要带留痕**：2026-09-22 排查字面模型 404 时，
				// 决策日志里只有一条 `status=404`、attempts 为空——看不见是谁拒的。
				Attempts: attempts,
			}, nil
		}
		attempts = append(attempts, Attempt{
			Provider:  op.pv.ID,
			Model:     model,
			Kind:      fail.Kind.String(),
			Status:    res.StatusCode,
			Reason:    fail.Reason,
			Stream:    req.Kind == provider.Streaming,
			ElapsedMs: int(elapsed.Milliseconds()),
			BodyBytes: len(req.Body),
		})
		// 可重试（transport/5xx/池全挂）→ 池级退避 + fallback 下一候选。
		r.markBackoff(op)
		hops++
	}
	// 一个候选都没试（全落在池级退避窗口内）≠ 全池耗尽。
	//
	// 退避是本服务的自我保护状态（"刚失败过，先别急着再打"），不是"这一层死了"的证据。
	// 据此触发降级 = 一次 429 的余波就把整个系统切到付费兜底，还要等 ≥T 分钟健康窗口
	// 才可能回来（2026-09-19 16:41 的误判形态）。免费池几十把 key、多个模型，
	// 该被这样拉下水的门槛不能这么低——付费是兜底，不是主力。
	if attempted == 0 {
		return Result{Attempts: attempts}, fmt.Errorf(
			"router: all %s candidates cooling (tried 0, limit %d)", req.Format, r.maxRetry)
	}

	// 试过且全挂 → 发观测信号（告警留痕用，**不驱动路由**）。
	//
	// 门槛必须含"免费层真的被试过"（freeAttempted > 0）：
	// 仅当付费候选被试探而免费候选全在退避窗口内时，attempted 也非零，但"免费层耗尽"
	// 这个结论里一次免费的尝试都没有——那是把"自我保护状态"当成了"可用性证据"，
	// 与上面 attempted == 0 那条是同一个错误，只是绕过了那个出口。
	if r.exhaustedSink != nil && freeAttempted > 0 {
		r.exhaustedSink()
	}
	return Result{Attempts: attempts}, fmt.Errorf(
		"router: exhausted %s candidates (tried %d, limit %d)", req.Format, attempted, r.maxRetry)
}

// candidates 同格式 + tier 分层（free 前、paid 后），格式隔离（I3）。
//
// wantKind 非空（虚拟模型带类型）时额外按"该 provider 是否持有此类型的模型"过滤：
// 选到不持有此类型的 provider 将无模型可改写，只能退而用不相干的模型——
// 那比直接失败更难查（"我要推理型"静默变成了别的）。
func (r *Router) candidates(format config.Format, wantKind string) []orderedProvider {
	free, paid := []orderedProvider{}, []orderedProvider{}
	for i := range r.providers {
		op := &r.providers[i]
		if op.pv.Format != format {
			continue
		}
		if wantKind != "" && !HasKind(op.pv, wantKind) {
			continue
		}
		if op.pv.Tier == config.TierFree {
			free = append(free, *op)
		} else {
			paid = append(paid, *op)
		}
	}
	out := make([]orderedProvider, 0, len(free)+len(paid))
	out = append(out, free...)
	out = append(out, paid...)
	return out
}

// tryCandidate 在该 provider 内换 key 重试（最多 3 把），返回 done=true 表示调用方应返回。
// done 语义：成功 / 4xx（凭据类除外）/ 流中断 / 池内 key 耗尽——都不再 fallback 到下一 provider。
// 401/403 不在此列：那是 key 的问题不是请求的问题，换池内下一把，不惊动 provider 级 fallback。
//
// 第四个返回值是该候选实际使用的模型（虚拟模型解析结果；字面模型时为空）。
func (r *Router) tryCandidate(ctx context.Context, op *orderedProvider, req RouteRequest) (provider.Result, provider.Fail, bool, string) {
	// 模型候选：字面模型只有一条（body 原样不动）；虚拟模型取该 provider 持有的**全部**
	// 匹配模型，按偏好顺序。
	//
	// 为什么要列表而不是单个：模型和多 key 一样是消耗品——"上游悄悄不再提供某个模型"
	// （404）是常态。只认第一名等于把 provider 的能力钉死在 config 的第一行上，
	// 同 provider 的其余模型一行流量都不承担（2026-09-20 实测：带一个可用备用模型的
	// provider，首选 5xx 时请求整体失败）。
	models := []string{""}
	if req.VirtualModel {
		models = ResolveModels(op.pv, req.WantKind)
		if len(models) == 0 {
			return provider.Result{}, provider.Fail{
				Kind:   provider.FailUpstream,
				Reason: fmt.Sprintf("provider %s has no model of kind %q", op.pv.ID, req.WantKind),
			}, false, ""
		}
	}

	// 同一个 provider 内最多试几个模型：上游整体故障时，别把整张目录都打一遍
	// （免费额度按账号限流，一次请求烧 20 个模型比失败更糟）。
	const maxModels = 3
	for mi, m := range models {
		if mi >= maxModels {
			break
		}
		body := req.Body
		if req.VirtualModel {
			rewritten, err := SetModel(req.Body, m)
			if err != nil {
				// body 不是 JSON 对象：绝不能把虚拟名当模型发上游（那会换来一个
				// 指向错误方向的 "model not found"）。
				return provider.Result{}, provider.Fail{
					Kind: provider.FailUpstream, Reason: err.Error(),
				}, false, ""
			}
			body = rewritten
		}

		for attempt := 0; attempt < 3; attempt++ {
			key, ok := op.pool.Select()
			if !ok {
				// 全 key 冷却 → 该 provider 不可用，标记池级退避并 fallback 下一候选。
				// 这是 **key** 的问题，换模型无用。
				r.markBackoff(op)
				return provider.Result{}, provider.Fail{Kind: provider.FailUpstream, Reason: "all keys cooled"}, false, m
			}
			pvReq := provider.Request{
				Format:   string(op.pv.Format),
				BaseURL:  op.pv.BaseURL,
				APIKey:   op.keyVal[ints(key.ID, op.pv.ID)],
				Body:     body,
				Kind:     req.Kind,
				StreamCB: req.StreamCB,
				// 头预算按**请求体大小**给：TTFT 是上下文的函数，
				// 拿常数去卡会把大上下文的合法长等待误判成挂死
				//（2026-09-21/22 实测 25 条 503 全是 body 0.7–1.7MB 的请求撞上常数 90s）。
				HeaderBudget: provider.HeaderBudgetFor(len(body)),
			}
			res, fail := provider.Invoke(ctx, r.client, pvReq)
			if fail.Kind == provider.FailNone {
				if res.StatusCode == http.StatusNotFound && mi+1 < len(models) {
					// 404 = 上游**不认这个模型**。这是"模型维度"的事实，不是 provider 的、
					// 也不是客户端的：换同 provider 的下一个模型，别把 404 甩给调用方，
					// 也别整层 fallback 到别的 provider（那会白白绕开一个还能用的服务商）。
					break
				}
				return res, fail, true, m
			}
			if fail.Kind == provider.FailRateLimit {
				op.pool.RecordRateLimit(key.ID) // key 级 429 冷却（I8），换下一把
				continue
			}
			if fail.Kind == provider.FailAuth {
				// 凭据被拒（401/403）：这把 key 已失效，长禁它并换池内下一把。
				// 不落到 provider 级 fallback——同一个 provider 的其余 key 多半是好的。
				op.pool.RecordDead(key.ID)
				continue
			}
			if fail.Kind == provider.FailStreamMid {
				return res, fail, true, m // 流中断不重放（I7）
			}
			if fail.Kind == provider.FailTransport {
				// 连接层失败：是 provider（或本机网络）的事实，与具体模型无关 → 不换模型。
				return res, fail, false, m
			}
			if fail.Kind == provider.FailContextTooLong {
				// 上下文是**请求**的属性：换模型/换 key 都是同一个答案，直接上抛。
				return res, fail, false, m
			}
			// 注意 FailModelNotFound **不在此列**：对虚拟模型它该走下面的"换同 provider 的
			// 下一个模型"（T-018：上游悄悄撤了某个模型，同 provider 还有别的）；
			// 只有**字面**模型才是请求级的确定性失败，由 Route 显式短路。
			// 走到这里 = **模型级**或 provider 级，先换模型试试。
			//
			//   - FailUpstream（5xx）：上游对"不认识的模型名"未必回 404——
			//     agnes 回的就是 503 model_not_found（见 TROUBLESHOOTING §3）。
			//   - FailDenied（403）：实测证明是"账号对这个模型没权限"，凭据是好的
			//     （同一把 key 对别的模型 200）。它**不碰 key**——403 曾与 401 同归
			//     FailAuth，于是无权模型会把该 provider 的 key 逐个长禁、整层下线一小时。
			// 有下一个模型就先换模型；没有才落到 provider 级 fallback。
			if mi+1 < len(models) {
				break
			}
			return res, fail, false, m
		}
		// 走到这里 = 该模型被上游拒（404/5xx）且还有下一个模型，或该模型 key 轮换用尽。
	}
	return provider.Result{}, provider.Fail{
		Kind: provider.FailUpstream, Reason: "model rotation exhausted"}, false, models[0]
}

// 退避字段的并发保护：op 指针共享，经 Router.mu 收敛。

func (r *Router) inBackoff(op *orderedProvider) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return op.backoffSet && r.now().Before(op.backoff)
}

func (r *Router) markBackoff(op *orderedProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	op.backoff = r.now().Add(r.coolDur)
	op.backoffSet = true
}

// PoolHealth 一个 provider 池的状态（/health 用）。
type PoolHealth struct {
	Provider string `json:"provider"`
	Format   string `json:"format"`
	Tier     string `json:"tier"`
	// Keys 是池内**真实可用**的 key 数（不含未解析占位符），契约见 workboard-contract.md。
	// 旧实现返回的是配置里的原始条数（含占位符），于是"6 把占位符全没解析、
	// 池实际 0 把、请求全 503"时它照样报 keys:3——对 WorkBoard 撒谎。
	Keys       int  `json:"keys"`
	Unresolved int  `json:"unresolved"` // 未解析的占位符数（key 指针没取到值）
	Cooling    int  `json:"cooling"`    // 冷却中的 key 数
	BackedOff  bool `json:"backed_off"` // 池级退避中（全 key 挂过）
}

// Health 返回全部 provider 池的状态快照（/health 端点）。
func (r *Router) Health() []PoolHealth {
	out := make([]PoolHealth, 0, len(r.providers))
	for i := range r.providers {
		op := &r.providers[i]
		out = append(out, PoolHealth{
			Provider:   op.pv.ID,
			Format:     string(op.pv.Format),
			Tier:       string(op.pv.Tier),
			Keys:       len(op.keyVal),
			Unresolved: op.unresolved,
			Cooling:    op.pool.CooldownCount(),
			BackedOff:  r.inBackoff(op),
		})
	}
	return out
}

// SelectSidelined 从 free 里挑出**当前被隔离**的 provider（池级退避中 / 有 key 在冷却）。
//
// "隔离"是逐池的一手事实，与"停在哪个档位"无关——后者是全局的二手指标。
// 探活的范围由前者决定，才可能在"整体还在免费档、只有某个池被隔离"时把它救回来：
// 旧口径"停在付费档才探全部"下，这种池只能干等退避到期；吃 401/403 长禁（1h）的
// 更是没有提前解除的途径，免费池随时间被慢慢啃掉。
//
// ⚠️ free 必须来自 **config 的 provider 列表**（`cfg.FreeProviders()`），
// 不能取自 Router 内部那份：`New` 会把副本的 `Keys` 置空（明文只留在 keyVal），
// 从 Router 里捞出来的 provider 没有 key，拿去做探测只会换来一堆"无凭据"失败。
//
// 无被隔离的池时返回 nil（调用方据此跳过这一轮探测）。
func SelectSidelined(free []config.Provider, hs []PoolHealth) []config.Provider {
	down := make(map[string]bool, len(hs))
	for _, h := range hs {
		if h.BackedOff || h.Cooling > 0 {
			down[h.Provider] = true
		}
	}
	if len(down) == 0 {
		return nil
	}
	out := make([]config.Provider, 0, len(down))
	for _, p := range free {
		if down[p.ID] {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		// 被隔离的全是付费池（或 free 里没有对应项）——对免费侧而言无事可做。
		// 这里必须归一成 nil：否则"只隔离了付费池"会返回一个非 nil 的空切片，
		// 与"无隔离池"成为两种不同的值，调用方就得记两套判断。
		return nil
	}
	return out
}

// ClearBackoff 解除某 provider 的池级退避（只解退避，不动 key 级冷却）。
func (r *Router) ClearBackoff(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.providers {
		if r.providers[i].pv.ID == id {
			r.providers[i].backoffSet = false
			return
		}
	}
}

// MarkHealthy 上游恢复确认：解除该 provider 的池级退避 + 全池**长禁**（401/403）。
//
// 由探活侧在探测成功时驱动（main 的 Probe 闭包）。此前 ClearBackoff 与 Pool.Clear
// 在生产路径上零调用点，注释却写着"生产由 T-004 探测成功时驱动"——注释是假的，
// 后果是长禁没有任何提前解除的途径，只能干等 1h，免费池随时间被慢慢啃掉。
//
// 权衡：探活成功只证明"这个池里至少有一把能用的 key"，不证明每把都好。
// 因此被解禁的死 key 会被再尝试一次，随即被 RecordDead 再次长禁——代价自限；
// 429 短冷却不在解除范围（见 pool.ClearDead）。
func (r *Router) MarkHealthy(id string) {
	for i := range r.providers {
		op := &r.providers[i]
		if op.pv.ID != id {
			continue
		}
		r.ClearBackoff(id)
		op.pool.ClearDead()
		return
	}
}

// ints 把 pool keyID（"provID/keyN"）映射回 keyVal 下标。
func ints(keyID, provID string) int {
	i := strings.LastIndex(keyID, "/key")
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(keyID[i+len("/key"):])
	if n < 0 {
		return 0
	}
	return n
}
