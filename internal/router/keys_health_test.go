package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"moretoken/internal/config"
	"moretoken/internal/provider"
)

// TestHealth_KeysAreRealCountNotRawCount —— /health 的 keys 必须报**池内真实可用的**
// key 数，而不是配置里的原始条数。
//
// 旧实现返回 len(op.pv.Keys)（含未解析占位符），于是"6 把占位符全没解析、池实际 0 把、
// 请求全 503"时它照样报 keys:3——对 WorkBoard 撒谎（契约明文写的是"无 env: 占位"）。
func TestHealth_KeysAreRealCountNotRawCount(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{{
		ID: "mixed", BaseURL: "http://x", Format: config.FormatOpenAI, Tier: config.TierFree,
		// 2 把真实 + 3 把各种占位（env 未设置、vault 未解析、裸前缀）
		Keys: []string{"sk-real-1", "env:FTA_NOT_SET_XYZ", "vault:no/such", "env:", "sk-real-2"},
	}}}
	r := New(cfg, WithClient(http.DefaultClient))

	h := r.Health()
	if len(h) != 1 {
		t.Fatalf("health len = %d, want 1", len(h))
	}
	if h[0].Keys != 2 {
		t.Errorf("Keys = %d, want 2（只有 2 把真实 key）", h[0].Keys)
	}
	if h[0].Unresolved != 3 {
		t.Errorf("Unresolved = %d, want 3（env 未设置 + vault 未解析 + 裸 env:）", h[0].Unresolved)
	}
}

// TestRouter_VaultPlaceholderNeverSentUpstream —— vault: 占位符不得进池。
//
// 若漏了（只认 "env:"），占位符会被当真 key 发上游：
// `Authorization: Bearer vault:freellm/...` → 401。
func TestRouter_VaultPlaceholderNeverSentUpstream(t *testing.T) {
	var sawAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = append(sawAuth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{{
		ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
		Keys: []string{"vault:unresolved/path", "env:", "sk-good"},
	}}}
	r := New(cfg, WithClient(http.DefaultClient))

	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{}`), Kind: provider.NonStreaming,
	})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	for _, a := range sawAuth {
		if strings.Contains(a, "vault:") || strings.Contains(a, "env:") || strings.TrimSpace(a) == "Bearer" {
			t.Errorf("占位符被发上游了: %q", a)
		}
	}
	if len(sawAuth) != 1 || sawAuth[0] != "Bearer sk-good" {
		t.Errorf("auth = %v, want [Bearer sk-good]", sawAuth)
	}
}

// TestRouter_DeadKey_IsolatesAndRotates —— 401/403 必须换池内下一把，不得透传给客户端。
//
// 这是多 key 轮换的核心承诺。旧实现把 401 归入 FailNone（"4xx 透传"），
// 路由判 done=true → 401 原样甩给客户端 → **一个失效的 key 打死整个池**
// （台账里 xkiro 的失效信号正是 `Invalid or disabled ClientApiKey`）。
func TestRouter_DeadKey_IsolatesAndRotates(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if strings.HasSuffix(r.Header.Get("Authorization"), "sk-dead") {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"Invalid or disabled ClientApiKey"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{{
		ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
		Keys: []string{"sk-dead", "sk-alive-a", "sk-alive-b"},
	}}}
	r := New(cfg, WithClient(http.DefaultClient))

	// 第一次：撞上死 key → 必须换到活 key，返回 200 而不是 401。
	res, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{}`), Kind: provider.NonStreaming,
	})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, want 200（死 key 应被隔离，不该把 401 透传给客户端）", res.StatusCode)
	}

	// 死 key 已被长禁：后续请求不应再撞它。
	before := atomic.LoadInt32(&hits)
	if _, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{}`), Kind: provider.NonStreaming,
	}); err != nil {
		t.Fatalf("second route: %v", err)
	}
	if got := atomic.LoadInt32(&hits) - before; got != 1 {
		t.Errorf("第二次请求上游命中 %d 次，want 1（死 key 不该被再次选中）", got)
	}
	if h := r.Health(); h[0].Cooling != 1 {
		t.Errorf("Cooling = %d, want 1（死 key 应在冷却/长禁中）", h[0].Cooling)
	}
}

// TestRouter_AllKeysDead_Exhausts —— 全池 key 都失效时按"池内耗尽"落到下一候选/503，
// 而不是把某一把的 401 当作正常响应返回。
//
// 判据用的是 **401**（凭据无效），不是 403。2026-09-20 两个上游实测：
//
//	好 key + 好模型 → 200 ／ 好 key + 无权模型 → 403 ／ 垃圾 key → 401
//
// 403 是"这个账号对这个模型没权限"，凭据是好的——它按**模型级**处理，不动 key。
// 本条测试原先用 403 断言"两把都被长禁"，编码的正是被实测推翻的那条假设；
// 改用 401 之后，它测的才真是自己注释里写的"全池 key 都失效"。
func TestRouter_AllKeysDead_Exhausts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{{
		ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
		Keys: []string{"sk-1", "sk-2"},
	}}}
	r := New(cfg, WithClient(http.DefaultClient))

	_, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatOpenAI, Body: []byte(`{}`), Kind: provider.NonStreaming,
	})
	if err == nil {
		t.Fatal("全池 key 被拒时应返回错误（调用方转 503），而不是把 403 当成功")
	}
	if h := r.Health(); h[0].Cooling != 2 {
		t.Errorf("Cooling = %d, want 2（两把都被长禁）", h[0].Cooling)
	}
}
