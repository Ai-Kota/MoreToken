// Package catalog 上游模型目录的**纳新**：发现 → 实测 → 入库。
//
// 为什么需要它（用户定性，2026-09-20）：免费池是主力，而**供给是易腐资源**——
// 促销结束、上游撤模型、新模型上架，全都在我们配置之外发生。
// 只靠人工维护 config 的 models 列表，池子只会单调衰减：补回来要"人去找 → 验证 →
// 改配置 → 部署"，**恢复时间常数无界**。新陈代谢是让这条常数有界的唯一办法。
//
// 实测判据（2026-09-20，xkiro）：
//   - 上游目录有 111 个模型，其中 27 个带 `:free`；我们的 config 只用了 3 个。
//   - 抽样 4 个未在用的 `:free`：**2 个 200、1 个上游 500**、1 个在用。
//     ⇒ **列表 ≠ 可用**，必须真调（T-011 的教训在这里第二次成立）。
//
// 本包只做"发现 + 实测"。**复核与淘汰不在此重复实现**——直接复用 router 的
// 池级退避与探活（有池被隔离就探），那是同一件事的既有机制。
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"moretoken/internal/config"
	"moretoken/internal/provider"
)

// ListURL 把 provider 的 base_url 归一为模型目录端点。
//
// 沿用 provider.BuildURL 的同一套约定（base 不含端点名）：
//
//	openai    base 以 /v1 结尾  → {base}/models      （https://api.xkiro.com/v1 → /v1/models）
//	anthropic base 不带版本      → {base}/v1/models   （https://apihub.agnes-ai.com → /v1/models）
//
// 已含端点名时原样返回（历史写法，同 BuildURL）。
func ListURL(baseURL string, format config.Format) string {
	trimmed := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(trimmed, "/models") {
		return trimmed
	}
	if format == config.FormatAnthropic {
		return trimmed + "/v1/models"
	}
	return trimmed + "/models"
}

// ModelInfo 上游目录里的**一条模型记录**。
//
// 除 id 外的字段是"**上游自愿给的**"：xkiro 会给 context_length / max_output_tokens，
// agnes 只给 5 个基础字段。给不给都行——不给就不填，**绝不猜**。
type ModelInfo struct {
	ID string `json:"id"`
	// ContextLength 上下文窗口（0 = 上游没给）。
	ContextLength int `json:"context_length,omitempty"`
	// MaxOutputTokens 输出上限（0 = 上游没给）。
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
}

// Fetch 拉取上游**声称**供应的模型列表。
//
// 注意措辞：这是上游的自述，不是可用性证据——调用方必须再实测（见 Harvester.Run）。
func Fetch(ctx context.Context, client *http.Client, prov config.Provider, apiKey string) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ListURL(prov.BaseURL, prov.Format), nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if prov.Format == config.FormatAnthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", ListURL(prov.BaseURL, prov.Format), err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch models: status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var out struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("解析模型目录（期望 openai 形状 {data:[{id}]}）: %w", err)
	}
	ids := make([]ModelInfo, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m)
		}
	}
	return ids, nil
}

// nonChatMarkers 明显不是对话模型的 id 片段。
//
// 只按**名字**粗筛，宁可漏筛不可错杀：真正的判据是后面的实测——
// 筛掉的如果其实是对话模型，代价是"这次没纳进来"；错杀一个能用的，代价是白烧一次实测请求。
var nonChatMarkers = []string{
	"image", "video", "audio", "tts", "whisper", "embed", "rerank", "moderation", "speech",
}

// freeFirst 稳定排序：带 `:free` 标记的排前面，其余保持原序。
//
// 为什么需要：Max 是成本闸，而目录顺序把旗舰模型排在前面——
// 2026-09-20 实测，Max=6 时六个候选全是 403 的旗舰，27 个真正可用的 `:free` 一个都没轮到。
// 排序用**上游自己的声明**当先验，不引入任何我方假设。
func freeFirst(in []ModelInfo) []ModelInfo {
	out := make([]ModelInfo, 0, len(in))
	for _, m := range in {
		if strings.HasSuffix(m.ID, ":free") {
			out = append(out, m)
		}
	}
	for _, m := range in {
		if !strings.HasSuffix(m.ID, ":free") {
			out = append(out, m)
		}
	}
	return out
}

// IsChatCandidate 粗筛：这个 id 值不值得花一次实测。
func IsChatCandidate(id string) bool {
	lower := strings.ToLower(id)
	for _, m := range nonChatMarkers {
		if strings.Contains(lower, m) {
			return false
		}
	}
	return true
}

// Rejection 一次实测失败的留痕。留着是因为"为什么没纳进来"和"纳进来了什么"一样重要——
// 上游目录会变，今天的 500 明天可能是 200。
type Rejection struct {
	ID  string `json:"id"`
	Err string `json:"err"`
}

// Result 一个 provider 的纳新结果。
type Result struct {
	Verified []ModelInfo `json:"verified"`
	// Refreshed 已在库的模型的**元数据刷新**（窗口/输出上限），来自同一次目录请求，零额外成本。
	Refreshed []ModelInfo `json:"refreshed,omitempty"`
	Rejected  []Rejection `json:"rejected,omitempty"`
	Skipped   int         `json:"skipped"`             // 已在 config 里声明、本次未实测的
	Truncated int         `json:"truncated,omitempty"` // 因 Max 上限未实测的
}

// Harvester 一次纳新的参数。
type Harvester struct {
	Client   *http.Client
	Provider config.Provider
	APIKey   string
	Known    []string      // 已在 config 里声明的 id：跳过（手写声明优先，带真实 kinds）
	Max      int           // 本次最多实测几个（0 = 不限）
	Delay    time.Duration // 两次实测之间的间隔（免费额度按账号限流，别打爆）
}

// Run 执行"发现 → 实测"。
//
// 只有 2xx 才算通过——与探活同一判据（provider.IsHealthy）。
// 通过者**只标 general**：实测能证明的只有"它活着并能回一条响应"，
// 证明不了"它会推理"。kinds 是能力断言，必须人工实测后再升格（T-011：
// coding/general 曾是虚假精度，只有 reasoning 有区分力）。
func (h Harvester) Run(ctx context.Context) (Result, error) {
	key := strings.TrimSpace(h.APIKey)
	if key == "" || config.IsPlaceholder(key) {
		return Result{}, fmt.Errorf("provider %s 无可用凭据，无法实测", h.Provider.ID)
	}
	if h.Client == nil {
		return Result{}, fmt.Errorf("nil client")
	}

	ids, err := Fetch(ctx, h.Client, h.Provider, key)
	if err != nil {
		return Result{}, err
	}

	known := make(map[string]bool, len(h.Known))
	for _, k := range h.Known {
		known[k] = true
	}

	// 候选排序：上游自己标了免费的（`:free` 后缀）先实测。
	//
	// 依据（2026-09-20 实测）：xkiro 目录 111 个模型里 27 个带 `:free`。
	// 未带标记的旗舰（claude-opus-5 / gpt-5.6-sol …）对这一档凭据**全部 403**，
	// 而带 `:free` 的抽样全 200。目录顺序把旗舰排在前面 ⇒ Max=6 时一个免费模型
	// 都轮不到（实测：6 个候选全 403、102 个未测）。
	//
	// 这是**排序先验**不是过滤器：没带标记的不淘汰，只是排后面。
	// 过滤器会把"某天上游换了标记习惯"变成静默的能力损失。
	ids = freeFirst(ids)

	var res Result
	res.Verified = []ModelInfo{}
	tested := 0
	for _, mi := range ids {
		id := mi.ID
		if known[id] {
			res.Skipped++
			// **已知模型的元数据也要刷新**：「已知」只意味着不必重新**实测**，
			// 而 context_length / max_output_tokens 就在这次目录响应里，是白拿的。
			// 不刷的话，那些在"支持声明窗口"之前纳进来的模型会**永远**没有窗口
			//（2026-09-22：5 个自动纳新模型全空，因为 Known 一路跳过它们）。
			if mi.ContextLength > 0 || mi.MaxOutputTokens > 0 {
				res.Refreshed = append(res.Refreshed, mi)
			}
			continue
		}
		if !IsChatCandidate(id) {
			continue
		}
		if h.Max > 0 && tested >= h.Max {
			res.Truncated++
			continue
		}
		if tested > 0 && h.Delay > 0 {
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(h.Delay):
			}
		}
		tested++

		ok, err := h.probe(ctx, id, key, h.Provider)
		if ok {
			// 连**上游自愿给的元数据**一起带回：context_length / max_output_tokens。
			// 这不是锦上添花——它是 `/v1/models` 对外声明窗口的**唯一自动来源**，
			// 而声明窗口是"客户端在超限前压缩"的前提（2026-09-22 那起卡死会话的正解）。
			// agnes 不给这些字段 ⇒ 就不填，绝不猜。
			res.Verified = append(res.Verified, mi)
			continue
		}
		res.Rejected = append(res.Rejected, Rejection{ID: id, Err: errText(err)})
	}
	return res, nil
}

// probe 发一次最小真实请求。复用 provider.Invoke + ProbeBody——
// 探测体必须带**真模型名**（TROUBLESHOOTING §3：曾经硬编码 "m"，上游一律拒，
// 于是探测恒判不健康，回免费的分支在生产中不可达）。
func (h Harvester) probe(ctx context.Context, model, key string, prov config.Provider) (bool, error) {
	req := provider.Request{
		Format:  string(prov.Format),
		BaseURL: prov.BaseURL,
		APIKey:  key,
		Body:    provider.ProbeBody(model),
		Kind:    provider.NonStreaming,
	}
	res, fail := provider.Invoke(ctx, h.Client, req)
	if provider.IsHealthy(res, fail) {
		return true, nil
	}
	if fail.Kind != provider.FailNone {
		// 别复用 provider 里那句"凭据被拒，隔离该 key"——那是**路由侧**的口径
		// （路由会对该 key 长禁）。纳新**不惩罚任何 key**，照抄那句话会把排查方向带偏：
		// 实测同一把 key 对 agnes-2.5-flash 回 200、对 agnes-2.5-pro 回 403，
		// 凭据是好的，是账号对这个模型没有权限。
		if res.StatusCode != 0 {
			return false, fmt.Errorf("status %d（上游拒绝；纳新不据此惩罚 key）", res.StatusCode)
		}
		return false, fmt.Errorf("%s", fail.Reason)
	}
	// 非 2xx：把上游原文留下——它直接指出方向（TROUBLESHOOTING §7：
	// "上游返回的原文要留下来，顺手改写成'上游不可用'就把线索丢了"）。
	return false, fmt.Errorf("status %d: %s", res.StatusCode, truncate(string(res.Body), 120))
}

func errText(err error) string {
	if err == nil {
		return "unknown"
	}
	return err.Error()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
