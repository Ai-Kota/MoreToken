package config

import (
	"strings"

	"moretoken/internal/admission"
)

// MergeResult 一次 [ApplyAdmission] 的结果。**至少 Unmatched 必须被调用方报出来**，
// 否则本函数就有一类失败是无声的（见下）。
type MergeResult struct {
	Added    int // 新追加进池的模型数
	Upgraded int // 已存在模型被补齐 kinds 的处数

	// Unmatched 有实测证据（该维度真的通过了）、却**没有任何 provider 的 base_url 与之匹配**
	// 的模型。它们不是"不合格"而是"无处安放" —— 进不了池，且**不会有任何报错**。
	//
	// ⚠️ 为什么必须单独暴露（2026-10-01，本函数自己的注释先写下了这个失效模式却没装仪表）：
	// normBase 的归一化一旦漏掉某种书写差异（或该上游压根没配 provider），模型就静默进不了池，
	// 调用方只看到 `新增 0`，**无从知道是"表里没有合格模型"还是"全都没匹配上"**。
	// 这与本项目反复吃亏的"静默归零"同型，故单独列出来供日志/doctor 明说。
	Unmatched []string
}

// ApplyAdmission 把**实测准入结论**并进配置：实测通过的模型进对应的池（provider），
// 已存在的模型补齐实测通过的 kinds。
//
// # 为什么要有它（2026-10-01 用户定，方案 a）
//
// 运行时那层 admission filter **只能过滤、不能新增**：候选来自各 provider 的 `models[]`，
// 一个"实测合格、但配置里没声明"的模型根本进不了候选 —— 测了也用不上。
// 本函数补上"入池"这一半，于是评估器的结论能真正变成可路由的供给。
//
// # 它接的是 moretoken **本来就预留的扩展点**
//
// `MergeAuto` 的注释写着：「实测能证明的只有"它活着并能回一条响应"，证明不了"它会推理"……
// **要升格某个模型的 kinds，必须走人工实测**」。本函数就是那句"人工实测"的自动化执行者。
//
// # 边界（MergeAuto 两条硬规矩的精神一条不丢）
//
//  1. **只追加、不改写、不前插**：新模型排在该 provider 现有 `models` 之后；
//     已有模型的 kinds **一个都不删** —— 手写 kinds 是人的显式偏好，"实测"没有改写它的权力。
//     剔除不合格是**运行时 filter** 的职责（见 internal/router.filterByAdmission）。
//     两者互补：**filter 管删，本函数管补**。
//  2. **kinds 只来自实测证据**：没有任何维度实测通过 ⇒ 不给标签、也不入池。
//     绝不采信清单文件里自称的能力 —— 与 MergeAuto「kinds 强制 general」同一条底线。
//
// # 归属：按 base_url 匹配 provider
//
// 一个 base 可能对上多个 provider（同一上游的 anthropic 与 openai 两个协议入口）——
// 那就都加：该模型在两条协议路径上都可用。归一化比较见 normBase。
func ApplyAdmission(cfg *Config, rows []admission.ModelAdmission, minRatio float64) MergeResult {
	var res MergeResult
	if cfg == nil || len(rows) == 0 {
		return res
	}
	if minRatio <= 0 || minRatio > 1 {
		minRatio = 1.0
	}
	for _, r := range rows {
		if strings.TrimSpace(r.Model) == "" {
			continue
		}
		kinds := passingKinds(r, minRatio)
		if len(kinds) == 0 {
			continue // 无实测证据 ⇒ 不入池（见边界 2）
		}
		matched := false
		for i := range cfg.Providers {
			p := &cfg.Providers[i]
			if normBase(p.BaseURL) != normBase(r.Base) {
				continue
			}
			matched = true
			// 已在池中 ⇒ 只补不删
			found := false
			for j := range p.Models {
				if p.Models[j].ID == r.Model {
					found = true
					for _, k := range kinds {
						if !hasKind(p.Models[j].Kinds, k) {
							p.Models[j].Kinds = append(p.Models[j].Kinds, k)
							res.Upgraded++
						}
					}
					break
				}
			}
			if found {
				continue
			}
			// 不在池中 ⇒ **追加到末尾**（规矩 1：绝不前插，不抢手写模型的位置）
			p.Models = append(p.Models, Model{ID: r.Model, Name: r.Model, Kinds: kinds})
			res.Added++
		}
		if !matched {
			// 有证据、没去处 —— 必须浮上来，不能让它静静消失（见 MergeResult.Unmatched）。
			res.Unmatched = append(res.Unmatched, r.Model)
		}
	}
	return res
}

// passingKinds 返回该记录实测通过的维度，**顺序固定**（reasoning → coding）。
//
// 固定顺序是为了让结果可复现：map 遍历无序，直接吐出来会让同一份表两次合并出不同
// 的 kinds 顺序，diff 就没法看了。
func passingKinds(r admission.ModelAdmission, minRatio float64) []string {
	var out []string
	for _, dim := range []string{admission.DimReasoning, admission.DimCoding} {
		if r.Passes(dim, minRatio) {
			out = append(out, dim)
		}
	}
	return out
}

// normBase 归一化 base 以便跨源比较：去空白、去尾斜杠、去尾 /v1。
//
// ⚠️ 必须归一化（本项目血账"跨系统比字符串，正/反斜杠与大小写差异让相等判据恒假"）：
// 评估器记的是上游根地址（`https://api.xkiro.com`），而 config 里同一上游的 openai
// 入口写成 `https://api.xkiro.com/v1` —— 不归一化就**永远匹配不上**，
// 且失败方向是静默的（模型进不了池，没有任何报错）。
func normBase(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, "/v1")
	return strings.TrimSuffix(s, "/")
}

func hasKind(kinds []string, k string) bool {
	for _, x := range kinds {
		if x == k {
			return true
		}
	}
	return false
}
