package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"moretoken/internal/admission"
	"moretoken/internal/config"
	"moretoken/internal/provider"
)

// 任务 20 的验收：`auto:coding` 必须落到**实测编程通过**的模型上，而不是随便一个带 coding 标签的模型。
//
// 背景（ADR-004 §四）：选型此前只读**人工写的 kinds**，于是"够得着的编码模型"与
// "被选中的模型"是两回事 —— 实测能编程的模型一直闲着。本测试把"实测结论真的驱动选型"钉死。

func writeAdmission(t *testing.T, body string) *admission.Table {
	t.Helper()
	p := filepath.Join(t.TempDir(), "admitted.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return admission.Load(p)
}

// TestRoute_VirtualModel_AdmissionPicksMeasuredModel：两个模型都带 coding 标签，
// 但只有 `real-coder` 实测编程通过 ⇒ 必须选它。
func TestRoute_VirtualModel_AdmissionPicksMeasuredModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{{
		ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI,
		Tier: config.TierFree, Keys: []string{"sk-a"},
		Models: []config.Model{
			// 排在前面：按旧的 kinds 逻辑它会被选中（配置顺序即偏好顺序）
			{ID: "general-coder", Kinds: []string{"coding", "general"}},
			{ID: "real-coder", Kinds: []string{"coding"}},
		},
	}}}

	// 只有 real-coder 编程维度实测通过
	tab := writeAdmission(t, `[
	  {"model":"general-coder","anthropic_ok":true,"battery_verdict":"UNCERTAIN",
	   "dims":{"coding":{"passed":2,"total":5}}},
	  {"model":"real-coder","anthropic_ok":true,"battery_verdict":"PASS",
	   "dims":{"coding":{"passed":5,"total":5}}}
	]`)

	r := New(cfg, WithClient(http.DefaultClient), WithAdmission(tab))
	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"auto:coding"}`),
		Kind: provider.NonStreaming, VirtualModel: true, WantKind: "coding",
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if res.Model != "real-coder" {
		t.Errorf("应选实测通过的 real-coder（general-coder 实测 2/5 应被排除），got %q", res.Model)
	}
}

// TestRoute_VirtualModel_AdmissionBlocksAll：该维度无人实测通过 ⇒ 不选任何模型。
//
// 这是"未测不入池（严格）"的直接后果：宁可没有候选（报错、由上层换 provider），
// 也不把没依据的模型当成合格品发出去。
func TestRoute_VirtualModel_AdmissionBlocksAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("不该真的发出请求 —— 准入表里没有该维度通过的模型")
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{{
		ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI,
		Tier: config.TierFree, Keys: []string{"sk-a"},
		Models: []config.Model{{ID: "general-coder", Kinds: []string{"coding"}}},
	}}}
	// 编程只测了 2/5（未达标）
	tab := writeAdmission(t, `[{"model":"general-coder","anthropic_ok":true,
	  "dims":{"coding":{"passed":2,"total":5}}}]`)

	r := New(cfg, WithClient(http.DefaultClient), WithAdmission(tab))
	if _, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"auto:coding"}`),
		Kind: provider.NonStreaming, VirtualModel: true, WantKind: "coding",
	}); err == nil {
		t.Fatal("该维度无准入模型时应报错，而不是随便发一个")
	}
}

// TestRoute_VirtualModel_EmptyAdmissionFallsBack：**表不可用时回退到 kinds**（fail-open）。
//
// 为什么允许 fail-open：表缺失 ≠ "所有模型都不合格"，而是"从未测过"。
// 若据此让网关什么都不服务，一次漏挂载就能把整条供给打死。
// ⚠️ 代价是"以为在按实测选型、其实没有"的静默降级风险 —— 故可见性由 main 的启动日志
//
//	与 **/doctor** 的 admission 字段承担，不靠这里。
func TestRoute_VirtualModel_EmptyAdmissionFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{{
		ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI,
		Tier: config.TierFree, Keys: []string{"sk-a"},
		Models: []config.Model{{ID: "general-coder", Kinds: []string{"coding"}}},
	}}}
	tab := writeAdmission(t, `[]`) // 空表 = 不可用

	r := New(cfg, WithClient(http.DefaultClient), WithAdmission(tab))
	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"auto:coding"}`),
		Kind: provider.NonStreaming, VirtualModel: true, WantKind: "coding",
	})
	if err != nil {
		t.Fatalf("空表应回退到 kinds 而非全拒: %v", err)
	}
	if res.Model != "general-coder" {
		t.Errorf("空表回退时应按 kinds 选中 general-coder，got %q", res.Model)
	}
}

// TestRoute_LiteralModelNotFilteredByAdmission：**字面模型不做准入过滤**。
//
// 准入管的是"我们替你挑"的那个池（auto:*）；调用方**点名**要某个模型是它的显式选择，
// 网关不该越权否决 —— 否则一句 `model: <我方未测模型>` 会莫名其妙被拒。
func TestRoute_LiteralModelNotFilteredByAdmission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{{
		ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI,
		Tier: config.TierFree, Keys: []string{"sk-a"},
		Models: []config.Model{{ID: "untested-model", Kinds: []string{"coding"}}},
	}}}
	tab := writeAdmission(t, `[{"model":"other","anthropic_ok":true,
	  "dims":{"coding":{"passed":5,"total":5}}}]`) // untested-model 不在表里

	r := New(cfg, WithClient(http.DefaultClient), WithAdmission(tab))
	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"untested-model"}`),
		Kind: provider.NonStreaming, Model: "untested-model",
	})
	if err != nil {
		t.Fatalf("字面模型不该被准入表否决: %v", err)
	}
	// ⚠️ 断言的是"没被拦下"，不是模型名 —— 字面模型时 res.Model 按设计为空
	// （Router 的注释：虚拟模型才回填实际模型 id，字面模型 body 原样透传）。
	if res.ProviderID != "p" {
		t.Errorf("字面模型应被正常路由到 p，got ProviderID=%q", res.ProviderID)
	}
}
