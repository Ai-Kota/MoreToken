// 虚拟模型（T-010）：客户端不点名具体模型，只声明"要什么类型的模型"。
//
//	"model": "auto"             → 任意类型，网关自己挑一个能用的
//	"model": "auto:reasoning"   → 要推理型
//	"model": "auto:coding"      → 要编码型
//	"model": "agnes-2.5-flash"  → 字面模型，走原路径（不改写、不过滤，零行为变化）
//
// 为什么可行（两个已核实的前提，见 docs/tasks/T-010.md）：
//   - 凭据不限制模型：实测付费 key 要 agnes-2.5-flash → 200 且 model 回显不变；
//     所以"切 provider"是换账号，不是换模型，智能体感知不到。
//   - agnes 不返回 thinking 块，对话状态里没有模型专属签名，
//     跨模型重发历史天然可接受。
//
// 关键取舍：**只对虚拟模型改写 body**。字面模型一个字节都不动，
// 把新逻辑的风险面压到最小（也保住了对既有客户端的完全兼容）。
package router

import (
	"encoding/json"
	"fmt"
	"strings"

	"moretoken/internal/config"
)

// VirtualName 虚拟模型主名。客户端在 model 字段里使用它（可带 ":<kind>" 后缀）。
const VirtualName = "auto"

// ParseVirtual 解析 model 字段是否为虚拟声明。
//
//	"auto"            → kind="" , true（任意类型）
//	"auto:reasoning"  → kind="reasoning", true
//	"agnes-2.5-flash" → kind="", false（字面模型）
//
// 大小写不敏感（"AUTO:Reasoning" 也认）；空 kind 视作任意类型。
func ParseVirtual(model string) (kind string, virtual bool) {
	m := strings.TrimSpace(model)
	head, tail, hasTail := strings.Cut(m, ":")
	if !strings.EqualFold(strings.TrimSpace(head), VirtualName) {
		return "", false
	}
	if !hasTail {
		return "", true
	}
	return strings.ToLower(strings.TrimSpace(tail)), true
}

// ModelOf 取请求体里的 model 字段。取不到（非法 JSON / 无该字段）返回空串。
func ModelOf(body []byte) string {
	var probe struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return ""
	}
	return probe.Model
}

// SetModel 把请求体里的 model 字段替换成 model，返回新的 body。
//
// 用 map[string]json.RawMessage 改写：JSON 的键序无语义，其余字段逐字节保留。
// body 不是 JSON 对象时返回错误（调用方应据此报错，而不是把虚拟名当模型发上游——
// 那会让上游回一个 "model not found"，把矛头指向错误的方向）。
func SetModel(body []byte, model string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, fmt.Errorf("请求体不是 JSON 对象，无法改写 model: %w", err)
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("编码 model: %w", err)
	}
	fields["model"] = encoded
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("重编请求体: %w", err)
	}
	return out, nil
}

// HasKind 该 provider 是否持有某种类型的模型（其 models 的 kinds 并集）。
// 供候选过滤用：选到不持有此类型的 provider 将无模型可改写。
//
// 实现已收敛到 config.Provider.HasKind——供给面断言（config.CheckCoverage）也要问
// 同一个问题，两处各写一份迟早会分叉。
func HasKind(p config.Provider, kind string) bool { return p.HasKind(kind) }

// ResolveModels 在该 provider 的模型里挑出**全部**匹配 kind 的模型，按偏好顺序
// （config 里的声明顺序即偏好顺序）。
//
// kind 为空 = 任意类型 → 全部模型。
//
// 返回**列表**而不是单个：模型和 key 一样是会话保持的消耗品——
// "上游悄悄不再提供某个模型"（404）是常态，只认第一名等于把 provider 的服务能力
// 钉死在 config 的第一行上。见 tryCandidate 里的模型轮换。
func ResolveModels(p config.Provider, kind string) []string {
	if kind == "" {
		out := make([]string, 0, len(p.Models))
		for _, m := range p.Models {
			out = append(out, m.ID)
		}
		return out
	}
	out := make([]string, 0, len(p.Models))
	for _, m := range p.Models {
		for _, k := range m.Kinds {
			if strings.EqualFold(k, kind) {
				out = append(out, m.ID)
				break
			}
		}
	}
	return out
}

// ResolveModel 取首选模型（ResolveModels 的第一个）。保留此函数供只关心首选的调用方用。
//
// 找不到匹配 → ok=false，调用方应把该 provider 从候选中剔除（或整个请求失败），
// **不得**退而用一个不相干的模型——那会让"要推理型"静默变成别的，比报错更难查。
func ResolveModel(p config.Provider, kind string) (string, bool) {
	ms := ResolveModels(p, kind)
	if len(ms) == 0 {
		return "", false
	}
	return ms[0], true
}

// OutputCap 该 provider 下指定模型的输出上限（max_output_tokens，0 = 未声明）。
// 供 T-044 抬预算时封顶——不造出超过模型声明上限的请求。
func OutputCap(p config.Provider, modelID string) int {
	for _, m := range p.Models {
		if m.ID == modelID {
			return m.MaxOutputTokens
		}
	}
	return 0
}

// EnsureOutputBudget 托底虚拟模型请求的输出预算（T-044）：
// max_tokens 缺失或低于 floor 时抬到 floor；cap>0（该模型声明的输出上限）时
// 封顶到 min(floor, cap)——不造出超限请求。**只抬不降**：客户端给的值 ≥ 目标
// 时原样不动。
//
// 返回 (新 body, 是否抬了, 抬前值, 抬后值)。抬前 0 = 原请求未带 max_tokens。
// floor<=0（关）或 body 非 JSON 对象时原样返回（raised=false）。
// openai 与 anthropic 的键名都是 max_tokens，一处改写。
func EnsureOutputBudget(body []byte, floor, capTok int) ([]byte, bool, int, int) {
	if floor <= 0 {
		return body, false, 0, 0
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return body, false, 0, 0
	}
	var cur int
	if raw, ok := fields["max_tokens"]; ok {
		if json.Unmarshal(raw, &cur) != nil {
			cur = 0
		}
	}
	target := floor
	if capTok > 0 && capTok < target {
		target = capTok
	}
	if cur >= target {
		return body, false, cur, cur
	}
	encoded, err := json.Marshal(target)
	if err != nil {
		return body, false, cur, cur
	}
	fields["max_tokens"] = encoded
	out, err := json.Marshal(fields)
	if err != nil {
		return body, false, cur, cur
	}
	return out, true, cur, target
}
