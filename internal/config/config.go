// Package config 加载 FreeLLMAPI 蓝本的声明式配置：
// provider（base_url + format + tier）× 多 key × models。
// 多 key 是稳定性的核心——账号级限流由多 key 轮换吸收（FreeLLMAPI 验证过的机制）。
//
// 密钥以**指针**形式写在配置里（配置文件入库，明文不得落盘）：
//
//	env:NAME     → 环境变量
//	vault:PATH   → 密码墙条目（"外面只放指针，明文只在墙里"）
//
// 解析由调用方显式触发（ResolveKeys），不在 Load 里做——Load 保持"读文件 + 校验"的纯职责，
// 这样解析可以拿到 context 与 logger（见 internal/nats 的"探测能力 / 暴露状态 / 执行"分层）。
package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Format 表示上游协议格式。两条路径完全隔离，互不串选。
type Format string

const (
	FormatOpenAI    Format = "openai"    // /v1/chat/completions
	FormatAnthropic Format = "anthropic" // /v1/messages（Claude Code）
)

// Tier 表示 provider 的免费/付费层级，是省钱状态机的基础（相对 FreeLLMAPI 的差异化）。
type Tier string

const (
	TierFree Tier = "free" // 免费层：任一健康就走它
	TierPaid Tier = "paid" // 付费兜底：全免费不可用才显式进入
)

// 密钥引用前缀。两者都是"指针"，真值不在配置文件里。
const (
	PrefixEnv   = "env:"
	PrefixVault = "vault:"
)

// DefaultVaultBin vault 二进制默认路径。
// 与 tools/PUBLIC/vault/client/vaultcli.go 的 DefaultBin 保持一致；可用 VAULT_BIN 覆盖。
const DefaultVaultBin = `E:\AImlyForge\tools\PUBLIC\vault\bin\vault.exe`

// DefaultVaultTimeout 整轮 vault 解析的总预算（不是每次调用的超时）。
//
// 按"与 key 数无关"的常量预算：15 把 key 各 3s 会是 45s 最坏；
// 一次 ctx 预算把启动惩罚硬夹在 10s 内，无论配置里有多少 key。
const DefaultVaultTimeout = 10 * time.Second

// ErrVaultUnavailable vault 二进制缺失或不可用。
//
// 与"vault 可用但某个 path 取不到"区分开：前者是能力缺失（静默降级，
// 保护"骨架/CI 可无 key 加载"），后者是配置错误（必须响）。
var ErrVaultUnavailable = errors.New("vault unavailable")

// Model 单个模型条目。
type Model struct {
	ID   string `json:"id"`   // 上游模型 id，如 agnes-2.5-flash
	Name string `json:"name"` // 显示名
	// Kinds 该模型能承担的功能类型（general / reasoning / coding / fast）。
	//
	// 供虚拟模型路由挑选（客户端发 "auto:reasoning" → 网关选一个 kinds 含 reasoning 的模型）。
	// 留空 = 不参与类型路由，但仍可被无类型的 "auto" 选中。
	Kinds []string `json:"kinds,omitempty"`

	// ContextLength 该模型能容纳的**输入** token 上限（0 = 未知）。
	//
	// 它要对外声明（`/v1/models` 的 `max_input_tokens`），让客户端**在超限前**压缩——
	// 否则客户端会把上下文堆到远超上限，然后收到一个它认不出的错误，无限重试。
	// 2026-09-22 实测：某会话堆到 994166 token，而 agnes 上限 524288。
	//
	// 取值来源必须是**证据**，不是猜：agnes 三个模型都是 524288（从它自己的
	// 超限报错里读出来的）；xkiro 的目录直接给 `context_length`（纳新时自动带出来）。
	ContextLength int `json:"context_length,omitempty"`

	// MaxOutputTokens 输出上限（0 = 未知）。同样对外声明为 `max_tokens`。
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
}

// Provider 一个上游服务商（对应 FreeLLMAPI 的 platform/customProviders）。
type Provider struct {
	ID      string   `json:"id"`
	BaseURL string   `json:"base_url"` // 如 https://apihub.agnes-ai.com/v1
	Format  Format   `json:"format"`
	Tier    Tier     `json:"tier"`
	Keys    []string `json:"keys"` // 多 key 轮换（>=1，limit 由多 key 吸收）
	Models  []Model  `json:"models"`
}

// Config 顶层配置。
type Config struct {
	Listen string `json:"listen"`

	// AuthToken 单 token 简化鉴权（T-024 兼容模式）。支持字面量 / env:VAR / vault:PATH
	// 三种形态（与 keys 同款，经 ResolveKeys 一并解析）。
	//
	// 优先级：config/tokens.json 存在且有活跃条目 ⇒ 多 token 模式（本字段被忽略）；
	// 否则本字段非空 ⇒ 单 token 模式；两者皆空 ⇒ 不鉴权（T-024 之前的行为，零变化）。
	// 正式部署建议用 -token-gen 走多 token 模式（吊销/过期/审计/每客户端独立身份）。
	AuthToken string `json:"auth_token,omitempty"`

	Providers []Provider `json:"providers"`

	// AdmissionPath 实测能力准入选表的路径（外部评估工具产出的 JSON，格式见 internal/admission）。
	//
	// 2026-10-01（ADR-004 §四）：选型此前只读**人工写的 kinds**；接上它之后
	// `auto:reasoning` / `auto:coding` 改为在该维度**实测通过**的模型里挑。
	//
	// 留空 / 文件不存在 / 解析失败 ⇒ **不做准入过滤**（回退既有行为），
	// 且状态在 /doctor 上暴露 —— 不静默降级。详见 internal/admission 包注释。
	AdmissionPath string `json:"admission_path,omitempty"`

	// AdmissionMinRatio 某维度的准入线：passed/total ≥ 本值才算过。
	//
	// 缺省 / 0 / >1 一律按 **1.0（全过）** 处理 —— **不配置就与接入前完全一致**。
	//
	// 为什么可配（2026-10-01 用户定，方案 A）：题组只有 5 道，多对一道就是 80% vs 100%，
	// "全过"在小样本上等于"完美或零"，实测导致**编程池为空**（最好的模型 4/5）。
	// 阈值让"多严"成为可调的决定，而不是写死在代码里。
	AdmissionMinRatio float64 `json:"admission_min_ratio,omitempty"`

	// ExcludeModels 模型级排除名单（T-043）：这些 id 永远不得进入免费池。
	//
	// 为什么需要它（2026-10-08 用户拍板）：agnes-3.0-flash 定位"付费维护"，
	// 用免费 key 调它是否扣付费余额【未核实】；用户决定不冒这个险。但它是
	// **自动纳新**收进来的——只删一次清单，下一轮 harvest 会从旧清单继承再写回
	// （T-043 已记录的复发机制）。排除名单是策略，必须写在手写区，由三处执行：
	//   - config.MergeAuto（加载自动清单时跳过）
	//   - config.ApplyAdmission（准入表回流时跳过）
	//   - main.go runHarvest（继承旧清单 + 实测纳新时跳过）
	//
	// 为什么不是写在 models.auto.json：那是生成物，每轮会被重写；策略与库存必须分开。
	ExcludeModels []string `json:"exclude_models,omitempty"`

	// AutoAdded 本次加载从 models.auto.json 追加进来的模型条数（不参与序列化）。
	// 存在的意义是"纳新到底有没有生效"要能被看见——一个悄悄不生效的机制
	// 与没有这个机制没有区别（T-014 的教训）。
	AutoAdded int `json:"-"`

	// RateLimitDailyBan key 级 429 日账本阈值（T-040）：某把 key 当日 429 次数
	// 达到本值 ⇒ 摘除至**当日 24:00**（跨日自动归零复活）。
	//
	// 为什么存在（2026-10-07）：单次 429 的 60s 短冷却只挡"分钟级的墙"；
	// 免费档的额度墙是**日级**的（xkiro 429 原文："wait for the daily reset"）。
	// 对"今天没了"的 key，60s 解禁换来的只是再撞一次墙——持久化任务 7×24 压
	// 免费池时，这种撞墙循环烧的是全池的时间。
	//
	// 语义（用指针区分"没配"与"配 0"，JSON 里 int 分不开这两者）：
	//   - 未配置（nil）⇒ 缺省 4。N=4 的由来：单次 429 多是池子撞墙的余波；
	//     连续 4 次（间隔 60s 短冷却）≈ 该账号当日额度见底。宁晚不冤——
	//     晚摘 3 次 × 60s ≈ 3 分钟损失，误杀则封一把还有额度的 key 一整天。
	//   - 显式 0 ⇒ 关（回归闸：行为与启用前完全一致，只走 60s 短冷却）。
	//   - ≥1 ⇒ 生效该阈值。
	RateLimitDailyBan *int `json:"rate_limit_daily_ban,omitempty"`

	// AllowPaidFallback 虚拟模型（auto / auto:*）是否允许 fallback 进**付费**层（T-042）。
	//
	// 用户定性（2026-10-07）："付费大模型兜底是个伪命题，应该是免费大模型循环切换
	// 兜底"——日常任务 100% 压免费池；付费 key 只服务基建维护（**字面模型点名直连**，
	// 那条路不受本字段影响，永远可用）。免费全挂 ⇒ 503 + 等日额度重置（T-040 配套）。
	//
	// 指针语义（同 RateLimitDailyBan 先例，区分"没配"与"配 false"）：
	//   - nil（未配置）⇒ **false**（新政策：虚拟模型不进付费层）——行为变化，
	//     升级说明见 CHANGELOG；
	//   - 显式 false ⇒ 同 nil；显式 true ⇒ 恢复"免费挂转付费"旧行为
	//     （回滚闸，供 2026-09-20 型事故排查时临时开）。
	AllowPaidFallback *bool `json:"allow_paid_fallback,omitempty"`

	// MinOutputTokens 虚拟模型请求的输出预算下限（T-044，2026-10-07 实测驱动）。
	//
	// 为什么存在：主力 agnes-2.5-flash 是**推理型**——思考链极长。调用方默认给
	// 32–128 的 max_tokens 时，思考把预算吃光、**正文 content 恒空**
	// （finish_reason=length），难题 8 题里 7 题"答不出"；预算给到 4096 后
	// 同题 6/8 对——**同一模型，纯预算差异**（T-041 追加二）。任何短答场景的
	// 调用方都会重演这个现象，所以托底是网关的责任。
	//
	// 语义（指针先例沿用 T-040/T-042，区分"没配"与"配 0"）：
	//   - nil（未配置）⇒ 缺省 **4096**（今日实测"够用线"）；
	//   - 显式 0 ⇒ 关（回归闸：完全尊重客户端原值，行为与启用前一致）；
	//   - 显式 N ⇒ 下限取 N。
	//
	// 只**抬不降**：客户端 max_tokens ≥ 下限 ⇒ 原样；缺失或 < 下限 ⇒ 抬到
	// min(下限, 该模型已声明的 max_output_tokens)（有模型上限声明时封顶，
	// 不造出超限请求）。仅对**虚拟模型**生效（"网关替你挑的池"才由网关兜底）；
	// 字面模型一个字节不动（与 admission 过滤同款纪律）。
	MinOutputTokens *int `json:"min_output_tokens,omitempty"`
}

// Load 从 JSON 文件加载并校验配置。
//
// 纯职责：读文件 + 校验 + 默认值。**不做密钥解析**——那一步由调用方经 ResolveKeys 显式触发。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	// 自动纳新清单（可选）：与手写配置同目录，只做追加（见 MergeAuto 的两条硬规矩）。
	// 文件不存在 ⇒ 行为与以前完全一致；文件存在但坏了 ⇒ 报错（别把"纳新从未生效"藏起来）。
	inv, err := LoadAuto(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if n := MergeAuto(&cfg, inv); n > 0 {
		cfg.AutoAdded = n
	}
	return &cfg, nil
}

// IsPlaceholder 判断一个 key 条目是否为未解析的引用占位符（即"不可用作凭据"）。
//
// 统一收敛此判断——旧实现把它写死在三处（config/router/main），加前缀必然漏改，
// 而漏改的后果是占位符被当成真 key 发上游：`Authorization: Bearer env:AGNES_KEY_1` → 401。
//
// 无长度守卫是刻意的：裸 "env:" / "vault:"（空名字，很常见的打字错误）长度恰为 4，
// 用 len(k)>4 判断会放过它。前缀一出现就是"不可用凭据"，管它后面有没有内容。
//
// 约定：前缀集合是封闭的，字面量 key 不得以它们开头（现实中的 key 形如
// sk- / sk-ant- / sk-or- / cpk- / sk-xt-，不会撞上）。
func IsPlaceholder(k string) bool {
	return strings.HasPrefix(k, PrefixEnv) || strings.HasPrefix(k, PrefixVault)
}

// IsExcludedModel 报告 id 是否在排除名单中（T-043）。
//
// 名单很小且启动后只读，线性扫描即可；nil 配置返回 false（调用方不必判空）。
func (c *Config) IsExcludedModel(id string) bool {
	if c == nil {
		return false
	}
	for _, x := range c.ExcludeModels {
		if x == id {
			return true
		}
	}
	return false
}

// Resolver 把一个 vault path 解析为明文。可注入，便于测试（默认实现会 exec vault.exe）。
type Resolver func(ctx context.Context, path string) (string, error)

// KeyReport 一次密钥解析的结果，供启动日志与降级诊断（**不含明文**）。
type KeyReport struct {
	Resolved   int      // 可用凭据数（解析成功 + 字面量）
	Unresolved int      // 仍为占位符的 key 数
	Details    []string // 每个未解析项的可诊断描述
}

// ResolveKeys 解析全部 keys 里的 env:/vault: 引用。
//
// 失败**不 fatal**——保留原占位符，池视其为"无凭据"（→ 503，而不是把空串或占位符
// 发上游换一个 401），与"骨架/CI 可无 key 加载"的既有语义一致。失败明细进 KeyReport。
//
// 两个关键点：
//   - **去重**：同一 vault path 在多处出现时只解析一次（负结果同样缓存），
//     否则 N 把 key 就是 N 次进程外 exec。
//   - **总预算**：由 ctx 控制，与 key 数无关。调用方应传入带超时的 ctx
//     （见 DefaultVaultTimeout）；ctx 为 nil 时退化为 Background。
func (c *Config) ResolveKeys(ctx context.Context, resolve Resolver) KeyReport {
	if ctx == nil {
		ctx = context.Background()
	}
	if resolve == nil {
		resolve = defaultVaultGet
	}
	var rep KeyReport
	cache := map[string]string{} // path → 明文（"" 表示负缓存）

	// AuthToken（单 token 兼容模式）先解析：与 keys 同款 env:/vault:/字面量 三形态。
	// 解析失败不 fatal（保留原值），但**必须留痕**——一个解析不出来的 auth_token
	// 会让网关拿占位符当 token 校验，合法客户端全 401 而现象指向"客户端配置错"。
	if c.AuthToken != "" {
		switch {
		case strings.HasPrefix(c.AuthToken, PrefixEnv):
			name := c.AuthToken[len(PrefixEnv):]
			if v := strings.TrimSpace(os.Getenv(name)); v != "" {
				c.AuthToken = v
			} else {
				rep.Details = append(rep.Details, fmt.Sprintf("auth_token: env:%s 未设置（鉴权将拒绝所有请求）", name))
			}
		case strings.HasPrefix(c.AuthToken, PrefixVault):
			path := c.AuthToken[len(PrefixVault):]
			v, err := resolve(ctx, path)
			if err != nil || strings.TrimSpace(v) == "" {
				reason := "返回空值"
				if err != nil {
					reason = err.Error()
				}
				rep.Details = append(rep.Details,
					fmt.Sprintf("auth_token: vault:%s (%s)（鉴权将拒绝所有请求）", path, reason))
			} else {
				c.AuthToken = strings.TrimSpace(v)
			}
		}
	}

	for i := range c.Providers {
		p := &c.Providers[i]
		for j, k := range p.Keys {
			switch {
			case strings.HasPrefix(k, PrefixEnv):
				name := k[len(PrefixEnv):]
				if v := strings.TrimSpace(os.Getenv(name)); v != "" {
					p.Keys[j] = v
					rep.Resolved++
				} else {
					rep.Unresolved++
					rep.Details = append(rep.Details,
						fmt.Sprintf("%s[%d]: env:%s 未设置", p.ID, j, name))
				}

			case strings.HasPrefix(k, PrefixVault):
				path := k[len(PrefixVault):]
				if v, ok := cache[path]; ok {
					if v != "" {
						p.Keys[j] = v
						rep.Resolved++
					} else {
						rep.Unresolved++
						rep.Details = append(rep.Details,
							fmt.Sprintf("%s[%d]: vault:%s 此前已解析失败", p.ID, j, path))
					}
					continue
				}
				v, err := resolve(ctx, path)
				// 判据是"trim 后是否非空"，不是"有没有报错"：
				// vault get 成功但值为空时返回 ("", nil)，空串没有前缀、
				// IsPlaceholder 认不出来，会被当真实 key 收进池 → `Bearer ` → 401。
				if err != nil || strings.TrimSpace(v) == "" {
					cache[path] = "" // 负缓存：同一坏 path 不重复 exec
					rep.Unresolved++
					reason := "返回空值"
					if err != nil {
						reason = err.Error()
					}
					rep.Details = append(rep.Details,
						fmt.Sprintf("%s[%d]: vault:%s (%s)", p.ID, j, path, reason))
					continue
				}
				v = strings.TrimSpace(v)
				cache[path] = v
				p.Keys[j] = v
				rep.Resolved++

			default:
				rep.Resolved++ // 字面量 key，无需解析
			}
		}
	}
	return rep
}

// defaultVaultGet 经 exec 调本机 vault.exe 取明文。
//
// 守"纯标准库 · 零外部依赖"铁律：不引 vaultcli 库，镜像 internal/nats 经 exec
// 调 nats.cli 的既有做法。解密能力只存在于 vault 二进制内，本进程从不接触密码墙。
func defaultVaultGet(ctx context.Context, path string) (string, error) {
	bin := os.Getenv("VAULT_BIN")
	if bin == "" {
		bin = DefaultVaultBin
	}
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("%w: %s", ErrVaultUnavailable, bin)
	}

	cmd := exec.CommandContext(ctx, bin, "get", path)
	cmd.Stdin = nil // 绝不给子进程留可读 stdin——防不可预期的阻塞
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("vault get %s: %v (%s)", path, err, truncate(errBuf.String(), 200))
	}
	// vault get 不翻译换行，落库时用不带 -n 的 echo 可能带入 \r。
	// TrimSpace 比 TrimSuffix("\n") 宽——真实 API key 不含首尾空白，这样是安全的。
	return strings.TrimSpace(out.String()), nil
}

// truncate 截断过长的 stderr 摘要，避免日志被塞爆。
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (c *Config) validate() error {
	if c.Listen == "" {
		c.Listen = ":8462"
	}
	seen := make(map[string]bool, len(c.Providers))
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.ID == "" {
			return fmt.Errorf("provider %d: id required", i)
		}
		if seen[p.ID] {
			return fmt.Errorf("duplicate provider id: %s", p.ID)
		}
		seen[p.ID] = true
		if p.BaseURL == "" {
			return fmt.Errorf("provider %s: base_url required", p.ID)
		}
		if p.Format != FormatOpenAI && p.Format != FormatAnthropic {
			return fmt.Errorf("provider %s: invalid format %q (want openai|anthropic)", p.ID, p.Format)
		}
		if p.Tier != TierFree && p.Tier != TierPaid {
			return fmt.Errorf("provider %s: invalid tier %q (want free|paid)", p.ID, p.Tier)
		}
		if len(p.Keys) == 0 {
			return fmt.Errorf("provider %s: at least 1 key required", p.ID)
		}
	}
	return nil
}

// FreeProviders 返回免费层 provider（状态机：任一健康就走免费）。
//
// 注意返回的是**值拷贝**，但 Keys 这个 slice 与 c.Providers[i].Keys 共享底层数组——
// 调用方不得就地改写它（会同时改到配置本体与其他拷贝）。
func (c *Config) FreeProviders() []Provider {
	var out []Provider
	for _, p := range c.Providers {
		if p.Tier == TierFree {
			out = append(out, p)
		}
	}
	return out
}

// PaidProviders 返回付费兜底链（全免费不可用才进入）。
func (c *Config) PaidProviders() []Provider {
	var out []Provider
	for _, p := range c.Providers {
		if p.Tier == TierPaid {
			out = append(out, p)
		}
	}
	return out
}
