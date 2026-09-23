// Package proxy 协议端点层（T-003）：把 router 接到 :8462。
// 薄层——剥协议壳 + 调 router.Route + 透传响应，不夹业务逻辑。
// 端点：
//
//	/v1/messages          → anthropic（Claude Code）
//	/v1/chat/completions  → openai
//	/v1/models            → 模型列表（?format=openai|anthropic）
//	/health               → 各 provider 池状态
package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"moretoken/internal/config"
	"moretoken/internal/metrics"
	"moretoken/internal/provider"
	"moretoken/internal/router"
)

// MaxBodyBytes 请求体上限（默认 32 MiB）。
//
// 旧的 1<<20 会在长会话中途**静默截断**：io.LimitReader 超限不报错、只是少读，
// 于是半个 JSON 被转发上游，上游回 `unexpected end of JSON input`，网关原样透传——
// 错误指向"调用方发了坏 JSON"，真凶（网关截断）在日志里一个字都没有。
// 换算是约 22 万 token 就撞墙，而 agnes 上下文 512K，长会话必然踩上。
//
// 32 MiB 远超 512K 上下文的实际体积（约 2–3 MiB），并把超限变成**显式 413**。
const MaxBodyBytes = 32 << 20

// Server HTTP 端点。
type Server struct {
	r      *router.Router
	cfg    *config.Config
	declog *DecisionLog

	// mwin 对外服务质量指标（nil = 不计）。数据全部来自**真实请求**，零新增探测。
	mwin *metrics.Window
	// escalateAfter 自愈承诺的超时上限（/doctor 用它判升级）。<=0 → metrics 的默认值。
	escalateAfter time.Duration

	// maxBody 请求体上限（NewServer 默认 MaxBodyBytes；测试可调小）。
	maxBody int64

	// 成功请求"值得留痕"的两条线（NewServer 设默认；测试可调小）。
	slowThreshold  time.Duration
	largeThreshold int
}

// SlowRequestThreshold / LargeBodyThreshold 成功路径留痕的两条线。
//
// 旧的头超时是常数 90s，60s 是"会/可能被它杀掉"那个区间的入口；
// 512KB 是实测里出现失败的那些 body（0.7–1.7MB）的下沿附近。
const (
	SlowRequestThreshold = 60 * time.Second
	LargeBodyThreshold   = 512 << 10
)

// noteIfNoteworthy 只在超过阈值时给成功条目补 elapsed / body。
//
// 目的单一：**让"修复生效"可观察**。目前无法主动复现那次失败
// （真实失败要"大 body + 上游繁忙"两个条件同时成立，而后者不可控），
// 所以只能等它自然发生——而如果成功路径什么都不记，
// "没再出现 503" 就分不清是**修好了**还是**没触发**。
func (s *Server) noteIfNoteworthy(elapsed time.Duration, bodyBytes int, e *DecisionEntry) {
	if s.slowThreshold > 0 && elapsed > s.slowThreshold {
		e.ElapsedMs = int(elapsed.Milliseconds())
	}
	if s.largeThreshold > 0 && bodyBytes > s.largeThreshold {
		e.BodyBytes = bodyBytes
	}
}

// NewServer 建端点。cfg 供 /v1/models、/health 读取 provider 元数据。
func NewServer(r *router.Router, cfg *config.Config, log *DecisionLog) *Server {
	return &Server{
		r: r, cfg: cfg, declog: log, maxBody: MaxBodyBytes,
		slowThreshold: SlowRequestThreshold, largeThreshold: LargeBodyThreshold,
	}
}

// WithMetrics 挂上指标窗口与升级阈值（可链式）。不挂 = 不计指标、/doctor 恒报 ok，
// 行为与以前完全一致。
func (s *Server) WithMetrics(w *metrics.Window, escalateAfter time.Duration) *Server {
	s.mwin = w
	s.escalateAfter = escalateAfter
	return s
}

// doctor 「自愈承诺超时」的判定端点（升级判据，非存活判据）。
//
// **为什么不并进 /health**：/health 是**存活**探针，dev-fleet 拿它判"要不要重启"。
// 而"自愈超时"要的是**升级**（叫人/叫 agent），不是重启。若并进 /health：
//
//	降级太久 → 判不健康 → dev-fleet 重启 → 指标窗口被清空 → 又"健康" → 下次再降级再重启
//
// 那会把"自愈失效"变成一台重启永动机，且每次重启都丢掉现场。
// 所以它是**独立的、不被 dev-fleet 用来拉起服务**的通道（cli 探针读它，只报告不重启）。
func (s *Server) doctor(w http.ResponseWriter, _ *http.Request) {
	snap := s.mwin.Snapshot() // nil 安全：没挂指标时是零值
	esc, reason := metrics.Escalate(snap, s.escalateAfter)

	w.Header().Set("Content-Type", "application/json")
	if esc {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "escalate", "reason": reason, "metrics": snap,
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "metrics": snap})
}

// contextMessage 从 router 的错误里抠出**客户端认的那句**。
//
// router 在外面包了一层哨兵（"context window exceeded: "），而客户端只认
// `prompt is too long: N tokens > M` 这个句式。正则匹配的是**子串**、多一层前缀不影响，
// 但错误信息是给人看的，抠干净免得带噪音。
func contextMessage(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if i := strings.Index(s, "prompt is too long"); i >= 0 {
		return s[i:]
	}
	return s
}

// Handler 返回路由表。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", s.chatHandler(config.FormatAnthropic))
	mux.HandleFunc("/v1/chat/completions", s.chatHandler(config.FormatOpenAI))
	mux.HandleFunc("/v1/models", s.models)
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/doctor", s.doctor)
	return mux
}

// chatHandler 处理一次聊天请求：读 body → 判断流式 → router.Route → 透传。
func (s *Server) chatHandler(format config.Format) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		// kindLabel 归一化后进指标维度：虚拟模型用它的 kind，**字面模型统一记 literal**。
		// 不把模型名放进维度——上游模型号有上百个，放进去维度就无限膨胀了。
		kindLabel := string(format) + "/literal"
		status := 0
		// 一次请求的结局只在这里记一次：**从真实流量算指标，不另发探针**。
		// 刻意不包装 ResponseWriter 去截状态码——那会破坏 w.(http.Flusher) 断言，
		// 把流式路径打回缓冲（SSE 会变成一次性吐出）。显式赋值麻烦一点，但安全。
		defer func() {
			if s.mwin == nil {
				return
			}
			s.mwin.Observe(time.Now(), kindLabel, status >= 200 && status < 300, time.Since(start))
		}()

		if req.Method != http.MethodPost {
			status = http.StatusMethodNotAllowed
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		limit := s.maxBody
		if limit <= 0 {
			limit = MaxBodyBytes
		}
		// 多读 1 字节：LimitReader 到限不报错、只是少读，无法区分"刚好到限"与"超限"。
		// 旧实现正是因此把超限请求砍成半个 JSON 再转发，让上游回一个指向调用方的
		// `unexpected end of JSON input`——真凶（网关静默截断）完全不可见。
		body, err := io.ReadAll(io.LimitReader(req.Body, limit+1))
		if err != nil {
			status = http.StatusBadRequest
			http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
			return
		}
		if int64(len(body)) > limit {
			status = http.StatusRequestEntityTooLarge
			http.Error(w, fmt.Sprintf(
				`{"error":"request body too large","limit_bytes":%d}`, limit),
				http.StatusRequestEntityTooLarge)
			return
		}

		// 一次解析取两件事：是否流式（两协议一致，看 "stream":true）+ model 是否虚拟声明。
		kind := provider.NonStreaming
		var probe struct {
			Stream *bool  `json:"stream"`
			Model  string `json:"model"`
		}
		_ = json.Unmarshal(body, &probe) // 解析失败时按"非流式 + 字面模型"处理，交给下游报错
		if probe.Stream != nil && *probe.Stream {
			kind = provider.Streaming
		}
		wantKind, virtual := router.ParseVirtual(probe.Model)
		if virtual {
			// ⚠️ 标签用**单独的变量**算。绝不能就地改 wantKind——
			// 它同时是传给路由器的参数：把 `auto`（无类型）写成 "any"，
			// 路由就会去找一个持有 kind="any" 的 provider，谁也持有不了 ⇒ 候选为空 ⇒ 503。
			// 2026-09-21 血账：这行就地赋值让 `model:"auto"` 在生产上每 ~4 分钟失败一次、
			// 持续两小时（23 条 503），而聚合原因只报 `no ... offers kind "any"`，
			// 看不出是网关自己造的。
			label := wantKind
			if label == "" {
				label = "any" // 指标维度里的显示名，仅此而已
			}
			kindLabel = string(format) + "/" + label
		}

		stream := kind == provider.Streaming
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}

		res, ferr := s.r.Route(req.Context(), router.RouteRequest{
			Format:       format,
			Body:         body,
			Kind:         kind,
			VirtualModel: virtual,
			WantKind:     wantKind,
			StreamCB: func(chunk []byte) {
				if stream {
					// 原样透传 SSE 数据块（provider 已剥 data: 前缀；这里补回线格式）。
					fmt.Fprintf(w, "data: %s\n\n", chunk)
					if fl, ok := w.(http.Flusher); ok {
						fl.Flush()
					}
				}
			},
		})
		if ferr != nil {
			// **上下文超限**要单独说：它是**调用方**的问题（送太多），不是服务挂了。
			// 返回 503 会让智能体以为"服务不可用"从而重试——而这是个确定性失败，
			// 重试只是白等（实测每个候选 60–80 秒）。413 + 可操作提示才是对的。
			if errors.Is(ferr, router.ErrModelNotFound) {
				// **Anthropic 的规范形状**：`404 not_found_error` + message 以 `model: ` 开头。
				//
				// Claude Code 的映射是写死的（从它的二进制里挖出来）：
				//   `not_found_error` + message 前缀 `model: `  →  `model_not_found`
				//   → 显示「模型可能不存在或你没有权限，运行 /model 换一个」
				// **这句话人可以直接照做** —— 于是"会话卡死关停"变成"改一行模型名"。
				//
				// 不能回 503：那是"服务端暂时问题，稍后重试"，会诱导客户端重试一个
				// 永远不可能成功的请求。2026-09-22 实测：5 个子会话被指定了
				// `claude-opus-4-8[1m]`（池里没有 opus），就是这么卡死关停的。
				status = http.StatusNotFound
				msg := fmt.Sprintf(`{"type":"error","error":{"type":"not_found_error","message":%q}}`,
					"model: "+probe.Model)
				if s.declog != nil {
					s.declog.Record(DecisionEntry{
						ProviderID: "router", Reason: ferr.Error(),
						Status: http.StatusNotFound,
						Want:   probe.Model, Attempts: res.Attempts,
					})
				}
				http.Error(w, msg, http.StatusNotFound)
				return
			}
			if errors.Is(ferr, router.ErrContextTooLong) {
				// **回 Anthropic 的规范形状**，不自造措辞。
				//
				// 客户端（Claude Code）里那条判据是写死的正则：
				//   /prompt is too long[^0-9]*(\d+)\s*tokens?\s*>\s*(\d+)/i
				// 认出它会提取 actual/limit 两个数、归到 "request too large"、
				// **提示 /compact 而不是重试**。
				//
				// 为什么不能回 503：503 的字面含义是"服务端暂时问题，稍后重试"——
				// **这个信号本身就在诱导重试**，而超限是个永远不会成功的请求。
				// 2026-09-22 那个会话就是这么卡住的：13 分钟 8 次，每次 70 秒。
				status = http.StatusBadRequest
				msg := fmt.Sprintf(`{"type":"error","error":{"type":"invalid_request_error","message":%q}}`,
					contextMessage(ferr))
				if s.declog != nil {
					s.declog.Record(DecisionEntry{
						ProviderID: "router", Reason: ferr.Error(),
						Status: http.StatusBadRequest,
						Want:   probe.Model, Attempts: res.Attempts,
					})
				}
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
			// 无可用候选 / 全池耗尽 → 503（I2 的空兜底语义）。
			// 虚拟模型要的类型无人能提供时，错误体带上原因，免得调用方只看到"全挂了"。
			msg := `{"error":"all providers exhausted"}`
			if virtual {
				msg = fmt.Sprintf(`{"error":"no provider available for model %q","detail":%q}`,
					probe.Model, ferr.Error())
			}
			// **这条路径必须留痕**：它是"没有响应"的那一类结果，请求方日志里只剩一句 503。
			// 不记的话，事故现场在 /decisions 里就是一段**空白**——2026-09-20
			// 15:31:49–15:34:49 正是如此，全靠翻档前后恰好有流量才反推出机制。
			//
			// 此前这里写着"决策日志已留痕"，但代码在 Record 之前就 return 了：
			// 注释承诺的调用点，要在代码里找得到（本仓已栽过一次，见 TROUBLESHOOTING §2）。
			if s.declog != nil {
				s.declog.Record(DecisionEntry{
					ProviderID: "router", // 没有任何 provider 服务成功——决策在路由层就终止了
					Reason:     ferr.Error(),
					Status:     http.StatusServiceUnavailable,
					Want:       probe.Model, // 请求的 model 原文，便于反查"是什么被拒了"
					// 逐个候选的失败原因：这是"是谁坏的、怎么坏的"的唯一载体。
					Attempts: res.Attempts,
				})
			}
			status = http.StatusServiceUnavailable
			http.Error(w, msg, http.StatusServiceUnavailable)
			return
		}
		if s.declog != nil {
			e := routerEntry(res)
			if virtual {
				e.Model, e.Want = res.Model, probe.Model
			}
			// 成功也要能证明"修复生效"：超阈值的慢/大请求留痕（见 noteIfNoteworthy）。
			s.noteIfNoteworthy(time.Since(start), len(body), &e)
			s.declog.Record(e)
		}
		status = res.StatusCode
		if stream {
			return // 流已在 StreamCB 里逐块写完
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.StatusCode)
		w.Write(res.Body)
	}
}

// models 列模型（?format= 过滤；缺省全量）。
//
// 除具体模型外，一并列出**虚拟模型**（T-010）：客户端发给 model 字段的是
// "auto" / "auto:<kind>"，网关按类型挑一个具体模型。列出来供客户端发现可用类型。
func (s *Server) models(w http.ResponseWriter, req *http.Request) {
	format := config.Format(req.URL.Query().Get("format"))
	type m struct {
		ID       string        `json:"id"`
		Name     string        `json:"name"`
		Provider string        `json:"provider"`
		Format   config.Format `json:"format"`
		Tier     config.Tier   `json:"tier"`
		Kinds    []string      `json:"kinds,omitempty"`
		Virtual  bool          `json:"virtual,omitempty"`

		// 客户端**认这两个名字**（从 Claude Code 二进制里挖到的 Anthropic API 文档）：
		//
		//	Each model object has `id`, `display_name`, `created_at`, and — since Mar 2026 —
		//	**`max_input_tokens`** (the context window), `max_tokens` (the output cap),
		//	and `capabilities`. **There is no `context_window` field.**
		//
		// 声明窗口是"无感"的正解：客户端据此**在超限前**压缩，
		// 于是**根本不会产生超限请求**——而不是等撞墙了再翻译错误。
		DisplayName string `json:"display_name,omitempty"`
		// MaxInputTokens 上下文窗口（0/缺省 = 未知，客户端退回它自己的默认假设）。
		MaxInputTokens int `json:"max_input_tokens,omitempty"`
		MaxTokens      int `json:"max_tokens,omitempty"`
	}
	list := []m{}
	kinds := map[string]bool{}
	for _, p := range s.cfg.Providers {
		if format != "" && p.Format != format {
			continue
		}
		for _, mm := range p.Models {
			list = append(list, m{
				ID: mm.ID, Name: mm.Name, Provider: p.ID,
				Format: p.Format, Tier: p.Tier, Kinds: mm.Kinds,
				DisplayName: mm.Name,
				// 窗口对外声明：客户端据此**在超限前**压缩。
				MaxInputTokens: mm.ContextLength,
				MaxTokens:      mm.MaxOutputTokens,
			})
			for _, k := range mm.Kinds {
				kinds[k] = true
			}
		}
	}
	// 虚拟模型置顶：智能体只需认这几个名字，具体模型随健康/免费层变化。
	//
	// 窗口取候选里**最小的那个**——客户端据此决定何时压缩，min 才是"我能保证的下限"。
	// 取首选（或取 max）会把"我可能被换到更小的模型"藏起来，客户端压得不够早，
	// 于是又撞回那个刚花大力气翻译的超限错误。
	virtual := []m{{
		ID: router.VirtualName, Name: "Auto (any kind)", Virtual: true,
		DisplayName:    "Auto (any kind)",
		MaxInputTokens: minWindow(s.cfg, format, ""),
		MaxTokens:      minOutTokens(s.cfg, format, ""),
	}}
	for _, k := range sortedKeys(kinds) {
		virtual = append(virtual, m{
			ID: router.VirtualName + ":" + k, Name: "Auto (" + k + ")", Virtual: true,
			DisplayName:    "Auto (" + k + ")",
			MaxInputTokens: minWindow(s.cfg, format, k),
			MaxTokens:      minOutTokens(s.cfg, format, k),
		})
	}
	list = append(virtual, list...)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"data": list, "format": format})
}

// minWindow / minOutTokens 虚拟模型的窗口 = 候选里**最小的那个**（未知的不计入）。
//
// 为什么取 min：客户端拿这个数决定**何时压缩**。取 min 是"我能保证的下限"；
// 取首选或 max 会把"我可能被换到更小的模型"藏起来，客户端压得不够早。
// 全部未知时返回 0（字段缺省），客户端退回它自己的默认假设——**不猜一个数出来**。
func minWindow(cfg *config.Config, format config.Format, kind string) int {
	return minOverModels(cfg, format, kind, func(m config.Model) int { return m.ContextLength })
}

func minOutTokens(cfg *config.Config, format config.Format, kind string) int {
	return minOverModels(cfg, format, kind, func(m config.Model) int { return m.MaxOutputTokens })
}

func minOverModels(cfg *config.Config, format config.Format, kind string, pick func(config.Model) int) int {
	best := 0
	for _, p := range cfg.Providers {
		if format != "" && p.Format != format {
			continue
		}
		if kind != "" && !p.HasKind(kind) {
			continue
		}
		for _, mm := range p.Models {
			if kind != "" && !modelHasKind(mm, kind) {
				continue
			}
			v := pick(mm)
			if v <= 0 {
				continue // 未知不计入——不拿"不知道"去压低下限
			}
			if best == 0 || v < best {
				best = v
			}
		}
	}
	return best
}

func modelHasKind(m config.Model, kind string) bool {
	for _, k := range m.Kinds {
		if strings.EqualFold(k, kind) {
			return true
		}
	}
	return false
}

// sortedKeys 稳定输出 kind 顺序（免得 /v1/models 每次顺序都变）。
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// health 各 provider 池状态（冷却 key 数 / 总 key 数 / 退避）。
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.r.Health())
}

// routerEntry 把 router.Result 转成决策日志条目（密钥安全：只记 providerID + retried）。
func routerEntry(res router.Result) DecisionEntry {
	return DecisionEntry{
		ProviderID: res.ProviderID,
		Retried:    res.Retried,
		Reason:     res.Fail.Reason,
		Status:     res.StatusCode,
	}
}
