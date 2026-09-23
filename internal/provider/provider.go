// Package provider 封装上游 HTTP 请求（FreeLLMAPI 蓝本的协议适配层）：
//   - openai 格式：POST {base}/chat/completions（base 以 /v1 结尾的 OpenAI 兼容端点）
//   - anthropic 格式：POST {base}/v1/messages（官方 Anthropic 兼容端点，零翻译直通）
//
// 两种格式路径完全隔离（ADR-003），互不串选。SSE 流式逐块透传，
// 失败分类见 FailKind——它是 T-002 重试安全边界（I7）的判据：
// 只有响应字节发出前的失败可重放到链上下一 key。
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Kind 请求种类：非流式整体等待 / 流式逐块回调。
type Kind int

const (
	NonStreaming Kind = iota
	Streaming
)

// Request 一次上游调用的全部输入（显式，无包级状态）。
// Body 是已序列化的请求体（两种格式各自的 schema），provider 不解析它——
// 只负责"发对 URL、带对 key、收对响应"。
type Request struct {
	Format   string // "openai" | "anthropic"，对应 config.Provider.Format
	BaseURL  string // config.Provider.BaseURL
	APIKey   string // pool 选定的那把 key（key 对调用方透明）
	Model    string
	Body     []byte
	Kind     Kind
	StreamCB func(chunk []byte) // Streaming 时逐块回调（SSE data 行已剥前缀）
	// Idle 响应体空闲上限（上游既不回字节也不断开 → 主动中止，见 idle.go）。
	// 0 表示用 StreamIdle 默认值。
	Idle time.Duration

	// HeaderBudget 响应头阶段的时间预算。0 → DefaultHeaderBudget。
	//
	// 调用方应按**请求体大小**给出（HeaderBudgetFor）——TTFT 是上下文的函数，
	// 拿一个常数去卡所有请求，就会把大上下文的合法长等待误判成"上游挂死"。
	HeaderBudget time.Duration
}

// Result 一次调用的结果。StatusCode 为 0 表示没收到状态码（连接错误）。
type Result struct {
	StatusCode int
	Body       []byte // 非流式：完整响应体；流式：空（数据走 StreamCB）
}

// FailKind 失败分类（重试安全边界的判据，I7）。
type FailKind int

const (
	FailNone      FailKind = iota // 成功（非 2xx 但响应完整收完也算，错误体在 Result.Body）
	FailTransport                 // 连接错误 / 超时（响应字节前）→ 可重放
	FailRateLimit                 // 429（响应字节前）→ 可重放 + 该 key 冷却
	FailUpstream                  // 5xx（响应字节前）→ 可重放
	FailAuth                      // 401：凭据无效 → 隔离该 key，换池内下一把
	FailStreamMid                 // 流已发字节后中断 → 不可重放（tool-call 不重复）
	// FailDenied 403：请求被拒，但**与凭据无关**——通常是"这个账号对这个模型没权限"。
	// 按**模型级**处理（同 provider 换下一个模型），**不动任何 key**。见 Invoke 里的判据。
	//
	// 实测（2026-09-20，agnes 与 xkiro 两个上游结果一致）：
	//
	//	好 key + 好模型      → 200
	//	好 key + **无权模型** → 403
	//	**垃圾 key** + 好模型 → 401
	//
	// 403 曾与 401 同归 FailAuth，于是路由对它也 RecordDead（长禁该 key 1 小时）——
	// 一个无权模型进了 config 就会把该 provider 的 key 逐个禁光、整层下线一小时
	//（33 把 key × 1h，而正确的代价是 0 把 key × 60s）。
	FailDenied

	// FailContextTooLong **确定性**失败：请求超出上游的上下文上限。
	//
	// 所有候选、所有 key 都会给同一个答案 ⇒ **重试毫无意义**，只会让调用方白等
	//（实测 agnes 要先扛 60–80 秒才回，6 个候选就是几分钟）。
	// 2026-09-22 实测现场：某会话上下文 994166 token，而上限 524288 —— 近 2 倍，
	// 它一直重发同一个请求，看起来就是"卡了很久"，而且会永远卡下去。
	//
	// 判据**看 body 不看状态码**：同一件事 agnes 直连回 400、经它的网关回 502。
	FailContextTooLong

	// FailModelNotFound 上游**没有这个模型**——同样是**确定性**失败。
	//
	// 所有候选都会给同一个答案（模型名不在任何一家的目录里），重试毫无意义。
	// 而它必须被翻译成客户端认得的样子：Claude Code 的映射是
	// `not_found_error` + message 以 `model: ` 开头 → `model_not_found`
	// → 显示"模型可能不存在或你没有权限，运行 /model 换一个"——**人可以照做**。
	//
	// 2026-09-22 实测：某会话的 5 个子会话被指定了 `claude-opus-4-8[1m]`（池里没有 opus），
	// 网关回 503（"服务端暂时问题，稍后重试"）⇒ 客户端重试 ⇒ 会话卡死关停。
	FailModelNotFound
)

// String 失败类别的稳定短名（进决策日志，供事后按类别聚合）。
//
// 取值是**契约**：改了它，基于历史留痕做的统计就断了。加新类别可以，改旧名字不行。
func (k FailKind) String() string {
	switch k {
	case FailNone:
		return "none"
	case FailTransport:
		return "transport"
	case FailRateLimit:
		return "ratelimit"
	case FailUpstream:
		return "upstream"
	case FailAuth:
		return "auth"
	case FailStreamMid:
		return "stream_mid"
	case FailDenied:
		return "denied"
	case FailContextTooLong:
		return "context_too_long"
	case FailModelNotFound:
		return "model_not_found"
	}
	return "unknown"
}

// modelNotFoundMarkers 上游"没有这个模型"的识别特征（小写匹配）。
//
// 收的是**够具体**的片段。误判的代价不对称：把"上游故障"误判成"模型不存在"，
// 会让一个本该重试掉的失败变成"换模型吧"（用户白改配置）；
// 反过来则是继续重试一个注定的失败。两边都不好，所以宁可窄。
var modelNotFoundMarkers = []string{
	"model_not_found",
	"model not found",
	"does not exist",
	"unknown model",
	"invalid model",
	"no such model",
}

// isModelNotFound 这个错误体是不是"上游没有这个模型"。
func isModelNotFound(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	s := strings.ToLower(string(body))
	for _, m := range modelNotFoundMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// contextMarkers 上下文超限的识别特征（小写匹配）。
//
// 用的是**各家上游的原文片段**，不是猜的——实测 agnes 的原话：
//
//	ContextWindowExceededError: ... The input (994166 tokens) is longer than
//	the model's context length (524288 tokens).
//
// 刻意只收**足够具体**的片段：像 "context length" 这种泛词可能在无关错误里出现，
// 误判的后果是"本该重试的失败被当成确定性失败"（用户拿到一个本该被重试掉的错误）。
var contextMarkers = []string{
	"contextwindowexceeded",
	"context_length_exceeded",
	"longer than the model's context length",
	"maximum context length",
}

// contextTokenRe 从上游原文里抠出"实际 token 数"与"上限"两个数。
//
// 实测 agnes 原话：`The input (994166 tokens) is longer than the model's context length (524288 tokens).`
var contextTokenRe = regexp.MustCompile(`(?i)\(?(\d{3,})\s*tokens?\)?[^0-9]{0,40}(?:context length|maximum|limit)?\s*\(?(\d{3,})\s*tokens?`)

// canonicalContextReason 把各家上游的超限原文**翻译成客户端认得的措辞**。
//
// 为什么必须翻译而不是自造：Claude Code 里那条判据是写死的正则
//（从它的二进制里挖出来）：
//
//	/prompt is too long[^0-9]*(\d+)\s*tokens?\s*>\s*(\d+)/i
//	→ {actualTokens, limitTokens}
//
// 也就是说**客户端只认 Anthropic 的规范句式**。我们自造的
// `{"error":"context window exceeded","hint":"压缩会话"}` 它一个字都不认——
// 于是它按"服务端故障"处理、继续重试，而这是个**永远不会成功**的请求。
//
// 留上游原文在后半段：数字给客户端用，原文给人排查用。
func canonicalContextReason(body []byte, status int) string {
	raw := truncate(string(body), 300)
	if m := contextTokenRe.FindSubmatch(body); m != nil {
		return fmt.Sprintf("prompt is too long: %s tokens > %s maximum (status %d; upstream: %s)",
			m[1], m[2], status, raw)
	}
	// 抠不出数字时也**必须带上 "prompt is too long" 这个句式**——
	// 客户端的分类正则 `\b(too long|too large|exceeds|token limit|prompt is too long)\b`
	// 认得它，会归到 "request too large" 并提示 /compact，而不是当作可重试故障。
	return fmt.Sprintf("prompt is too long (status %d; upstream: %s)", status, raw)
}

// isContextTooLong 这个错误体是不是"上下文超限"。
func isContextTooLong(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	s := strings.ToLower(string(body))
	for _, m := range contextMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// truncate 截断到 n 字节（留上游原文时用；runes 可能被截半，但错误体本就是给人看的）。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Fail 一次失败。Reason 供决策日志留痕（T-003 切换留痕的输入）。
type Fail struct {
	Kind   FailKind
	Reason string
}

// IsRetryable 响应字节尚未发出的失败才可重放（I7）。
func (f Fail) IsRetryable() bool {
	switch f.Kind {
	case FailTransport, FailRateLimit, FailUpstream, FailAuth, FailDenied, FailModelNotFound:
		// FailModelNotFound **算可重试**，因为"模型不存在"是**谁的属性**取决于请求：
		//   · 虚拟模型（auto:reasoning）→ 落到某个具体模型，那个模型被上游撤了 ⇒
		//     **模型级**事实 ⇒ 该换同 provider 的下一个模型（T-018）
		//   · 字面模型（claude-opus-4-8[1m]）→ 客户端点名要的 ⇒ **请求级**事实 ⇒
		//     换谁都是同一个答案，由 Route 显式短路成 ErrModelNotFound
		// 这里返回 true 让虚拟那条路照常轮换；字面那条由 Route 提前截住。
		return true
	}
	return false
}

// BuildURL 把 config.base_url 归一为端点 URL。
// 约定（config/config.json 现行）：base_url 以协议前缀结尾，不含端点名：
//
//	openai     → base + "/chat/completions"（https://api.xkiro.com/v1 → /v1/chat/completions）
//	anthropic  → base + "/v1/messages"   （https://api.deepseek.com/anthropic → /anthropic/v1/messages）
//
// 历史写法（base_url 已含端点名，如 .../v1/chat/completions）自动识别，不重复拼接。
func BuildURL(baseURL, format string) (string, error) {
	trimmed := strings.TrimRight(baseURL, "/")
	suffix := "/chat/completions"
	if format == "anthropic" {
		suffix = "/v1/messages"
	}
	if strings.HasSuffix(trimmed, suffix) {
		return trimmed, nil
	}
	return trimmed + suffix, nil
}

// Invoke 发一次上游请求。流式逐块回调；非流式收完整体。
// 返回 (Result, Fail)：成功时 Fail.Kind==FailNone。
func Invoke(ctx context.Context, client *http.Client, req Request) (Result, Fail) {
	url, err := BuildURL(req.BaseURL, req.Format)
	if err != nil {
		return Result{}, Fail{Kind: FailTransport, Reason: "build url: " + err.Error()}
	}
	// 头阶段预算：per-request，按请求体大小缩放（见 HeaderBudgetFor 与 NewTransport 的说明）。
	//
	// 为什么不用 Transport.ResponseHeaderTimeout：它是 Transport 级的常数，
	// 而这里需要"60KB 给 90s、1.5MB 给 192s"——一个 Transport 装不下两个值。
	budget := req.HeaderBudget
	if budget <= 0 {
		budget = DefaultHeaderBudget
	}
	hctx, cancelHeader := context.WithCancel(ctx)
	defer cancelHeader()
	// 头一到就撤掉这枚定时器（下面 client.Do 返回后 Stop）。
	headerTimer := time.AfterFunc(budget, cancelHeader)

	r, err := http.NewRequestWithContext(hctx, http.MethodPost, url, bytes.NewReader(req.Body))
	if err != nil {
		return Result{}, Fail{Kind: FailTransport, Reason: "new request: " + err.Error()}
	}
	h := make(http.Header, 2)
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer "+req.APIKey)
	if req.Format == "anthropic" {
		h.Set("anthropic-version", "2023-06-01") // 兼容端点约定（v4.0 冒烟验证过）
	}
	r.Header = h

	resp, err := client.Do(r)
	// 头已到（或已失败）：撤掉头预算定时器，后续由 idle 看门狗管。
	headerTimer.Stop()
	if err != nil {
		// **把两种"canceled"分开报**——它们含义完全相反，混成一个字串会让排障跑偏：
		//   调用方断开 → ctx.Err() != nil（客户端不等了，与上游无关）
		//   头预算烧完 → ctx 还好、hctx 被我们的定时器取消了（**上游挂死**）
		if ctx.Err() == nil && hctx.Err() != nil {
			return Result{}, Fail{
				Kind: FailTransport,
				Reason: fmt.Sprintf("header budget exceeded (budget=%s, body=%d bytes)",
					budget, len(req.Body)),
			}
		}
		// 连接错误 / 调用方取消：响应字节前，可重放。
		return Result{}, Fail{Kind: FailTransport, Reason: "transport: " + err.Error()}
	}
	// 空闲看门狗：网关不设总超时（为让长生成不被切断），代价是上游"不回字节也不断开"
	// 时 Read 会永远阻塞。对智能体，挂死比报错糟得多——报错能重试，挂死是会话原地冻住。
	resp.Body = wrapIdle(resp.Body, req.Idle)
	defer resp.Body.Close()

	// **错误响应统一先读一次 body**：下面几个分支都要用它，而且**上游的原文本身就是线索**。
	// 本仓排查手册写过这一点：「上游返回的原文要留下来，它直接指出方向；
	// 顺手改写成'上游不可用'就把线索丢了」——2026-09-22 排查"会话卡很久"时，
	// 留痕里只有 `status 502`，而 agnes 真正想说的是"上下文超限"，线索就是被这里丢掉的。
	if resp.StatusCode >= 400 {
		res := drainErrBody(resp)

		// 上下文超限是**确定性**失败：所有候选、所有 key 都会给同一个答案，
		// 重试只是让调用方白等（实测 agnes 要先扛 60–80 秒），最后仍是一个注定的失败。
		//
		// 判据**看 body 不看状态码**：同一件事 agnes 直连回 400、经它的网关回 502。
		if isContextTooLong(res.Body) {
			return res, Fail{
				Kind:   FailContextTooLong,
				Reason: canonicalContextReason(res.Body, resp.StatusCode),
			}
		}
		// "上游没有这个模型"同样是**确定性**失败，也必须翻译成客户端认得的形状。
		//
		// 判据看 body 不看状态码：实测 agnes 对未知模型有时回 404（我们原本透传，客户端
		// 恰好能认出）、有时回 503 `model_not_found`（我们当可重试 ⇒ 耗尽 ⇒ 回 503 ⇒
		// 客户端重试）。**同一个事实，两种结局**，取决于上游回哪个码。
		if isModelNotFound(res.Body) {
			return res, Fail{
				Kind: FailModelNotFound,
				// 带上游原文：它可能点明是哪个模型名不认（人排查要看）。
				Reason: fmt.Sprintf("status %d: %s", resp.StatusCode, truncate(string(res.Body), 200)),
			}
		}

		// 401/403 必须单独成一类。它们曾被归入 FailNone（"4xx 透传"），
		// 于是路由判 done=true 把 401 原样甩给客户端——**一个失效的 key 会打死整个池**，
		// 多 key 轮换这个核心承诺就落空了。凭据被拒不是客户端错误，是"这把 key 不能用了"：
		// 应当隔离该 key、换池内下一把，而不是透传。
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			return res, Fail{Kind: FailRateLimit, Reason: "429"}
		case http.StatusUnauthorized:
			return res, Fail{Kind: FailAuth, Reason: "status 401 (凭据无效，隔离该 key)"}
		case http.StatusForbidden:
			return res, Fail{
				Kind:   FailDenied,
				Reason: "status 403 (请求被拒，与凭据无关；按模型级处理)",
			}
		}
		if resp.StatusCode >= 500 {
			return res, Fail{
				Kind: FailUpstream,
				// **带上上游原文**：只写 `status 502` 等于把唯一的方向性线索丢掉。
				Reason: fmt.Sprintf("status %d: %s", resp.StatusCode, truncate(string(res.Body), 200)),
			}
		}
		// 其余 4xx（400/404/422…）：透传给调用方判断
	}

	if req.Kind == NonStreaming {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return Result{StatusCode: resp.StatusCode}, Fail{Kind: FailStreamMid, Reason: "read body: " + err.Error()}
		}
		return Result{StatusCode: resp.StatusCode, Body: body}, Fail{Kind: FailNone}
	}

	// 流式：逐块透传。首块回调发出后，任何中断都归入 FailStreamMid（I7：不可重放）。
	sse := newSSE(resp.Body)
	for sse.next() {
		if req.StreamCB != nil {
			req.StreamCB(sse.data())
		}
	}
	if err := sse.err(); err != nil && !errors.Is(err, io.EOF) {
		return Result{StatusCode: resp.StatusCode}, Fail{Kind: FailStreamMid, Reason: "stream: " + err.Error()}
	}
	return Result{StatusCode: resp.StatusCode}, Fail{Kind: FailNone}
}

func drainErrBody(resp *http.Response) Result {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return Result{StatusCode: resp.StatusCode, Body: body}
}

// ProbeBody 最小健康探测请求体（成本近 0；2xx = 上游可达**且该模型可用**才算健康）。
//
// model 必须是**该 provider 真实持有的模型名**（调用方从 p.Models 取）。
// 这里曾硬编码假名 "m"，而两个上游都拒绝未知模型名
// （agnes 503 model_not_found / xkiro 404 model does not exist）——
// 于是探测恒判不健康 → 状态机"免费连续健康 ≥T 分钟"永远累积不到阈值 →
// 一旦进了付费层就再也回不到免费（T-004 的回归分支在生产中不可达）。
//
// 同一形态的 body 同时满足两个格式：openai 要 model 非空，anthropic 要 max_tokens 显式声明。
func ProbeBody(model string) []byte {
	req := struct {
		Model     string              `json:"model"`
		MaxTokens int                 `json:"max_tokens"`
		Messages  []map[string]string `json:"messages"`
	}{
		Model:     model,
		MaxTokens: 1,
		Messages:  []map[string]string{{"role": "user", "content": "ping"}},
	}
	// 结构体只含 string/int/切片，json.Marshal 不会失败。
	body, _ := json.Marshal(req)
	return body
}

// IsHealthy 一次调用的"健康"判定：状态码 2xx 即健康（429/5xx/超时/断流都不算）。
func IsHealthy(res Result, fail Fail) bool {
	return fail.Kind == FailNone && res.StatusCode >= 200 && res.StatusCode < 300
}

// EncodeBody 请求体序列化（两种格式各自的 schema 由调用方构造，这里只管编解码不报错）。
func EncodeBody(v any) ([]byte, error) { return json.Marshal(v) }

// DecodeResult 解析非流式响应体到 out。
func DecodeResult(res Result, out any) error {
	if len(res.Body) == 0 {
		return fmt.Errorf("empty body (status %d)", res.StatusCode)
	}
	return json.Unmarshal(res.Body, out)
}
