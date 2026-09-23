package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 自动纳新清单的合并契约（T-018）。
//
// 动机：免费池是主力，而供给是**易腐资源**——上游撤模型、撤免费、上新模型都在我们配置之外发生。
// 纯人工维护只会单调衰减，补回来的恢复时间常数是**无界**的。纳新让它有界。
//
// 但"自动进来的东西"必须受两条硬约束，否则它会变成新的风险源：
//   - 只追加：不许抢占手写声明的偏好位（否则一次纳新就能把主力模型换掉）
//   - kinds 只给 general：实测能证明"它活着"，证明不了"它会推理"

// TestMergeAuto_AppendsAfterHandwritten —— 手写的排前面，自动的排后面，顺序即偏好。
func TestMergeAuto_AppendsAfterHandwritten(t *testing.T) {
	cfg := &Config{Providers: []Provider{{
		ID: "p", Keys: []string{"k"},
		Models: []Model{{ID: "preferred", Kinds: []string{"reasoning"}}},
	}}}
	inv := &AutoInventory{Providers: map[string]AutoProvider{
		"p": {Verified: []Model{{ID: "discovered"}, {ID: "another"}}},
	}}

	added := MergeAuto(cfg, inv)

	if added != 2 {
		t.Fatalf("added = %d, want 2", added)
	}
	got := []string{}
	for _, m := range cfg.Providers[0].Models {
		got = append(got, m.ID)
	}
	want := []string{"preferred", "discovered", "another"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("模型顺序 = %v, want %v —— 自动的绝不能排在手写的前面", got, want)
		}
	}
}

// TestMergeAuto_ForcesGeneralKind —— 清单里写 kinds 也不算数。
//
// 把它做成**结构约束**而不是惯例，是因为惯例会被下一次"顺手改一下生成文件"破掉；
// 而"自动纳进来的模型直接宣称自己能推理"是这台机器上最贵的一类错误。
func TestMergeAuto_ForcesGeneralKind(t *testing.T) {
	cfg := &Config{Providers: []Provider{{ID: "p", Keys: []string{"k"}}}}
	inv := &AutoInventory{Providers: map[string]AutoProvider{
		"p": {Verified: []Model{{ID: "x", Kinds: []string{"reasoning", "coding"}}}},
	}}

	MergeAuto(cfg, inv)

	got := cfg.Providers[0].Models[0].Kinds
	if len(got) != 1 || got[0] != "general" {
		t.Errorf("kinds = %v, want [general] —— 清单文件说了不算，实测只能证明'它活着'", got)
	}
}

// TestMergeAuto_NoDuplicateAndIgnoresUnknownProvider —— 不重复追加；清单里有、config 里没的 provider 直接忽略。
func TestMergeAuto_NoDuplicateAndIgnoresUnknownProvider(t *testing.T) {
	cfg := &Config{Providers: []Provider{{
		ID: "p", Keys: []string{"k"}, Models: []Model{{ID: "a"}},
	}}}
	inv := &AutoInventory{Providers: map[string]AutoProvider{
		"p":             {Verified: []Model{{ID: "a"}, {ID: "b"}}},
		"not-in-config": {Verified: []Model{{ID: "ghost"}}},
	}}

	if added := MergeAuto(cfg, inv); added != 1 {
		t.Errorf("added = %d, want 1（a 已存在、ghost 无对应 provider）", added)
	}
	if n := len(cfg.Providers[0].Models); n != 2 {
		t.Errorf("模型数 = %d, want 2", n)
	}
}

// TestLoadAuto_MissingIsNilNotError —— 老部署没有这个生成物，必须退化成"与以前完全一致"。
func TestLoadAuto_MissingIsNilNotError(t *testing.T) {
	inv, err := LoadAuto(t.TempDir())
	if err != nil {
		t.Fatalf("缺文件不该报错（否则老部署起不来）：%v", err)
	}
	if inv != nil {
		t.Errorf("缺文件应返回 nil，实际 %+v", inv)
	}
}

// TestLoadAuto_MalformedIsError —— 文件在、但坏了 ⇒ 必须响。
//
// 静默忽略它等于把"纳新从未生效"藏起来——T-014 的坑：三层全挂一声不吭。
// 缺文件与坏文件是**两件事**，不能合并处理。
func TestLoadAuto_MalformedIsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, AutoFile), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuto(dir); err == nil {
		t.Fatal("清单损坏必须报错，不能静默吞掉")
	}
}

// TestLoad_MergesAutoFromSameDir —— 端到端：Load 读 config.json 时把同目录的清单并进来。
func TestLoad_MergesAutoFromSameDir(t *testing.T) {
	cfg, err := Load("testdata/auto/config.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.AutoAdded != 2 { // discovered + dup（preferred 已在手写区）
		t.Errorf("AutoAdded = %d, want 2 —— '纳新到底生没生效'必须能被看见", cfg.AutoAdded)
	}
	ids := []string{}
	for _, m := range cfg.Providers[0].Models {
		ids = append(ids, m.ID)
	}
	if len(ids) != 3 || ids[0] != "preferred" {
		t.Errorf("合并后模型 = %v，want [preferred discovered dup]", ids)
	}
	if cfg.Providers[0].Models[1].Kinds[0] != "general" {
		t.Errorf("自动进来的模型 kinds 应被强制为 general，实际 %v", cfg.Providers[0].Models[1].Kinds)
	}
}

// TestLoad_NoAutoFileIsUnchanged —— 没有清单时，Load 的行为与加这个机制之前**完全一致**。
func TestLoad_NoAutoFileIsUnchanged(t *testing.T) {
	cfg, err := Load("testdata/gap-config.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AutoAdded != 0 {
		t.Errorf("AutoAdded = %d, want 0", cfg.AutoAdded)
	}
	if len(cfg.Providers[0].Models) != 1 {
		t.Errorf("模型数 = %d, want 1（无清单 = 零行为变化）", len(cfg.Providers[0].Models))
	}
}
