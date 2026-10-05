// MoreToken —— 免费优先的会话保持型路由网关（:8462）。
//
// 组装：config → router → proxy 端点。纯标准库单二进制，零外部依赖。
// 定位与不变量见 docs/project/GOALS.md 与 docs/decisions/ADR-003。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
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

	"moretoken/internal/admission"
	"moretoken/internal/auth"
	"moretoken/internal/catalog"
	"moretoken/internal/config"
	"moretoken/internal/metrics"
	"moretoken/internal/nats"
	"moretoken/internal/probe"
	"moretoken/internal/provider"
	"moretoken/internal/proxy"
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
	var platforms bool
	flag.BoolVar(&platforms, "platforms", false, "平台发现：注册表 × vault × config 三向 diff，列出「我们还缺哪些平台」（只读，不接线）")
	var doctorURL string
	flag.StringVar(&doctorURL, "doctor-url", "http://127.0.0.1:8462/doctor", "（客户端模式）/doctor 地址")

	// ---- Token 管理子命令（T-024：发放面，做完即退，不启动服务）----
	var tokenGen, tokenList, tokenRevoke, tokenRotate bool
	flag.BoolVar(&tokenGen, "token-gen", false, "生成入站 token（明文直入 vault，不回显；-token-plain 例外）后退出")
	flag.BoolVar(&tokenList, "token-list", false, "列出已登记 token（状态/过期/最后使用）后退出")
	flag.BoolVar(&tokenRevoke, "token-revoke", false, "吊销 -token-name 指定的 token（网关热加载，下一请求即拒）后退出")
	flag.BoolVar(&tokenRotate, "token-rotate", false, "轮换 -token-name 指定的 token（新值入墙、指针不变、旧值即废）后退出")
	var tokenName string
	flag.StringVar(&tokenName, "token-name", "", "token 名（每客户端一个；gen 必填，revoke/rotate 指定目标）")
	var tokenTTL time.Duration
	flag.DurationVar(&tokenTTL, "token-ttl", auth.DefaultTTL, "token 有效期（默认 30 天；0 = 永不过期，须显式选择）")
	var tokenPlain bool
	flag.BoolVar(&tokenPlain, "token-plain", false, "无 vault 模式：明文回显一次、不入墙（自担保管；默认禁止）")
	var tokensFile string
	flag.StringVar(&tokensFile, "tokens-file", "", "tokens.json 路径（默认 <config 同目录>/tokens.json）")
	var materializeConfig string
	flag.StringVar(&materializeConfig, "materialize-config", "",
		"（宿主部署工具）解析 config 里全部 env:/vault: 指针后输出**完整明文 config** 到 <path>（`-`=stdout），然后退出。"+
			"供 Docker 部署：容器内没有 vault.exe，解析必须发生在宿主（T-026）。输出物含全部密钥明文——只许进命名卷/管道，严禁入 git")
	flag.Parse()

	if doctor {
		os.Exit(runDoctorClient(doctorURL))
	}

	// materialize 在 config.Load 之后、其他一切之前分发（它自己 Load，不共享后续流程）。
	if materializeConfig != "" {
		os.Exit(runMaterializeConfig(configPath, materializeConfig))
	}

	// token 子命令在 config.Load **之前**分发：发放面不依赖 provider 配置是否完好
	// （config 坏了也要能吊销 token——那可能正是止损操作）。
	if n := pickTokenCommand(tokenGen, tokenList, tokenRevoke, tokenRotate); n != "" {
		os.Exit(runTokenCLI(n, tokenName, tokenTTL, tokenPlain, tokensFile, configPath))
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if check {
		os.Exit(runCoverageCheck(cfg))
	}
	if platforms {
		os.Exit(runPlatforms(cfg, configPath))
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
	//
	// ⚠️ 路由只构造一次：选项**全部**并进 opts 再 New。曾经先 New 一次再 New 一次，
	// 前一个实例被直接覆盖、注入的东西全丢 —— 这类"建了又盖"的写法必须并成一次。
	rtOpts := []router.Option{
		router.WithClient(provider.UpstreamClient()),
		// 免费层真的被试过且全挂 → 记一次观测（留痕 + 上报），**不改变路由行为**。
		router.WithFreeExhaustedSink(func() { st.AllFreeUnavailable(context.Background()) }),
	}
	// 实测能力准入选表（ADR-004 §四）：只对虚拟模型（auto:*）按维度过滤，
	// 让 auto:coding 落到"实测编程通过"的模型上、而不是通用模型。
	// 路径为空 ⇒ 完全不启用（行为与接入前一致）；文件缺失/坏 ⇒ 回退且状态**在日志与 /doctor 上可见**。
	if cfg.AdmissionPath != "" {
		adm := admission.Load(cfg.AdmissionPath)
		if cfg.AdmissionMinRatio > 0 {
			adm.SetMinRatio(cfg.AdmissionMinRatio)
		}
		// **入池**：把实测结论并进配置（新增模型 + 补齐 kinds）。
		// 与运行时的准入过滤互补 —— filter 管删，这里管补（见 config.ApplyAdmission 注释）。
		// ⚠️ 必须在 router.New 之前：router 持有的是 cfg 里已合并好的 providers。
		mr := config.ApplyAdmission(cfg, adm.Rows(), adm.MinRatio())
		if mr.Added > 0 || mr.Upgraded > 0 {
			log.Printf("admission: 实测结论已并入配置 —— 新增模型 %d 个、补齐 kinds %d 处", mr.Added, mr.Upgraded)
		}
		// ⚠️ 必须有这一段：有实测证据、却没有 provider 能接住的模型**不会有任何报错**。
		// 不报出来的话，`新增 0` 就无法区分"表里没有合格模型"与"全都没匹配上 base_url"——
		// 排查会一头扎向配额，而真因是配置里没有那个上游。
		if len(mr.Unmatched) > 0 {
			log.Printf("admission: ⚠️ %d 个模型实测通过但**没有任何 provider 的 base_url 与之匹配**，"+
				"进不了池（检查该上游是否已在 config 的 providers 里）：%v",
				len(mr.Unmatched), mr.Unmatched)
		}
		rtOpts = append(rtOpts, router.WithAdmission(adm))
		n, r, c, aerr := adm.Stats()
		if adm.Empty() {
			log.Printf("admission: 表不可用（%s）—— **不做准入过滤**，回退到 kinds；err=%s",
				cfg.AdmissionPath, aerr)
		} else {
			// 准入线一并打出来：一个"为什么 4/5 也过了"的问题，必须能从日志/doctor 自答。
			log.Printf("admission: 已加载 %d 个模型（reasoning 准入 %d / coding 准入 %d · 准入线 %.0f%%）",
				n, r, c, adm.MinRatio()*100)
		}
		// 热加载：表更新后**无需重启容器**即可生效（准入表是分钟/小时级产物，
		// 但重启容器对供给有中断代价 —— 不该让"重跑一次评估"必须赔上一次重启）。
		// ⚠️ 此前 RefreshIfChanged 写了却没接线，等于热加载只存在于函数名里（2026-10-01 自查）。
		admCtx, cancelAdm := context.WithCancel(context.Background())
		defer cancelAdm()
		go adm.Watch(admCtx, 30*time.Second)
	}
	rt = router.New(cfg, rtOpts...)
	srv := proxy.NewServer(rt, cfg, declog).WithMetrics(mwin, escalateAfter)

	// 入站鉴权（T-024）：tokens.json > config.auth_token > 不鉴权（nil = 零行为变化）。
	// tokensFile != "" 表示**显式指定**——此时文件缺失必须炸（fail-closed，T-026）：
	// 容器 CMD 写死了 -tokens-file，若挂载错位而这里静默回落"不鉴权"，
	// 就会得到一个 healthcheck 全绿的裸奔网关（评审 S3：最坏失败模式是安静的）。
	if authStore := buildAuthStore(defaultTokensFile(tokensFile, configPath), tokensFile != "", cfg); authStore != nil {
		srv = srv.WithAuth(authStore)
		log.Printf("auth: 入站 token 校验已启用（/health 豁免；吊销/轮换经热加载下一请求生效）")
	}

	// /decisions 端点：暴露最近决策留痕（密钥安全——无 key 值）。
	// 注册在外层 mux 上、不经 srv.Handler()，须单独 AuthWrap 才同样受保护（T-024：
	// 决策日志含路由情报，默认保护、宁严勿松）。
	mux := http.NewServeMux()
	mux.Handle("/", srv.Handler())
	mux.Handle("/decisions", srv.AuthWrap(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		n := 100
		if v := req.URL.Query().Get("n"); v != "" {
			if p, err := strconv.Atoi(v); err == nil && p > 0 {
				n = p
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(declog.Recent(n))
	})))

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

// pickTokenCommand 四个布尔开关 → 至多一个子命令名。
// 多个同时给 → 报错退出（flag 包不做互斥，这里补上：歧义指令必须响）。
func pickTokenCommand(gen, list, revoke, rotate bool) string {
	var picked []string
	switch {
	case gen:
		picked = append(picked, "gen")
	case list:
		picked = append(picked, "list")
	case revoke:
		picked = append(picked, "revoke")
	case rotate:
		picked = append(picked, "rotate")
	}
	// 上面的 switch-case 只会命中一个；再数一遍防未来改成 if 链时漏互斥。
	n := 0
	for _, b := range []bool{gen, list, revoke, rotate} {
		if b {
			n++
		}
	}
	if n > 1 {
		log.Fatalf("token 子命令互斥：-token-gen/-token-list/-token-revoke/-token-rotate 只能给一个")
	}
	if len(picked) == 1 {
		return picked[0]
	}
	return ""
}

// defaultTokensFile -tokens-file 缺省 = config 同目录的 tokens.json。
// CLI 与网关启动**必须**用同一函数算落点——两边指到不同文件的话，
// 吊销/轮换就"不生效"了（而且是静默不生效，最难排查的那类）。
func defaultTokensFile(tokensFile, configPath string) string {
	if tokensFile != "" {
		return tokensFile
	}
	return filepath.Join(filepath.Dir(configPath), "tokens.json")
}

// runTokenCLI 发放面子命令入口。
func runTokenCLI(cmd, name string, ttl time.Duration, plain bool, tokensFile, configPath string) int {
	tokensFile = defaultTokensFile(tokensFile, configPath)
	return auth.CLI{
		Command: cmd, Name: name, TTL: ttl, Plain: plain,
		TokensFile: tokensFile, Out: os.Stdout,
	}.Run()
}

// runMaterializeConfig 宿主侧部署工具（T-026）：把 config 里全部 env:/vault: 指针
// 解析成明文，输出**完整可独立运行**的 config JSON。
//
// 为什么必须有它：容器里没有 vault.exe（Windows 二进制、DB 在宿主），
// 而 90 把 key 全是 vault: 指针。解析只能发生在宿主——复用**已测试的** ResolveKeys
// （去重、负缓存、总预算），不在 shell 里重新发明一遍。
//
// 部署闸语义：任何一把 key 解析失败 → 非零退出，**不输出半成品**。
// 容器里无法补解析，残缺 config 上船 = 那个池静默无凭据（503），
// 而现象会指向"上游挂了"——错误方向的排查最贵。
//
// 输出物含全部密钥明文：`-`（stdout）模式供管道直投 Docker 卷（明文不落宿主磁盘）；
// 写文件模式仅限受控路径，用完即删。**任何情况下不得进 git。**
func runMaterializeConfig(configPath, out string) int {
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "materialize: load config: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultVaultTimeout)
	defer cancel()
	rep := cfg.ResolveKeys(ctx, nil)
	if rep.Unresolved > 0 {
		fmt.Fprintf(os.Stderr, "materialize: %d 把 key 解析失败，拒绝输出半成品 config：\n", rep.Unresolved)
		for _, d := range rep.Details {
			fmt.Fprintf(os.Stderr, "  - %s\n", d)
		}
		return 1
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "materialize: marshal: %v\n", err)
		return 1
	}
	raw = append(raw, '\n')

	if out == "-" {
		// stdout 模式：只写数据，任何诊断都进 stderr（管道对端拿到的是纯净 JSON）。
		// EPIPE（对端提前退出）→ 非零；Go 对 fd1 的 SIGPIPE 默认直接杀进程（退出 141），
		// 两条路都是非零，部署脚本的 pipefail 都能抓住。
		if _, err := os.Stdout.Write(raw); err != nil {
			fmt.Fprintf(os.Stderr, "materialize: write stdout: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "materialize: ok（%d 把 key 已解析，明文经 stdout 输出）\n", rep.Resolved)
		return 0
	}
	if err := os.WriteFile(out, raw, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "materialize: write %s: %v\n", out, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "materialize: ok（%d 把 key 已解析 → %s，0600；含明文，用完即删，严禁入 git）\n", rep.Resolved, out)
	return 0
}

// buildAuthStore 启动时组装校验面（T-024 优先级：tokens.json > config.auth_token > 不鉴权）。
//
// 返回 nil = 未配置鉴权，行为与 T-024 之前完全一致（不挂中间件、零开销）。
// 单 token 兼容模式只在 auth_token **已成功解析**时启用——解析失败还拿占位符
// 去校验的话，合法客户端全 401 而现象指向"客户端配错了"，那是最坏的排查体验；
// 此时宁可退回不鉴权 + ResolveKeys 已打过的 WARN（失败必须响，响过之后不再叠坑）。
//
// required=true（-tokens-file 显式给出，容器形态恒真）时文件缺失 → 启动即炸（T-026 fail-closed）：
// "显式声明了鉴权名单却找不到名单"与"从没配置过鉴权"是两种世界，前者静默放行
// 就是裸奔网关（评审 S3）。挂载错位必须在第一秒暴露，不是在全绿里潜伏。
func buildAuthStore(tokensFile string, required bool, cfg *config.Config) *auth.Store {
	store, err := auth.LoadStore(tokensFile,
		auth.WithLogger(log.Printf),
		auth.WithTouchDisabled(os.Getenv("MORETOKEN_AUTH_TOUCH") == "off"))
	if err != nil {
		log.Fatalf("load tokens %s: %v", tokensFile, err) // 声明了却坏了 → 启动即炸，不静默
	}
	if required && !store.HasEntries() {
		if _, statErr := os.Stat(tokensFile); statErr != nil {
			log.Fatalf("auth: -tokens-file 显式指定为 %s 但文件不可读（%v）——fail-closed 拒绝裸奔启动。"+
				"检查挂载/路径；确认要关闭鉴权则移除 -tokens-file 参数", tokensFile, statErr)
		}
	}
	if store.HasEntries() {
		return store
	}
	tok := cfg.AuthToken
	if tok != "" && !strings.HasPrefix(tok, config.PrefixEnv) && !strings.HasPrefix(tok, config.PrefixVault) {
		return auth.InMemoryStore([]auth.Entry{{
			Name: "auth_token", Hash: auth.Hash(tok), Created: time.Now(),
		}}, auth.WithLogger(log.Printf))
	}
	if tok != "" {
		log.Printf("WARN auth_token 未解析成功（仍是 %s 指针），本次启动**不鉴权**——见上方 unresolved 明细", tok)
	}
	return nil
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
	// 非 200/503 的一切响应（典型：T-024 之后的 401——/doctor 受鉴权保护而探针没带 token）
	// 都归"不可达/配置错"档（exit 2），**绝不回落 ok**。
	// 401 的 body 不是 doctor JSON，旧代码会解出空 Status → 走到底下的 ok 分支 → 假绿；
	// 而假绿比空白更贵（T-016 铁律）：它让"升级判据已失效"看起来像"一切健康"。
	if resp.StatusCode != http.StatusOK {
		log.Printf("doctor: 网关回了 HTTP %d（不是判据响应）——常见原因：/doctor 受 token 鉴权保护而探针未带 token，"+
			"或网关换成了别的进程。判据**未生效**，按不可达处理", resp.StatusCode)
		return 2
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
