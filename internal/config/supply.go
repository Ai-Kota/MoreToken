package config

import (
	"fmt"
	"strings"
)

// DemandKinds 客户端**实际会请求**的虚拟模型类型——**产品事实，独立于供给声明**。
//
// 为什么必须独立声明：若从"config 里有哪些 kind"反推需求，就是自己检查自己——
// 供给收缩时需求跟着收缩，缺口永远报不出来。
//
// 这不是假想。2026-09-20 的事故正是这个形状：门控把付费档的 reasoning 候选删光，
// 系统里**没有任何一处**记录过"reasoning 是必须覆盖的"，于是没人能在它塌掉时报警——
// 只能等用户在 15:31 撞上 503。
//
// 依据：cc-ft 走 `auto:reasoning`；`/v1/models` 对每个 kind 都广告 `auto:<kind>`。
// 改动这里等于改产品承诺，要有意为之。
var DemandKinds = []string{"reasoning", "coding", "general", "fast"}

// HasKind 该 provider 是否持有某类型的模型（其 models 的 kinds 并集）。
//
// ⚠️ kinds 的**精度本身是可疑的**（T-011 实测：coding/general 曾完全不区分，
// 只有 reasoning 有区分力）。本函数只回答"声明上有没有"，不回答"实测准不准"——
// 后者要靠真实调用验证（见 T-011 的 MODEL-CAPABILITY.md）。
func (p Provider) HasKind(kind string) bool {
	for _, m := range p.Models {
		for _, k := range m.Kinds {
			if strings.EqualFold(k, kind) {
				return true
			}
		}
	}
	return false
}

// CoverageGap 一个 (format, kind) 的覆盖缺口。
type CoverageGap struct {
	Format    Format `json:"format"`
	Kind      string `json:"kind"`
	Providers int    `json:"providers"` // 覆盖该 (format,kind) 的 provider 总数
	Free      int    `json:"free"`
	Paid      int    `json:"paid"`
}

// Fatal 该 (format,kind) 一个 provider 都没有 ⇒ 这类请求**必然 503**。
func (g CoverageGap) Fatal() bool { return g.Providers == 0 }

// String 人类可读的一行。
func (g CoverageGap) String() string {
	if g.Fatal() {
		return fmt.Sprintf("%s×%s：**无人覆盖** —— 这类请求必然 503", g.Format, g.Kind)
	}
	if g.Free == 0 {
		return fmt.Sprintf("%s×%s：只有付费兜底覆盖（免费层没有）—— 主力层缺口", g.Format, g.Kind)
	}
	return fmt.Sprintf("%s×%s：只有 1 个 provider 覆盖（无冗余）", g.Format, g.Kind)
}

// CheckCoverage 断言「需求面 ⊆ 供给面」。
//
// 这是一条**静态**性质：只依赖 config，不依赖任何运行时状态。
// 静态性来自 T-015 立的不变量——运行时状态（池退避 / key 冷却 / 档位）
// 只能改变候选的**顺序**与**跳过**，不能改变候选的**存在**。
//
// 正因为它静态，才能在**部署前被穷举检查**：DemandKinds × Format 是个有限小集合。
// 这和"运行时多加几个检查"是完全不同量级的东西——前者对整类缺陷**完备**，
// 后者只是多抓几个个案。
//
// 严重度两档：
//   - fatal：Providers == 0 —— 必然 503
//   - warn ：只有一个 provider（无冗余），或免费层不覆盖（只能靠兜底）
func CheckCoverage(cfg *Config) (fatal, warn []CoverageGap) {
	if cfg == nil {
		return nil, nil
	}
	// 只检查 config 里**实际存在**的格式：没配任何 provider 的格式，
	// 请求它本来就会明确报错（"no X provider in config"），不需要重复告警。
	formats := map[Format]bool{}
	for _, p := range cfg.Providers {
		formats[p.Format] = true
	}

	for f := range formats {
		for _, k := range DemandKinds {
			g := CoverageGap{Format: f, Kind: k}
			for _, p := range cfg.Providers {
				if p.Format != f || !p.HasKind(k) {
					continue
				}
				g.Providers++
				if p.Tier == TierFree {
					g.Free++
				} else {
					g.Paid++
				}
			}
			switch {
			case g.Fatal():
				fatal = append(fatal, g)
			case g.Free == 0 || g.Providers == 1:
				warn = append(warn, g)
			}
		}
	}
	return fatal, warn
}
