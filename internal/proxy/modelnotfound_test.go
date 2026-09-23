package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"moretoken/internal/config"
	"moretoken/internal/router"
)

// 字面模型打不中时，必须说**客户端认得的形状**。
//
// 从 Claude Code 二进制里挖到的映射：
//
//	`not_found_error` + message 以 `model: ` 开头  →  `model_not_found`
//	→ 显示「模型可能不存在或你没有权限，运行 /model 换一个」
//
// **这句话人可以直接照做。** 而回 503 是"服务端暂时问题，稍后重试"——
// 会把"改一行模型名"变成"无限重试直到会话卡死关停"。
//
// 2026-09-22 实测现场：某会话的 5 个子会话被指定了 `claude-opus-4-8[1m]`（池里没有 opus），
// 它们拿到的正是 503。

func literalModelServer(t *testing.T, upstreamStatus int, upstreamBody string) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(upstreamStatus)
		io.WriteString(w, upstreamBody)
	}))
	t.Cleanup(up.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "up", BaseURL: up.URL, Format: config.FormatAnthropic, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "agnes-2.5-flash"}}},
	}}
	srv := NewServer(router.New(cfg, router.WithClient(http.DefaultClient)), cfg, NewDecisionLog(8))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postLiteral(t *testing.T, ts *httptest.Server, model string) (int, string) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"`+model+`","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestEndpoint_LiteralModelNotFound_Returns404Not503 —— 核心契约。
//
// 上游把它挂在 **503** 上（agnes 对未知模型就是这样），我们**仍然要回 404**——
// 因为"模型不存在"不是服务端暂时故障，是请求本身不可满足。
//
// 【反证】把 proxy 里 ErrModelNotFound 那个分支删掉 ⇒ 落到 503 分支 ⇒ 本条变红。
func TestEndpoint_LiteralModelNotFound_Returns404Not503(t *testing.T) {
	ts := literalModelServer(t, http.StatusServiceUnavailable,
		`{"error":{"type":"503","message":"model_not_found: unknown model"}}`)

	code, body := postLiteral(t, ts, "claude-opus-4-8[1m]")

	if code == http.StatusServiceUnavailable {
		t.Fatal("回 503 = 告诉客户端「稍后重试」——那正是 2026-09-22 五个子会话卡死关停的原因")
	}
	if code != http.StatusNotFound {
		t.Errorf("status = %d, want 404（客户端的 not_found_error 映射靠它）", code)
	}
	if !strings.Contains(body, `"type":"not_found_error"`) {
		t.Errorf("body 必须是 not_found_error 形状，实际 %s", body)
	}
	// message 必须以 `model: ` 开头——客户端就是靠这个前缀归类成 model_not_found 的
	if !strings.Contains(body, `"message":"model: claude-opus-4-8[1m]"`) {
		t.Errorf("message 必须是 `model: <名字>`，客户端靠这个前缀识别，实际 %s", body)
	}
}

// TestEndpoint_LiteralModelNotFound_Upstream404Too —— 上游回 404 时同样归一。
//
// 同一件事上游有时回 404（我们原本透传，客户端**碰巧**能认）、有时回 503
// （我们当可重试 ⇒ 耗尽 ⇒ 回 503 ⇒ 客户端重试）。**归一之后两种都一样。**
func TestEndpoint_LiteralModelNotFound_Upstream404Too(t *testing.T) {
	ts := literalModelServer(t, http.StatusNotFound, `{"error":{"message":"Model \"nope\" does not exist"}}`)

	code, body := postLiteral(t, ts, "nope")
	if code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
	if !strings.Contains(body, `"type":"not_found_error"`) ||
		!strings.Contains(body, `"message":"model: nope"`) {
		t.Errorf("归一后的形状不对：%s", body)
	}
}

// TestEndpoint_VirtualModelGone_StillRotates —— **虚拟模型不能走这条短路**。
//
// 虚拟模型的模型名由我们解析：上游撤了其中一个，该换同 provider 的下一个（T-018），
// 而不是宣布"模型不存在"。这条防的是"把字面模型的规则误伤到虚拟模型上"——
// 2026-09-22 加这个功能时就真误伤过一次，被 TestRoute_ModelGone404_* 抓出来。
func TestEndpoint_VirtualModelGone_StillRotates(t *testing.T) {
	var hits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"model":"gone"`) {
			http.Error(w, `{"error":{"message":"Model \"gone\" does not exist"}}`, http.StatusNotFound)
			return
		}
		io.WriteString(w, `{"content":"ok"}`)
	}))
	t.Cleanup(up.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "up", BaseURL: up.URL, Format: config.FormatAnthropic, Tier: config.TierFree,
			Keys: []string{"k"},
			Models: []config.Model{
				{ID: "gone", Kinds: []string{"general"}},
				{ID: "alive", Kinds: []string{"general"}},
			}},
	}}
	srv := NewServer(router.New(cfg, router.WithClient(http.DefaultClient)), cfg, NewDecisionLog(8))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"auto:general","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("虚拟模型遇到某个模型被撤，应换下一个而不是失败：status=%d %s", resp.StatusCode, b)
	}
	if hits < 2 {
		t.Errorf("上游命中 %d 次，应至少 2（先试 gone 再试 alive）", hits)
	}
}
