// MoreToken —— 免费优先的会话保持型路由网关（:8462）。
//
// 组装：config → router → proxy 端点。纯标准库单二进制，零外部依赖。
// 定位与不变量见 docs/project/GOALS.md 与 docs/decisions/ADR-003。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"moretoken/internal/catalog"
	"moretoken/internal/config"
	"moretoken/internal/metrics"
	"moretoken/internal/nats"
	"moretoken/internal/probe"
	"moretoken/internal/proxy"
	"moretoken/internal/provider"
	"moretoken/internal/router"
	"moretoken/internal/state"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "config/config.json", "Provider config JSON path")
	var listen string
	flag.StringVar(&listen, "listen", "", "Listen address (overrides config; default from config or :8462)")
	var check bool
	flag.BoolVar(&check, "check", false, "只校验供给面是否覆盖需求面，然后退出（部署前闸，不启动服务）")
	var harvest bool
	flag.BoolVar(&harvest, "harvest", false, "从上游模型目录纳新（发现+实测），写 models.auto.json 后退出")
	var harvestTTL time.Duration
	flag.DurationVar(&harvestTTL, "harvest-ttl", 20*time.Hour, "清单比它新就跳过纳新（自节流）")
	var harvestMax int
	flag.IntVar(&harvestMax, "harvest-max", 12, "本次最多实测几个新模型（0=不限）")
	var harvestDelay time.Duration
	flag.DurationVar(&harvestDelay, "harvest-delay", 3*time.Second, "两次实测之间的间隔（免费额度按账号限流）")
	var escalateAfter time.Duration
	flag.DurationVar(&escalateAfter, "escalate-after", metrics.DefaultEscalateAfter,
		"连续失败超过此时长即判「自愈失效」（默认 2× 免费层自愈承诺）")
	var doctor bool
	flag.BoolVar(&doctor, "doctor", false, "（客户端模式）读运行中网关的 /doctor 并据结果决定退出码，不启动服务")
	var doctorURL string
	flag.StringVar(&doctorURL, "doctor-url", "http://127.0.0.1:8462/doctor", "（客户端模式）/doctor 地址")
	flag.Parse()

	if doctor {
		os.Exit(runDoctorClient(doctorURL))
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if check {
		os.Exit(runCoverageCheck(cfg))
	}
	if harvest {
		os.Exit(runHarvest(cfg, configPath, harvestTTL, harvestMax, harvestDelay))
	}
	if cfg.AutoAdded > 0 {
		// 纳新生效要能被看见：一个悄悄不生效的机制与没有这个机制没区别。
		log.Printf("纳新：从 %s 追加了 %d 个模型（只标 general，排在所有手写模型之后）",
			config.AutoFile, cfg.AutoAdded)
	}
	if listen == "" {
		listen = cfg.Listen
	}

	// 密钥解析：把 keys 里的 env:/vault: 指针换成真值（明文只在内存，不落盘）。
	// 失败**不 fatal**——保留占位符 → 该池按"无凭据"处理 → 503，而不是把占位符发上游换 401。
	// 但必须**响**：配置写错 vault path 却毫无提示，正是"线上全 503 却不知为什么"的成因。
	keyCtx, cancelKeys := context.WithTimeout(context.Background(), config.DefaultVaultTimeout)
	rep := cfg.ResolveKeys(keyCtx, nil)
	cancelKeys()
	log.Printf("keys: %d resolved, %d unresolved", rep.Resolved, rep.Unresolved)
	for _, d := range rep.Details {
		log.Printf("WARN unresolved key: %s", d)
	}

	declog := proxy.NewDecisionLog(0)
	// 对外服务质量指标：60 分钟滚动窗口，数据全部来自**真实请求**（零新增探测）。
	// 它随 status 定期发布，供 dev-fleet 判定"自愈失没失效"——见 internal/metrics 的包注释。
	mwin := metrics.New(60, nil)
	hc := &http.Client{Timeout: 10 * time.Second} // 探测专用短超时
	// 免费层观测器（原 T-004 状态机；2026-09-20 起不再参与路由决策，见 internal/state 包注释）：
	// 对被隔离的池做定向探活 + 平滑上报档位/事件。
	// 探测的取 key 策略（轮换 + 命中任意一把即健康）见 internal/probe 的说明——
	// 只认第一把会让探测退化成单点，那把一挂就再也回不到免费层。
	var probeRound atomic.Uint64
	// rt 先声明后构造：探测闭包要回调它的 MarkHealthy（探活成功 → 解禁该池），
	// 而 Sidelined 又要读它的池状态——两边互相引用，用变量先占位拆环。
	// 赋值发生在 st.Run 启动之前，闭包只在 Run/请求路径里被调用，无竞态。
	var rt *router.Router
	st := state.New(state.Options{
		FreeProviders: cfg.FreeProviders(),
		Probe: func(ctx context.Context, p config.Provider) (bool, error) {
			ok := probe.Provider(ctx, hc, p, probeRound.Add(1)-1, probe.DefaultAttempts)
			if ok && rt != nil {
				// 上游恢复确认：解除该池的池级退避与长禁（401/403）。
				rt.MarkHealthy(p.ID)
			}
			return ok, nil
		},
		// 探测时机 = **被隔离的池**存在时，而不是"处在某个档位时"。
		// 探活是给被隔离的池一条复活通道，范围由隔离状态决定才是一手口径。
		// provider 取自 cfg（带 key），不是从 rt 内部捞——那份副本的 Keys 已被置空。
		Sidelined: func() []config.Provider {
			if rt == nil {
				return nil
			}
			return router.SelectSidelined(cfg.FreeProviders(), rt.Health())
		},
	})
	// NATS 发布器（T-005）：exec nats.cli，缺失时降级 no-op（NATS 是增强非依赖）。
	pub := nats.NewPublisher()

	// 回归/抖动事件：写决策日志（/decisions 可见）+ 广播 NATS event（WorkBoard 订阅）。
	st.SetEventSink(func(e state.Event) {
		declog.Record(proxy.DecisionEntry{
			At:         e.At,
			ProviderID: "state-machine",
			Reason:     e.Reason,
		})
		if pub.Enabled() {
			// 异步发布：这条回调发生在**请求路径**上（router 判全挂 → AllFreeUnavailable），
			// 而 NATS 是增强非依赖。同步发=让一次 DNS/TLS 抖动把客户端请求拖住数秒，
			// 那比丢一条广播糟得多。
			go func() {
				_ = pub.Publish(nats.SubjectEvent, nats.EventPayload{
					At:     e.At,
					From:   tierName(e.From),
					To:     tierName(e.To),
					Reason: e.Reason,
				})
			}()
		}
	})

	// 注意这里**没有**层门控：兜底是每请求的，档位不参与路由（2026-09-20，见 router.go）。
	rt = router.New(cfg,
		router.WithClient(provider.UpstreamClient()),
		// 免费层真的被试过且全挂 → 记一次观测（留痕 + 上报），**不改变路由行为**。
		router.WithFreeExhaustedSink(func() { st.AllFreeUnavailable(context.Background()) }),
	)
	srv := proxy.NewServer(rt, cfg, declog).WithMetrics(mwin, escalateAfter)

	// /decisions 端点：暴露最近决策留痕（密钥安全——无 key 值）。
	mux := http.NewServeMux()
	mux.Handle("/", srv.Handler())
	mux.HandleFunc("/decisions", func(w http.ResponseWriter, req *http.Request) {
		n := 100
		if v := req.URL.Query().Get("n"); v != "" {
			if p, err := strconv.Atoi(v); err == nil && p > 0 {
				n = p
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(declog.Recent(n))
	})

	httpSrv := &http.Server{Addr: listen, Handler: mux}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 启动免费层后台探测（T-004 的"停在付费时周期探测"）。
	go st.Run(ctx)
	// 启动定期状态发布（T-005）：每 30s 发一次 /health 快照 + 当前层到 NATS。
	if pub.Enabled() {
		go runStatusPublisher(ctx, pub, rt, st, mwin)
	}

	go func() {
		<-ctx.Done()
		log.Printf("shutting down (listen %s)", listen)
		shutdown(httpSrv)
	}()

	log.Printf("MoreToken listening on %s (%d providers: %d free, %d paid)",
		listen, len(cfg.Providers), len(cfg.FreeProviders()), len(cfg.PaidProviders()))
	if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}

// runDoctorClient 「自愈承诺超时」升级判据的客户端：读运行中网关的 /doctor。
//
// 为什么要走 HTTP 而不是在本地算：**指标窗口活在跑着的那个进程里**。
// 另起一个进程自己起个窗口，永远是空的 ⇒ 永远报健康（一个只会说 ok 的监测器）。
//
// 退出码刻意分三档，好让 dev-fleet 的状态行和人一眼分清"谁病了"：
//
//	0  正常
//	1  **自愈失效**——网关还活着、但连续失败已超过承诺。这是该叫人的信号。
//	2  网关**不可达**——这是存活问题，归 /health 与 dev-fleet 的重启路径，**不是升级**。
//
// 分档的意义：把"网关死了"和"网关活着但自愈失效了"混成一个非零码，
// 就会让升级逻辑去处理它管不了的事（重启解决不了自愈失效）。
func runDoctorClient(url string) int {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		log.Printf("doctor: 网关不可达（%v）—— 这是存活问题，不是升级问题", err)
		return 2
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	var out struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(body, &out)

	if resp.StatusCode == http.StatusServiceUnavailable || out.Status == "escalate" {
		log.Printf("doctor: **自愈失效** —— %s", out.Reason)
		return 1
	}
	log.Printf("doctor: ok（连续失败在承诺窗口内或此刻无失败，指标 %s）", strings.TrimSpace(string(body)))
	return 0
}

// runHarvest 从上游模型目录纳新：发现 → 实测 → 入库（写 models.auto.json）。
//
// 为什么需要它（用户定性）：免费池是主力，而供给是**易腐资源**——促销结束、
// 上游撤模型、新模型上架，全在我们配置之外发生。纯人工维护只会单调衰减，
// 而补回来的代价是"人去找 → 验证 → 改配置 → 部署"，**恢复时间常数无界**。
//
// 自节流：dev-fleet 每 5 分钟拉起一次本命令，真正该跑的节奏是天级。
// 用清单 mtime 决定跑不跑，比改调度器简单，且不会随时间漂。
func runHarvest(cfg *config.Config, configPath string, ttl time.Duration, max int, delay time.Duration) int {
	dir := filepath.Dir(configPath)
	invPath := filepath.Join(dir, config.AutoFile)

	if st, err := os.Stat(invPath); err == nil && ttl > 0 && time.Since(st.ModTime()) < ttl {
		log.Printf("纳新：清单 %.1fh 前生成，未到 TTL(%s)，跳过", time.Since(st.ModTime()).Hours(), ttl)
		return 0
	}

	// 纳新要真调上游，必须有凭据。给足预算：45 个 vault 指针 + 若干次实测。
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	rep := cfg.ResolveKeys(ctx, nil)
	log.Printf("纳新：keys %d resolved, %d unresolved", rep.Resolved, rep.Unresolved)

	client := provider.UpstreamClient()

	// **清单是累积的，不是每轮重建的。**
	//
	// 血账（2026-09-20 第二轮纳新实测）：上一轮验过的 agnes-3.0-flash，
	// 这一轮因为已并进 config 而被当作 Known 跳过 ⇒ `verified` 落空 ⇒ 覆盖写之后
	// 它从清单里消失 ⇒ 下次加载就不再并进 config ⇒ **池子逐轮自我侵蚀**。
	// 那正好是"源源不断"的反面。
	//
	// 于是：先继承旧清单，再把本轮新验过的并进去。淘汰不在这里做——
	// 它在**运行时**由模型轮换承担（上游撤了模型就回 404，路由自动换下一个），
	// 比离线定时复核更及时、也不需要额外的机制。
	inv := &config.AutoInventory{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Providers:   map[string]config.AutoProvider{},
	}
	prev, err := config.LoadAuto(dir)
	if err != nil {
		log.Printf("纳新：旧清单不可读（将重建，历史纳新结果会丢）：%v", err)
	}
	if prev != nil {
		for id, ap := range prev.Providers {
			inv.Providers[id] = ap
		}
		log.Printf("纳新：继承旧清单 %d 个 provider 的已验模型", len(prev.Providers))
	}
	fetched, failed := 0, 0
	// 同一个上游会被拆成多个 provider 条目（agnes 与 agnes-anthropic 只是协议格式不同，
	// 上游是同一台、目录也是同一份）。按**目录端点**去重，别把同一批模型实测两遍——
	// 免费额度是按账号限流的，重复实测是纯浪费。
	byURL := map[string]config.AutoProvider{}

	for i := range cfg.Providers {
		p := cfg.Providers[i]
		key := firstRealKey(p)
		if key == "" {
			log.Printf("纳新：%s 无可用凭据，跳过", p.ID)
			failed++
			continue
		}
		if ap, ok := byURL[catalog.ListURL(p.BaseURL, p.Format)]; ok {
			log.Printf("纳新：%s 与先前 provider 共用同一上游目录，复用结果", p.ID)
			inv.Providers[p.ID] = ap
			fetched++
			continue
		}
		known := make([]string, 0, len(p.Models))
		for _, m := range p.Models {
			known = append(known, m.ID)
		}
		h := catalog.Harvester{Client: client, Provider: p, APIKey: key,
			Known: known, Max: max, Delay: delay}
		res, err := h.Run(ctx)
		if err != nil {
			log.Printf("纳新：%s 失败：%v", p.ID, err)
			failed++
			continue
		}
		fetched++
		log.Printf("纳新：%s —— 新增实测通过 %d，实测失败 %d，已在库 %d，超上限未测 %d",
			p.ID, len(res.Verified), len(res.Rejected), res.Skipped, res.Truncated)
		for _, r := range res.Rejected {
			log.Printf("纳新：%s 实测未通过 %s：%s", p.ID, r.ID, r.Err)
		}
		// 继承该 provider 已有的已验模型，只做加法（见上面"清单是累积的"）。
		ap := inv.Providers[p.ID]
		have := make(map[string]bool, len(ap.Verified))
		for _, m := range ap.Verified {
			have[m.ID] = true
		}
		// 元数据刷新：已在库的模型也把窗口/输出上限更新一遍（同一次目录请求里白拿的）。
		// 不刷的话，那些在"支持声明窗口"之前纳进来的模型会永远没有窗口——
		// 而窗口是 `/v1/models` 对外声明 `max_input_tokens` 的唯一来源。
		refreshed := 0
		for _, mi := range res.Refreshed {
			for i := range ap.Verified {
				if ap.Verified[i].ID != mi.ID {
					continue
				}
				if ap.Verified[i].ContextLength != mi.ContextLength ||
					ap.Verified[i].MaxOutputTokens != mi.MaxOutputTokens {
					ap.Verified[i].ContextLength = mi.ContextLength
					ap.Verified[i].MaxOutputTokens = mi.MaxOutputTokens
					refreshed++
				}
			}
		}
		if refreshed > 0 {
			log.Printf("纳新：%s 刷新了 %d 条已有模型的窗口信息", p.ID, refreshed)
		}
		for _, mi := range res.Verified {
			if have[mi.ID] {
				continue
			}
			// kinds 只给 general：实测能证明的只有"它活着"，证明不了"它会推理"。
			// 升格必须走人工实测 + 写进手写的 config.json（见 config.MergeAuto）。
			//
			// 窗口照抄上游自愿给的（agnes 不给 ⇒ 留 0，绝不猜）；它对外声明为
			// `max_input_tokens`，是"客户端在超限前压缩"的前提。
			ap.Verified = append(ap.Verified, config.Model{
				ID: mi.ID, Name: mi.ID, Kinds: []string{"general"},
				ContextLength:   mi.ContextLength,
				MaxOutputTokens: mi.MaxOutputTokens,
			})
			have[mi.ID] = true
		}
		// 拒绝列表是本轮快照，整体替换——它回答的是"这一轮上游长什么样"。
		ap.Rejected = nil
		for _, r := range res.Rejected {
			ap.Rejected = append(ap.Rejected, struct {
				ID  string `json:"id"`
				Err string `json:"err"`
			}{ID: r.ID, Err: r.Err})
		}
		inv.Providers[p.ID] = ap
		byURL[catalog.ListURL(p.BaseURL, p.Format)] = ap
	}

	// 一个都没拉成 ⇒ **不覆盖**已有清单。把"上游全连不上"写成"清单是空的"，
	// 下一次部署就会把整个自动库存抹掉——那比纳新失败糟得多。
	if fetched == 0 {
		log.Printf("纳新：%d 个 provider 全部拉取失败，保留原有清单不动", failed)
		return 1
	}

	out, err := json.MarshalIndent(inv, "", "  ")
	if err != nil {
		log.Printf("纳新：序列化失败：%v", err)
		return 1
	}
	if err := os.WriteFile(invPath, append(out, '\n'), 0o644); err != nil {
		log.Printf("纳新：写 %s 失败：%v", invPath, err)
		return 1
	}
	total := 0
	for _, ap := range inv.Providers {
		total += len(ap.Verified)
	}
	log.Printf("纳新完成：%d 个 provider 成功、%d 个失败，写入 %s（%d 个模型）",
		fetched, failed, invPath, total)
	return 0
}

// firstRealKey 取 provider 的第一把**已解析**的凭据（占位符不算）。
func firstRealKey(p config.Provider) string {
	for _, k := range p.Keys {
		if !config.IsPlaceholder(k) && strings.TrimSpace(k) != "" {
			return strings.TrimSpace(k)
		}
	}
	return ""
}

// runCoverageCheck 部署前闸：断言「需求面 ⊆ 供给面」，有致命缺口则非零退出。
//
// 为什么闸在**部署前**、而不是启动时拒绝启动：静态性质的正确强制点在构建/部署，
// 不在运行时。运行时拒绝启动会把"配置缺一格"升级成"服务起不来"——那比缺口本身更糟，
// 而且会导致"配置有问题时连紧急修复都部署不了"（把一个可诊断的缺口换成一次停机）。
//
// 判据（为什么这能覆盖一整类缺陷）：需求面（DemandKinds × Format）是有限小集合，
// 供给面只依赖 config（静态，见 T-015 的不变量），所以这个检查是**穷举**的——
// 不是"多抓几个 bug"，而是"这一类不再可能"。
func runCoverageCheck(cfg *config.Config) int {
	fatal, warn := config.CheckCoverage(cfg)
	for _, g := range fatal {
		log.Printf("FATAL 供给面缺口 —— %s", g)
	}
	for _, g := range warn {
		log.Printf("WARN  供给面告警 —— %s", g)
	}
	if len(fatal) > 0 {
		log.Printf("供给面检查**不通过**：%d 处致命缺口。这类请求会直接 503，先补配置再部署。", len(fatal))
		return 1
	}
	log.Printf("供给面检查通过：需求面全覆盖（%d 处告警，未阻塞）", len(warn))
	return 0
}

// runStatusPublisher 定期把 /health 快照 + 当前层发布到 NATS（T-005）。
// 30s 节奏与 state 探测周期对齐；NATS 故障时 Publish 静默降级，绝不影响路由。
func runStatusPublisher(ctx context.Context, pub *nats.Publisher, r *router.Router, st *state.State, w *metrics.Window) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			pub.Publish(nats.SubjectStatus, statusSnapshot(r, st, w))
		}
	}
}

// statusSnapshot 把 router 池状态 + 当前层 + 服务质量组装成 NATS status payload。
func statusSnapshot(r *router.Router, st *state.State, w *metrics.Window) nats.StatusPayload {
	pls := make([]nats.Pool, 0, len(r.Health()))
	for _, h := range r.Health() {
		pls = append(pls, nats.Pool{
			Provider:   h.Provider,
			Format:     h.Format,
			Tier:       h.Tier,
			Keys:       h.Keys,
			Unresolved: h.Unresolved,
			Cooling:    h.Cooling,
			BackedOff:  h.BackedOff,
		})
	}
	return nats.StatusPayload{
		At:      time.Now(),
		Tier:    tierName(st.Tier()),
		Pools:   pls,
		Metrics: w.Snapshot(),
	}
}

// tierName 把 state.Tier 枚举转成 payload 字符串（契约稳定值：free/paid）。
func tierName(t state.Tier) string {
	if t == state.TierFree {
		return "free"
	}
	return "paid"
}

// shutdown 优雅关闭（带超时兜底）。
func shutdown(s *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
