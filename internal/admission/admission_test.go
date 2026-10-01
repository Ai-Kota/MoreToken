package admission

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTable(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "admitted.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("写表失败: %v", err)
	}
	return p
}

// TestAdmitted_StrictPerDimension 钉住准入的**严格**语义与**按维度**语义：
//   - 某维度准入 ⟺ 该维度实测过（total>0）且全通过（passed==total）；
//   - 两个维度独立 —— 编程满分不代表推理准入，反之亦然。
//
// 为什么按维度：严格准入若不分维度，池里人人满分 ⇒ `auto:coding` 无从"挑更会编程的"。
func TestAdmitted_StrictPerDimension(t *testing.T) {
	p := writeTable(t, `[
	  {"model":"good-both","anthropic_ok":true,"battery_verdict":"PASS",
	   "dims":{"reasoning":{"passed":5,"total":5},"coding":{"passed":5,"total":5}}},
	  {"model":"coder-only","anthropic_ok":true,"battery_verdict":"UNCERTAIN",
	   "dims":{"reasoning":{"passed":4,"total":5},"coding":{"passed":5,"total":5}}},
	  {"model":"reasoner-only","anthropic_ok":true,"battery_verdict":"UNCERTAIN",
	   "dims":{"reasoning":{"passed":5,"total":5},"coding":{"passed":3,"total":5}}},
	  {"model":"probe-failed","anthropic_ok":false,"battery_verdict":"",
	   "dims":{"reasoning":{"passed":5,"total":5},"coding":{"passed":5,"total":5}}},
	  {"model":"untested","anthropic_ok":true,"battery_verdict":"UNTESTED","dims":{}},
	  {"model":"incomplete","anthropic_ok":true,"battery_verdict":"INCOMPLETE",
	   "dims":{"reasoning":{"passed":5,"total":5},"coding":{"passed":2,"total":5}}}
	]`)
	tab := Load(p)
	if tab.Empty() {
		t.Fatal("表非空，Empty() 不应为 true")
	}

	cases := []struct {
		model, dim string
		want       bool
		why        string
	}{
		{"good-both", DimCoding, true, "两维全过"},
		{"good-both", DimReasoning, true, "两维全过"},
		{"coder-only", DimCoding, true, "编程满分 ⇒ 进编程池"},
		{"coder-only", DimReasoning, false, "推理 4/5 ⇒ 不进推理池（维度必须独立）"},
		{"reasoner-only", DimReasoning, true, "推理满分 ⇒ 进推理池"},
		{"reasoner-only", DimCoding, false, "编程 3/5 ⇒ 不进编程池"},
		{"probe-failed", DimCoding, false, "协议闸没过 ⇒ 成绩再高也不准入"},
		{"untested", DimCoding, false, "UNITTESTED：没测过不得当合格（未测不入池）"},
		{"untested", DimReasoning, false, "同上"},
		{"incomplete", DimCoding, false, "INCOMPLETE：测不全不得当合格"},
		{"incomplete", DimReasoning, false, "INCOMPLETE ⇒ **整行不作数**（该行 reasoning 5/5 看着达标，但它整体覆盖不全、分母不可比）"},
		{"never-heard-of-it", DimCoding, false, "表里没有 ⇒ 不准入"},
	}
	for _, c := range cases {
		if got := tab.Admitted(c.model, c.dim); got != c.want {
			t.Errorf("Admitted(%q,%q) = %v，want %v —— %s", c.model, c.dim, got, c.want, c.why)
		}
	}
}

// TestAdmitted_NormalizesModelName：跨仓比较的字符串必须归一化，
// 否则大小写/空白差异会让判据**静默恒假**（本项目"比路径"血账的同一类错）。
func TestAdmitted_NormalizesModelName(t *testing.T) {
	p := writeTable(t, `[{"model":"Qwen/Qwen3-Coder-Plus:Free","anthropic_ok":true,
	  "battery_verdict":"PASS","dims":{"coding":{"passed":5,"total":5}}}]`)
	tab := Load(p)
	for _, name := range []string{
		"Qwen/Qwen3-Coder-Plus:Free",
		"qwen/qwen3-coder-plus:free",
		"  QWEN/QWEN3-CODER-PLUS:FREE  ",
	} {
		if !tab.Admitted(name, DimCoding) {
			t.Errorf("归一化后应命中，未命中: %q", name)
		}
	}
}

// TestLoad_MissingFileIsEmptyNotFatal：表缺失 ⇒ 空表 + 有 Err，**不 panic**。
// 路由据 Empty() 回退到既有行为（fail-open），且状态可见 —— 见包注释。
func TestLoad_MissingFileIsEmptyNotFatal(t *testing.T) {
	tab := Load(filepath.Join(t.TempDir(), "nope.json"))
	if !tab.Empty() {
		t.Error("表缺失应视为空表")
	}
	if tab.Err() == "" {
		t.Error("表缺失应在 Err() 上可见（不得静默）")
	}
	if tab.Admitted("any", DimCoding) {
		t.Error("空表下不得准入任何模型")
	}
}

// TestReload_BadFileKeepsPrevious：坏文件**不得清空在用的表** ——
// 半截写入（moretoken 每轮重写该文件）期间若清了表，会出现"一秒全池清空"的抖动。
func TestReload_BadFileKeepsPrevious(t *testing.T) {
	p := writeTable(t, `[{"model":"m","anthropic_ok":true,"battery_verdict":"PASS",
	  "dims":{"coding":{"passed":5,"total":5}}}]`)
	tab := Load(p)
	if !tab.Admitted("m", DimCoding) {
		t.Fatal("初始表应命中")
	}
	// 写入半截 JSON 后触发重载
	if err := os.WriteFile(p, []byte(`[{"model":"m",`), 0o644); err != nil {
		t.Fatal(err)
	}
	tab.RefreshIfChanged()
	if tab.Empty() {
		t.Error("坏文件不得清空在用的表（应保留上一次成功解析的内容）")
	}
	if !tab.Admitted("m", DimCoding) {
		t.Error("坏文件后，旧内容仍应命中")
	}
	if tab.Err() == "" {
		t.Error("解析失败应在 Err() 上可见")
	}
}

// TestReload_FormatDriftFallsBackNotEmptiesPool 是**跨仓契约漂移**的回归闸。
//
// ⚠️ 实测演示（2026-10-01）：把字段名 `dims` 改成 `dimensions`（模型名、维度名、分数**全没变**），
// JSON **解析不报错**，但每行 Dims 都是 nil ⇒ `admits` 全 false ⇒ 表非空 ⇒ 严格过滤
// ⇒ **整个池被清空**、`auto:*` 全部无候选报错，**且一声不响**。
//
// 这不是"降级"而是"静默归零" —— 本机制能造成的最坏后果。故"有内容却一条都认不出"
// 必须判为**表不可用**（回退 kinds），而不是当作"所有模型都不合格"。
//
// 为什么这条对"moretoken 必须能独立使用"尤其重要：两个仓之间只靠这个**没有版本号的契约**
// 相连。契约漂移是唯一能穿透独立性的失效路径，必须在**消费端**兜住。
func TestReload_FormatDriftFallsBackNotEmptiesPool(t *testing.T) {
	// 字段名漂移：dims -> dimensions
	p := writeTable(t, `[{"model":"good-model","anthropic_ok":true,"battery_verdict":"PASS",
	  "dimensions":{"reasoning":{"passed":5,"total":5},"coding":{"passed":5,"total":5}}}]`)
	tab := Load(p)

	if !tab.Empty() {
		t.Fatal("格式漂移的表必须被判为**不可用**（Empty=true ⇒ 路由回退 kinds）；" +
			"否则它会静默把整个池清空 —— 比不接更糟")
	}
	if tab.Err() == "" {
		t.Error("格式漂移必须在 Err() 上可见（不得静默）")
	}
	// 回退语义：路由看 Empty() 为真就不做准入过滤 —— 这里断言判据本身没把模型误判为"不合格"
	if tab.Admitted("good-model", DimCoding) {
		t.Error("表不可用时 Admitted 应为 false（不参与过滤）")
	}
}

// TestReload_PartialDriftStillUsable：**部分**行可识别时不该整表作废 ——
// 只有当**一条都认不出**时才判不可用。半个坏表好过没有表，且误判作废会把好模型也挡掉。
func TestReload_PartialDriftStillUsable(t *testing.T) {
	p := writeTable(t, `[
	  {"model":"ok-model","anthropic_ok":true,"battery_verdict":"PASS",
	   "dims":{"coding":{"passed":5,"total":5}}},
	  {"model":"drifted-model","anthropic_ok":true,"battery_verdict":"PASS",
	   "dimensions":{"coding":{"passed":5,"total":5}}}
	]`)
	tab := Load(p)
	if tab.Empty() {
		t.Fatal("只要有一条可识别，表就应可用（不得因个别行漂移整表作废）")
	}
	if !tab.Admitted("ok-model", DimCoding) {
		t.Error("可识别的那条应正常准入")
	}
	if tab.Admitted("drifted-model", DimCoding) {
		t.Error("漂移那条认不出 ⇒ 不准入（但不应连累整表）")
	}
}

// TestMinRatio_ConfigurableBar 钉住"准入线可配"（2026-10-01 用户定，方案 A）。
//
// 背景：题组只有 5 道，多对一道就是 80% vs 100%。"全过"在小样本上等于"完美或零"——
// 实测导致**编程池为空**（最好的模型 4/5）。阈值让"多严"成为可调的决定。
func TestMinRatio_ConfigurableBar(t *testing.T) {
	p := writeTable(t, `[
	  {"model":"perfect","anthropic_ok":true,"battery_verdict":"PASS",
	   "dims":{"coding":{"passed":5,"total":5}}},
	  {"model":"four","anthropic_ok":true,"battery_verdict":"UNCERTAIN",
	   "dims":{"coding":{"passed":4,"total":5}}},
	  {"model":"three","anthropic_ok":true,"battery_verdict":"UNCERTAIN",
	   "dims":{"coding":{"passed":3,"total":5}}}
	]`)

	// ① 默认（不设置）= 1.0 严格 ⇒ **与接入前行为一致**，只有满分准入
	def := Load(p)
	if !def.Admitted("perfect", DimCoding) {
		t.Error("默认阈值下满分应准入")
	}
	if def.Admitted("four", DimCoding) {
		t.Error("默认阈值（1.0）下 4/5 不得准入 —— 默认必须与旧行为一致")
	}
	if def.MinRatio() != 1.0 {
		t.Errorf("默认准入线应为 1.0，got %v", def.MinRatio())
	}

	// ② 配 0.8 ⇒ 4/5 准入、3/5 不许
	loose := Load(p)
	loose.SetMinRatio(0.8)
	if !loose.Admitted("four", DimCoding) {
		t.Error("阈值 0.8 下 4/5 应准入（这是方案 A 的目的）")
	}
	if loose.Admitted("three", DimCoding) {
		t.Error("阈值 0.8 下 3/5（60%）不该准入")
	}
}

// TestMinRatio_FloatBoundaryIsInclusive 是**浮点边界**的回归闸。
//
// ⚠️ 血账（2026-10-01 实现时发现）：`0.8` 在 float64 里是 0.80000000000000004…，
// 阈值 × 5 题 = **4.0000000000000002** ⇒ 朴素的 `4 >= 4.000…2` 为 **false**，
// 于是"配了 80% 却把 4/5 判掉"。判据静默变严、方向是"永远差一点"（与"跨系统比字符串"
// 那类血账同型：失败方向恒定且不显眼）。修法是加容差；本测试钉住它不许回退。
func TestMinRatio_FloatBoundaryIsInclusive(t *testing.T) {
	p := writeTable(t, `[{"model":"m","anthropic_ok":true,"battery_verdict":"UNCERTAIN",
	  "dims":{"coding":{"passed":4,"total":5}}}]`)
	tab := Load(p)
	tab.SetMinRatio(0.8)
	if !tab.Admitted("m", DimCoding) {
		t.Fatal("阈值 0.8 × 5 题恰好等于 4/5，**边界必须含等号** —— " +
			"浮点乘法会算出 4.0000000000000002，不加容差就会把 4/5 判掉")
	}
}

// TestMinRatio_OutOfRangeFallsBackToStrict：越界阈值一律回落 1.0。
//
// **宁可严不宁可松**：一个写错的阈值（比如 0）会静默放行整个池 —— 那是本机制最危险的失效方向。
func TestMinRatio_OutOfRangeFallsBackToStrict(t *testing.T) {
	p := writeTable(t, `[{"model":"m","anthropic_ok":true,"battery_verdict":"UNCERTAIN",
	  "dims":{"coding":{"passed":1,"total":5}}}]`)
	for _, bad := range []float64{0, -1, 1.5, 100} {
		tab := Load(p)
		tab.SetMinRatio(bad)
		if tab.MinRatio() != 1.0 {
			t.Errorf("越界阈值 %v 应回落 1.0，got %v", bad, tab.MinRatio())
		}
		if tab.Admitted("m", DimCoding) {
			t.Errorf("越界阈值 %v 下不得放行 1/5", bad)
		}
	}
}

// TestMinRatio_StatsAgreesUnderCustomBar：不变式在**非默认阈值**下仍要成立。
//
// 默认阈值那版不变式（TestStatsAgreesWithAdmitted）覆盖不到这条 —— 而阈值恰恰是
// 最容易让两个入口分叉的地方（一个漏传 minRatio 就会各算各的）。
func TestMinRatio_StatsAgreesUnderCustomBar(t *testing.T) {
	p := writeTable(t, `[
	  {"model":"perfect","anthropic_ok":true,"battery_verdict":"PASS",
	   "dims":{"coding":{"passed":5,"total":5}}},
	  {"model":"four","anthropic_ok":true,"battery_verdict":"UNCERTAIN",
	   "dims":{"coding":{"passed":4,"total":5}}},
	  {"model":"incomplete","anthropic_ok":true,"battery_verdict":"INCOMPLETE",
	   "dims":{"coding":{"passed":5,"total":5}}}
	]`)
	tab := Load(p)
	tab.SetMinRatio(0.8)

	_, _, coding, _ := tab.Stats()
	want := 0
	for _, m := range []string{"perfect", "four", "incomplete"} {
		if tab.Admitted(m, DimCoding) {
			want++
		}
	}
	if coding != want {
		t.Fatalf("非默认阈值下 Stats 与 Admitted 分叉：Stats=%d 逐个数得 %d", coding, want)
	}
	// 具体期望：perfect(5/5) ✓、four(4/5≥80%) ✓、incomplete 覆盖不全 ✗ ⇒ 2
	if want != 2 {
		t.Errorf("阈值 0.8 下应有 perfect 与 four 两个准入，got %d", want)
	}
}

// TestWatch_PicksUpTableChange 证明**热加载真的生效**（不只是"函数被调用了"）。
//
// 为什么要这条（2026-10-01 自查）：RefreshIfChanged 当初写了却没接线 ——
// 热加载只存在于函数名里。本测试从"文件改了 → 表跟着变"这个**可观测结果**上钉住它，
// 而不是断言"某个函数被调用"（那是对实现的重述，不是对行为的验证）。
func TestWatch_PicksUpTableChange(t *testing.T) {
	p := writeTable(t, `[{"model":"m1","anthropic_ok":true,"battery_verdict":"PASS",
	  "dims":{"coding":{"passed":5,"total":5}}}]`)
	tab := Load(p)
	if !tab.Admitted("m1", DimCoding) || tab.Admitted("m2", DimCoding) {
		t.Fatal("初始表应只准入 m1")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tab.Watch(ctx, 20*time.Millisecond)

	// 换一张表：m1 不再准入、m2 准入。mtime 需变化 —— 显式改一下避免同秒写入。
	time.Sleep(1100 * time.Millisecond)
	if err := os.WriteFile(p, []byte(`[{"model":"m2","anthropic_ok":true,"battery_verdict":"PASS",
	  "dims":{"coding":{"passed":5,"total":5}}}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if tab.Admitted("m2", DimCoding) && !tab.Admitted("m1", DimCoding) {
			return // 热加载生效
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("Watch 未把表更新带进来：m1=%v m2=%v（热加载没生效）",
		tab.Admitted("m1", DimCoding), tab.Admitted("m2", DimCoding))
}

// TestWatch_StopsOnCancel：ctx 取消后应停止（不留后台 goroutine）。
func TestWatch_StopsOnCancel(t *testing.T) {
	tab := Load(writeTable(t, `[]`))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { tab.Watch(ctx, 10*time.Millisecond); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消 ctx 后 Watch 未退出（会泄漏 goroutine）")
	}
}

// TestStats 汇总口径：只数"该维度实测通过且协议闸通过"的模型。
func TestStats(t *testing.T) {
	p := writeTable(t, `[
	  {"model":"a","anthropic_ok":true,"battery_verdict":"PASS",
	   "dims":{"reasoning":{"passed":5,"total":5},"coding":{"passed":5,"total":5}}},
	  {"model":"b","anthropic_ok":true,"battery_verdict":"UNCERTAIN",
	   "dims":{"reasoning":{"passed":4,"total":5},"coding":{"passed":5,"total":5}}},
	  {"model":"c","anthropic_ok":false,"battery_verdict":"PASS",
	   "dims":{"reasoning":{"passed":5,"total":5},"coding":{"passed":5,"total":5}}}
	]`)
	// 逐条算一遍（免得期望值本身写错）：
	//   a: 两维皆 5/5、协议闸过        → reasoning ✓ coding ✓
	//   b: reasoning 4/5（不达标）、coding 5/5、协议闸过 → reasoning ✗ coding ✓
	//   c: 两维 5/5 但**协议闸没过**   → 两个维度都不进（成绩再高也不准入）
	n, r, c, errStr := Load(p).Stats()
	if n != 3 {
		t.Errorf("模型数应 3，got %d", n)
	}
	if r != 1 {
		t.Errorf("reasoning 准入应 1（只有 a），got %d", r)
	}
	if c != 2 {
		t.Errorf("coding 准入应 2（a 与 b），got %d", c)
	}
	if errStr != "" {
		t.Errorf("正常表不应有 Err，got %q", errStr)
	}
}

// TestStatsAgreesWithAdmitted 是一条**不变式**：Stats 报出来的数与 Admitted 的实际行为必须一致。
//
// 为什么单独钉：2026-10-01 联调实测抓到过它们分叉 —— Stats 自己写了份简化判据（漏了覆盖检查），
// 于是启动日志报 `reasoning 准入 2` 而实际只有 1。**报出来的数会骗人，比不报更糟。**
func TestStatsAgreesWithAdmitted(t *testing.T) {
	p := writeTable(t, `[
	  {"model":"full","anthropic_ok":true,"battery_verdict":"PASS",
	   "dims":{"reasoning":{"passed":5,"total":5},"coding":{"passed":5,"total":5}}},
	  {"model":"uncertain","anthropic_ok":true,"battery_verdict":"UNCERTAIN",
	   "dims":{"reasoning":{"passed":5,"total":5},"coding":{"passed":4,"total":5}}},
	  {"model":"incomplete","anthropic_ok":true,"battery_verdict":"INCOMPLETE",
	   "dims":{"reasoning":{"passed":5,"total":5},"coding":{"passed":5,"total":5}}},
	  {"model":"untested","anthropic_ok":true,"battery_verdict":"UNTESTED","dims":{}}
	]`)
	tab := Load(p)
	_, r, c, _ := tab.Stats()

	// 逐个模型数一遍，与 Stats 报的数对照
	wantR, wantC := 0, 0
	for _, m := range []string{"full", "uncertain", "incomplete", "untested"} {
		if tab.Admitted(m, DimReasoning) {
			wantR++
		}
		if tab.Admitted(m, DimCoding) {
			wantC++
		}
	}
	if r != wantR || c != wantC {
		t.Fatalf("Stats 与 Admitted 分叉：Stats 报 reasoning=%d coding=%d，逐个数得 %d/%d",
			r, c, wantR, wantC)
	}
	// 顺带钉住具体期望：
	//   full      : 两维皆 5/5、覆盖完整 → 都进
	//   uncertain : 推理 5/5 ✓、编程 4/5 ✗（覆盖完整但编程不达标）→ 只进推理
	//   incomplete: 覆盖不全 → 一个都不进（哪怕两维数字看着都是 5/5）
	//   untested  : 同上，一个都不进
	if wantR != 2 || wantC != 1 {
		t.Errorf("按 fixtures 应有 reasoning=2(full,uncertain) coding=1(full)，得 %d/%d", wantR, wantC)
	}
}
