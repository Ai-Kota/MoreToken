package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"moretoken/internal/config"
	"moretoken/internal/provider"
)

// 模型维度轮换的契约（T-018）。
//
// 起因：准备做"从上游目录自动纳新"时发现——**纳进来的模型用不上**。
// `tryCandidate` 在 key 循环**之前**解析一次模型（`ResolveModel` 取第一个匹配 kind 的），
// 然后只用它轮换 3 把 key；同一个 provider 的其它模型**永远不会被尝试**。
//
// 后果：池子的维度是 (provider × key)，**模型维度是死的**。
// 于是"往 config 里加 24 个免费模型"这件事的净收益是 **0**——
// 除非 provider 被整个换掉，那些模型一行服务流量都不会承担。
//
// 这条契约要求：模型与 key 一样是**可轮换**的资源——
// 首选模型失败（可重试类）时，同 provider 的次选模型应当顶上来。

// TestRoute_RotatesModelsWithinProvider —— 首选模型挂 → 次选模型顶上（同一个 provider 内）。
//
// 【当前为红】——这正是 T-018 要修的东西。红了才说明"纳新"不是装饰。
func TestRoute_RotatesModelsWithinProvider(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		m := ModelOf(body)
		mu.Lock()
		calls[m]++
		mu.Unlock()
		if m == "model-primary" {
			w.WriteHeader(http.StatusBadGateway) // 首选模型的上游挂了
			return
		}
		w.Write([]byte(`{"content":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "p", BaseURL: srv.URL, Format: "anthropic", Tier: "free",
			Keys: []string{"k"},
			Models: []config.Model{
				{ID: "model-primary", Kinds: []string{"general"}},
				{ID: "model-backup", Kinds: []string{"general"}}, // 同一个 provider 的次选
			}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic,
		Body:   []byte(`{"model":"auto:general","messages":[]}`),
		Kind:   provider.NonStreaming, VirtualModel: true, WantKind: "general",
	})
	if err != nil {
		t.Fatalf("首选模型挂了，同 provider 还有次选，不该整体失败：%v", err)
	}
	if res.ProviderID != "p" {
		t.Fatalf("provider = %q, want p（不该换 provider，先换模型）", res.ProviderID)
	}
	if res.Model != "model-backup" {
		t.Errorf("落地模型 = %q, want model-backup", res.Model)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls["model-backup"] == 0 {
		t.Errorf("次选模型一次都没被调用 —— 模型维度是死的，纳新等于装饰（调用分布 %v）", calls)
	}
}

// TestRoute_ModelDenied403_RotatesWithoutBurningKeys —— 403 按**模型级**处理。
//
// 实测判据（2026-09-20，agnes 与 xkiro 两个上游一致）：
//
//	好 key + 好模型 → 200 ／ 好 key + 无权模型 → 403 ／ 垃圾 key → 401
//
// 403 常常是"这个账号对这个模型没权限"，**凭据是好的**。
// 二者曾同归 FailAuth ⇒ 路由对 403 也 RecordDead（长禁该 key 1 小时）⇒
// 一个无权模型进 config 就会把该 provider 的 key 逐个禁光、整层下线一小时。
// 期望行为：换同 provider 的下一个模型，**一把 key 都不受损**。
//
// 【反证】把 provider.Invoke 里 403 那条分支去掉（重归 FailAuth）⇒ 本条的
// cooling 断言变红，且请求会整体失败。
func TestRoute_ModelDenied403_RotatesWithoutBurningKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		if ModelOf(body) == "denied" {
			http.Error(w, `{"error":"no permission for this model"}`, http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"content":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{{
		ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
		Keys: []string{"k1", "k2"},
		Models: []config.Model{
			{ID: "denied", Kinds: []string{"general"}},
			{ID: "alive", Kinds: []string{"general"}},
		},
	}}}
	r := New(cfg, WithClient(http.DefaultClient))

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{"model":"auto:general","messages":[]}`),
		Kind: provider.NonStreaming, VirtualModel: true, WantKind: "general",
	})
	if err != nil {
		t.Fatalf("403 是模型级事实，不该让请求失败：%v", err)
	}
	if res.Model != "alive" {
		t.Errorf("落地模型 = %q, want alive", res.Model)
	}
	if h := r.Health(); h[0].Cooling != 0 {
		t.Errorf("Cooling = %d, want 0 —— 403 不该烧 key（凭据明明是好的）", h[0].Cooling)
	}
}

// TestRoute_ModelGone404_RotatesAndDoesNotLeakToClient —— "上游不再提供某模型"的原生形态。
//
// 上游回 404 时，**不许把 404 甩给调用方**（今天 15:30 的 xkiro 两条 404 透传就是这个形态），
// 也不许整层 fallback 到别的 provider（那会白白绕开一个还能用的服务商）：
// 先在同一个 provider 里换模型。
func TestRoute_ModelGone404_RotatesAndDoesNotLeakToClient(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		m := ModelOf(body)
		mu.Lock()
		calls[m]++
		mu.Unlock()
		if m == "gone" {
			http.Error(w, `{"error":{"message":"Model \"gone\" does not exist"}}`, http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"content":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "p", BaseURL: srv.URL, Format: "anthropic", Tier: "free",
			Keys: []string{"k"},
			Models: []config.Model{
				{ID: "gone", Kinds: []string{"general"}},
				{ID: "alive", Kinds: []string{"general"}},
			}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic,
		Body:   []byte(`{"model":"auto:general","messages":[]}`),
		Kind:   provider.NonStreaming, VirtualModel: true, WantKind: "general",
	})
	if err != nil {
		t.Fatalf("404 是模型级事实，不该让整个请求失败：%v", err)
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d，404 不该透传给调用方", res.StatusCode)
	}
	if res.Model != "alive" {
		t.Errorf("落地模型 = %q, want alive", res.Model)
	}
}

// TestRoute_VirtualModel_LastModel404_MustNotLeak —— 判据钉住（T-031，**对旧代码为红**）。
//
// 起因（实测，2026-10-05）：cc-ft 会话弹
// 「There's an issue with the selected model (auto:reasoning)…Run /model」。
// 来源 `/decisions`（400 条）：`auto:reasoning` 有 3 条
// `provider=xkiro-anthropic status=404 model=minimax/minimax-m3:free`。
//
// 机制（读码确认，**非推断**）——`tryCandidate`：
//
//	if fail.Kind == provider.FailNone && res.StatusCode == 404 && mi+1 < len(models) {
//	    break // 还有下一个模型 → 在同一 provider 内换模型
//	}
//	return res, fail, true, m // 没有下一个模型 → done=true，把 404 当完成态上抛
//
// 两个关键事实（**已由下方对照测试实测，非推断**）：
//
//	① 有下一个模型（`mi+1 < len`）时，**任何** 404 都轮换——判据看的是**状态码**，
//	   与 body 措辞无关。
//	② 走到**最后一个**模型时，**是否终止**取决于该 404 被归成哪一类：
//	     FailNone（body 没被 marker 认出，`:567` 分支）→ `done=true` → 就地终止、泄漏；
//	     FailModelNotFound（body 被 marker 认出，`:606`）→ `done=false` → 沿候选链继续。
//
// 于是本缺陷的**精确成因**：`isModelNotFound` 的 marker 全为 OpenAI 味措辞
// （`does not exist` / `unknown model` …，见 provider.go:137-144），**不含 Anthropic 规范的
// `not_found_error` 形状** ⇒ 上游那道规范 404 归 `FailNone` ⇒ 就地终止 ⇒ 泄漏。
// 对照测试 TestRoute_VirtualModel_RecognizedNotFound_FallsThroughToNextProvider 用**同一场景、
// 仅把 body 换成含 marker 的措辞**即变绿——这就是判据。
//
// 现实成因：xkiro-anthropic 的 reasoning 只有 `minimax/minimax-m3:free` 一个，且已被上游退役
// （config/models.auto.json 里 xkiro 已无 m3）；它对已退役模型回的正是 Anthropic 规范 404。
//
// 契约：上游对**我方解析出的**模型回 404，是**模型级事实**，不是客户端的事实。对虚拟模型，
// 它绝不能成为面向客户端的最终答复——应继续沿候选链走（换模型 / 换 provider）；确实无路时
// 给一个**网关侧**的失败，而不是把锅甩给一个用户从未指定的模型名。
//
// 本测试即最小复现：provider primary 只有一个 reasoning 模型且它 404，provider backup 健康。
// 修前：primary 的 404 直接 done=true，backup 一次都没被试（红）。修后：落到 backup（200）。
//
// 【T-032 已修，转绿】修法：`isModelNotFound` 按 **JSON 结构**识别 Anthropic 规范的
// `not_found_error` ⇒ 该 404 归 `FailModelNotFound` ⇒ 无下一个模型时返回 `done=false`
// （router.go:606）⇒ 沿候选链继续。供给冗余与模型级 de-admission 为纵深防御（另立任务）。
func TestRoute_VirtualModel_LastModel404_MustNotLeak(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		m := ModelOf(body)
		mu.Lock()
		calls[m]++
		mu.Unlock()
		if m == "gone" {
			// 上游对已退役模型的规范答复（Anthropic 协议端点常见形状）。
			http.Error(w, `{"type":"error","error":{"type":"not_found_error","message":"model: gone"}}`,
				http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"content":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "primary", BaseURL: srv.URL, Format: "anthropic", Tier: "free",
			Keys:   []string{"k"},
			Models: []config.Model{{ID: "gone", Kinds: []string{"reasoning"}}}}, // 仅此一个，且已退役
		{ID: "backup", BaseURL: srv.URL, Format: "anthropic", Tier: "free",
			Keys:   []string{"k"},
			Models: []config.Model{{ID: "alive", Kinds: []string{"reasoning"}}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic,
		Body:   []byte(`{"model":"auto:reasoning","messages":[]}`),
		Kind:   provider.NonStreaming, VirtualModel: true, WantKind: "reasoning",
	})
	if err != nil {
		t.Fatalf("换个 provider 就有可用模型，不该整体失败：%v", err)
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d —— 上游对某个具体模型的 404 被透传给调用方了（客户端会渲染成 auto:reasoning 不存在）", res.StatusCode)
	}
	if res.ProviderID != "backup" {
		t.Errorf("provider = %q, want backup —— 首选没得换时应沿候选链继续，而不是就地终止", res.ProviderID)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls["alive"] == 0 {
		t.Errorf("备选 provider 一次都没被调用 —— 一个候选的 404 终止了整条链（调用分布 %v）", calls)
	}
}

// TestRoute_VirtualModel_RecognizedNotFound_FallsThroughToNextProvider —— 对照实验（T-031）。
//
// 与上条唯一差别：上游 404 的 body **含 marker**（`does not exist`）⇒ 归类 FailModelNotFound。
// 于是 tryCandidate 走到「没有下一个模型」那句时，返回的是 `done=false`（router.go:606）
// ⇒ Route **继续沿候选链**到下一个 provider ⇒ 不泄漏。
//
// 两条合起来才划出本缺陷的**精确边界**：
//   - 有下一个模型（`mi+1 < len`）时，**任何** 404 都轮换（判据看状态码）——与 body 措辞无关；
//   - 走到**最后一个**模型时，**是否终止**取决于该 404 被归成 `FailModelNotFound`
//     还是 `FailNone`——**即取决于 body 能否被 marker 认出**。这才是本缺陷的成因。
func TestRoute_VirtualModel_RecognizedNotFound_FallsThroughToNextProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		if ModelOf(body) == "gone" {
			http.Error(w, `{"error":{"message":"Model \"gone\" does not exist"}}`, http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"content":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "primary", BaseURL: srv.URL, Format: "anthropic", Tier: "free",
			Keys: []string{"k"}, Models: []config.Model{{ID: "gone", Kinds: []string{"reasoning"}}}},
		{ID: "backup", BaseURL: srv.URL, Format: "anthropic", Tier: "free",
			Keys: []string{"k"}, Models: []config.Model{{ID: "alive", Kinds: []string{"reasoning"}}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic,
		Body:   []byte(`{"model":"auto:reasoning","messages":[]}`),
		Kind:   provider.NonStreaming, VirtualModel: true, WantKind: "reasoning",
	})
	if err != nil {
		t.Fatalf("不该整体失败：%v", err)
	}
	if res.ProviderID != "backup" || res.StatusCode != 200 {
		t.Errorf("provider=%q status=%d，want backup/200 —— 已命中的 404 应沿候选链继续",
			res.ProviderID, res.StatusCode)
	}
}
