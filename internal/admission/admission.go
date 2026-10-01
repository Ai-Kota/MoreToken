// Package admission 消费一份**外部产出的能力准入选表**，回答"某模型在某能力维度上是否准入"。
//
// 表由**外部能力评估工具**产出（任何直打上游、能输出下述 JSON 形状的工具都行），
// **本仓不含评估器、也不依赖它存在** —— 文件不配就是 no-op。表格式以本包的
// [ModelAdmission] / [DimScore] 为准（**契约由消费端定义**，见"表格式"一节）。
//
// # 为什么需要它（ADR-004 §一/§四）
//
// 本网关的选型此前只读 **人工写下的 `kinds` 断言** —— 而 moretoken 自己的
// `internal/catalog/catalog.go` 就承认过："kinds 是能力断言，必须人工实测后再升格"。
// 结果就是：实测能编程的模型够不着、够得着也不会被选中（`auto:coding` 一直落到通用模型上）。
//
// 本包把"实测结论"接进选型：只有**在该维度上实测通过**的模型才进对应维度的池。
//
// # 为什么"池"是按维度分的
//
// 严格准入（全过才入池）若不分维度，池里每个模型都是满分 ⇒ `auto:coding` 无从"挑更会编程的"。
// 故准入是**按维度**的：某模型编程维度通过就进编程池、推理维度通过就进推理池，两池成员可不同。
// 这与"通过才入池"不矛盾 —— 只是"池"有两个。
//
// # 表格式（契约在此，不在产表方）
//
// 一个 JSON 数组，每项一条模型记录：
//
//	[
//	  {
//	    "model": "some-model-id",          // 必填：上游模型 id
//	    "base": "https://api.example.com", // 供 ApplyAdmission 按 base_url 归属到 provider
//	    "anthropic_ok": true,              // 必填：该模型经 Anthropic 协议实测可通
//	    "battery_verdict": "PASS",         // 整组题目的总判词
//	    "dims": {                          // 按维度的实测样本；缺维度 = 该维度未测 = 不准入
//	      "reasoning": { "passed": 5, "total": 5 },
//	      "coding":    { "passed": 4, "total": 5 }
//	    }
//	  }
//	]
//
// 字段名以上方标签为准。**改了字段名（如 `dims` → `dimensions`）不会被报错地拒收** ——
// 解析成功但每行样本为空 ⇒ 全判不准入 ⇒ 池被清空。故此情形被专门识别为
// "**有内容却一条都认不出**"并判**表不可用**（回退 kinds），而不是当作"全都不合格"。
//
// # 判据
//
//	某模型在某维度准入 ⟺ anthropic_ok ∧ 有实测样本（total > 0）∧ passed/total ≥ 准入线
//
// 准入线默认 **1.0（全过）**，可用 `admission_min_ratio` 放宽（见 [Table.SetMinRatio]）。
// `UNTESTED` / `INCOMPLETE` / `FAIL` / 表里没有该模型 —— **一律不准入**（ADR-004 §四"未测不入池"）。
//
// # 表缺失时的行为（**唯一的 fail-open**，且必须响亮）
//
// 表文件不存在 ≠ "所有模型都不合格"，而是"从未测过"。若因此让网关什么都不服务，
// 一次漏挂载就能把整条供给打死。故**表缺失时回退到既有行为（读 kinds）并打一行启动日志**，
// 且把状态暴露在 **/doctor** 上 —— 让"没在按实测选型"这件事**可见**，而不是静默降级。
//
// ⚠️ 是 /doctor 不是 /health：/health 是**数组**（各池状态），dev-fleet 的存活闸按数组解析，
// 往里塞对象会破坏契约；/doctor 本就是"网关状态如何"的对象端点，加字段安全。
package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Dim 能力维度名。取值即此处的两个字面量 —— **这就是契约本身**，不跟随任何外部仓的定义。
const (
	DimReasoning = "reasoning"
	DimCoding    = "coding"
)

// ModelAdmission 一个模型在一张表里的准入记录。
type ModelAdmission struct {
	Model          string              `json:"model"`
	Base           string              `json:"base"`
	AnthropicOK    bool                `json:"anthropic_ok"`
	BatteryVerdict string              `json:"battery_verdict"`
	Dims           map[string]DimScore `json:"dims"`
}

// DimScore 一个维度的实测得分。字段名与下方 json tag 即**表格式契约**（见包注释）。
type DimScore struct {
	Passed int `json:"passed"`
	Total  int `json:"total"`
}

// Table 一份已加载的准入选表。
type Table struct {
	mu       sync.RWMutex
	path     string
	byNormal map[string]ModelAdmission // 归一化后的模型名 → 记录
	loadedAt time.Time
	mtime    time.Time
	lastErr  string

	// minRatio 某维度的准入线：passed/total ≥ minRatio 才算过。默认 1.0（=全过，与旧行为一致）。
	//
	// 为什么做成可配（2026-10-01 用户定，方案 A）：题组只有 5 道，**多对一道就是 80% vs 100%** ——
	// "全过"在这么小的样本上把门槛设成了"完美或零"，实测导致**编程池为空**（最好的模型 4/5，
	// 于是 auto:coding 一个候选都没有）。用阈值比用"全过"更贴近真实能力，且决定权留给使用者。
	//
	// ⚠️ 默认仍是 1.0：**不配置就与接入前完全一致**，不静默放宽任何东西。
	minRatio float64
}

// normalizeModel 归一化模型名，消除跨仓书写差异（大小写、首尾空白）。
//
// ⚠️ 教训（本项目血账"跨系统比路径，正反斜杠/大小写差异让相等判据恒假"）：
// 跨仓比较的字符串一律先归一化，否则 `a == b` 会**静默恒假**、判据失效且失败方向是"永远不匹配"。
func normalizeModel(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Load 从磁盘加载准入选表。文件不存在/解析失败时返回的 Table 处于"空"状态
// （Admitted 一律返回 false），并记下 lastErr 供 /doctor 暴露。
//
// 调用方应据 Empty() 判断"表是否可用"，据此决定是严格过滤还是回退。
func Load(path string) *Table {
	t := &Table{path: path, byNormal: map[string]ModelAdmission{}, minRatio: 1.0}
	t.reload()
	return t
}

// SetMinRatio 设置准入线（passed/total ≥ r）。
//
// 取值必须是 (0, 1]：越界一律回落到 1.0（严格）——
// **宁可严不宁可松**：一个写错的阈值（比如 0）会静默放行整个池，那是这个机制最危险的失效方向。
func (t *Table) SetMinRatio(r float64) {
	if r <= 0 || r > 1 {
		r = 1.0
	}
	t.mu.Lock()
	t.minRatio = r
	t.mu.Unlock()
}

// MinRatio 当前准入线（供 /doctor 暴露 —— 一个"为什么 4/5 也过了"的问题必须能自己查到答案）。
func (t *Table) MinRatio() float64 {
	if t == nil {
		return 1.0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.minRatio <= 0 {
		return 1.0
	}
	return t.minRatio
}

// reload 重新读盘（持写锁）。解析成功才替换内容 —— 半截/坏文件不得清空在用的表。
func (t *Table) reload() {
	st, err := os.Stat(t.path)
	if err != nil {
		t.mu.Lock()
		t.byNormal = map[string]ModelAdmission{}
		t.lastErr = "读表失败: " + err.Error()
		t.mtime = time.Time{}
		t.mu.Unlock()
		return
	}
	raw, err := os.ReadFile(t.path)
	if err != nil {
		t.mu.Lock()
		t.lastErr = "读表失败: " + err.Error()
		t.mu.Unlock()
		return
	}
	var rows []ModelAdmission
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.mu.Lock()
		t.lastErr = "解析表失败: " + err.Error() // 保留旧内容，不因坏文件清空在用表
		t.mu.Unlock()
		return
	}
	m := make(map[string]ModelAdmission, len(rows))
	for _, r := range rows {
		m[normalizeModel(r.Model)] = r
	}

	// ── 健全性闸：有内容但**一条都认不出** ⇒ 视为表不可用（回退 + 可见）──
	//
	// 为什么必须有（2026-10-01 实测演示）：本包与产出方可隔着一个**没有版本号的跨仓契约**。
	// 实测把字段名 `dims` 改成 `dimensions`（模型名/维度名/分数全没变），解析**不报错**，
	// 但每行的 Dims 都是 nil ⇒ `admits` 全 false ⇒ 表非空 ⇒ 严格过滤 ⇒ **整个池被清空**、
	// `auto:*` 全部无候选报错，**且一声不响**。
	//
	// 这正是本项目最忌的形态：不是"降级"而是"静默归零"。故此处把"有内容却认不出"判为
	// **表不可用** —— 于是 Empty() 为真、路由回退到 kinds，并在 /doctor 与启动日志上明说。
	// 宁可退回旧行为，不可悄悄把池清空。
	recognizable := 0
	for _, r := range m {
		for _, d := range r.Dims {
			if d.Total > 0 {
				recognizable++
				break
			}
		}
	}
	if len(m) > 0 && recognizable == 0 {
		t.mu.Lock()
		t.byNormal = map[string]ModelAdmission{}
		t.loadedAt = time.Now()
		t.mtime = st.ModTime()
		t.lastErr = fmt.Sprintf(
			"表有 %d 条但无一条含可识别的维度样本（Total>0）——疑为格式/契约漂移；"+
				"按**不可用**处理并回退到 kinds（否则会静默清空整个池）", len(m))
		t.mu.Unlock()
		return
	}

	t.mu.Lock()
	t.byNormal = m
	t.loadedAt = time.Now()
	t.mtime = st.ModTime()
	t.lastErr = ""
	t.mu.Unlock()
}

// RefreshIfChanged 表文件 mtime 变了就重载。供路由前轻量调用（stat 一次）。
func (t *Table) RefreshIfChanged() {
	if t == nil || t.path == "" {
		return
	}
	st, err := os.Stat(t.path)
	if err != nil {
		return
	}
	t.mu.RLock()
	cur := t.mtime
	t.mu.RUnlock()
	if st.ModTime().Equal(cur) {
		return
	}
	t.reload()
}

// Watch 周期性检查表文件 mtime，变了就重载，直到 ctx 取消。
//
// 为什么必须有它（2026-10-01 自查抓到的"定义了没接线"）：
// RefreshIfChanged 当初**写了却没有任何调用点** —— 热加载只存在于函数名里，
// 表更新后必须重启容器才生效。**这类代码比没有更糟**：它让人以为机制在跑。
// 本函数就是那个缺失的调用点。
//
// interval 建议 ≥ 10s：准入表是分钟/小时级更新的产物，秒级轮询只是白耗 syscall。
func (t *Table) Watch(ctx context.Context, interval time.Duration) {
	if t == nil || t.path == "" || interval <= 0 {
		return
	}
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			t.RefreshIfChanged()
		}
	}
}

// Empty 表是否为空（未加载成功、或一个可准入的模型都没有）。
func (t *Table) Empty() bool {
	if t == nil {
		return true
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.byNormal) == 0
}

// Err 返回最近一次加载错误（供 /doctor 暴露；空串表示正常）。
func (t *Table) Err() string {
	if t == nil {
		return ""
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.lastErr
}

// Admitted 某模型在指定维度上是否准入。**严格**：必须实测过、覆盖完整、且该维度全通过。
//
// 两道判据缺一不可：
//
//	① **整行覆盖完整**（verdict 不是 INCOMPLETE/UNTESTED/空）
//	② 该维度 passed == total 且 total > 0
//
// ① 为什么必须有（2026-10-01 联调时抓到的洞）：只看 ② 的话，一个**分母被缩过水**的维度
// 会假通过 —— 实测数据里 `agnes-2.5-flash` 的编程是 `3/3`，但那是 5 道题里**有 2 道未测成
// 被排除在分母外**的结果。`3/3 == passed==total` ⇒ 会被当成"编程满分"放行，
// 而这恰恰是本包要堵的"分母不可比"。整行覆盖不完整 ⇒ **整行不作数**。
//
// 覆盖完整之后，② 才是有意义的横向比较：`reasoning 5/5, coding 4/5` 的模型
// 进推理池、不进编程池 —— 这正是"按维度选型"能产生区分度的来源。
func (t *Table) Admitted(model, dim string) bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	r, ok := t.byNormal[normalizeModel(model)]
	return ok && r.admits(dim, t.minRatio)
}

// admits 是准据的**唯一实现** —— Admitted 与 Stats 都必须走它。
//
// ⚠️ 为什么强调"唯一"（2026-10-01 联调实测抓到的 bug）：Stats 起初自己写了一份简化判据
// （只查 dims 不查 verdict 覆盖），于是启动日志报 `reasoning 准入 2` 而实际只有 1 ——
// **报出来的数与真实行为不符**。两个入口各写一份判据，迟早会分叉。
func (r ModelAdmission) admits(dim string, minRatio float64) bool {
	if !r.AnthropicOK {
		return false
	}
	// ① 整行覆盖完整：PASS/FAIL/UNCERTAIN 都表示"每道题都答上了"（区别只在答对多少），分母可比；
	// INCOMPLETE（部分测成）/ UNTESTED（全未测成）/ 空 —— 覆盖不全 ⇒ 整行不作数。
	switch r.BatteryVerdict {
	case "PASS", "FAIL", "UNCERTAIN":
	default:
		return false
	}
	// ② 该维度实测过，且通过率 ≥ 准入线
	d, ok := r.Dims[dim]
	if !ok || d.Total <= 0 {
		return false
	}
	if minRatio <= 0 {
		minRatio = 1.0
	}
	// 用乘法比浮点除法稳：passed/total ≥ r  ⟺  passed ≥ r*total
	//
	// ⚠️ 必须带容差：`0.8` 在 float64 里是 0.80000000000000004…，
	// 阈值 0.8 × 5 题 = 4.0000000000000002 > 4 ⇒ **`4 >= 4.000…2` 为 false**，
	// 于是"配了 80% 却把 4/5 判掉"。这是典型的浮点假阴性：判据静默变严、且方向是"永远差一点"。
	const eps = 1e-9
	return float64(d.Passed)+eps >= minRatio*float64(d.Total)
}

// Passes 某条记录在给定准入线下是否**该维度准入**（导出给 config 合并用，判据同 admits）。
func (r ModelAdmission) Passes(dim string, minRatio float64) bool {
	return r.admits(dim, minRatio)
}

// Rows 返回全部记录的快照（顺序不保证）。
//
// 给"把实测结论并进配置"用（见 config.ApplyAdmission）：那里既要 model/dims 判准入，
// 也要 base 做 provider 归属映射 —— 故不能只暴露"名字集合"。
func (t *Table) Rows() []ModelAdmission {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]ModelAdmission, 0, len(t.byNormal))
	for _, r := range t.byNormal {
		out = append(out, r)
	}
	return out
}

// Stats 汇总，供 /doctor 展示"准入表是否在起作用"。
func (t *Table) Stats() (models int, reasoning int, coding int, err string) {
	if t == nil {
		return 0, 0, 0, "table nil"
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, r := range t.byNormal {
		models++
		// 走 admits()，与 Admitted() 同一判据 —— 否则报出来的数与真实行为会分叉（见 admits 注释）。
		if r.admits(DimReasoning, t.minRatio) {
			reasoning++
		}
		if r.admits(DimCoding, t.minRatio) {
			coding++
		}
	}
	return models, reasoning, coding, t.lastErr
}
