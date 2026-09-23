package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"moretoken/internal/config"
	"moretoken/internal/router"
)

// 客户端（Claude Code）里那条判据是写死的正则——从它的二进制里挖出来的：
//
//	/prompt is too long[^0-9]*(\d+)\s*tokens?\s*>\s*(\d+)/i
//	→ {actualTokens, limitTokens}
//
// 网关必须回**这个句式**，客户端才会认出、提取 token 数、归到 "request too large"、
// **提示 /compact 而不是重试**。自造措辞（"context window exceeded" / "hint: 压缩会话"）
// 它一个字都不认——于是它当服务端故障继续重试，而这是个永远不会成功的请求。
var clientContextRe = regexp.MustCompile(`(?i)prompt is too long[^0-9]*(\d+)\s*tokens?\s*>\s*(\d+)`)

// TestEndpoint_ContextTooLong_SpeaksClientLanguage —— 回客户端认得的规范句式。
//
// 【反证】把 message 换回自造的 `{"error":"context window exceeded","hint":...}`
// ⇒ 本测试的正则匹配断言变红。
func TestEndpoint_ContextTooLong_SpeaksClientLanguage(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway) // 注意挂在 502 上——判据只能看 body
		io.WriteString(w, `ContextWindowExceededError: The input (994166 tokens) is longer than the model's context length (524288 tokens).`)
	}))
	t.Cleanup(up.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "up", BaseURL: up.URL, Format: config.FormatOpenAI, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
	}}
	srv := NewServer(router.New(cfg, router.WithClient(http.DefaultClient)), cfg, NewDecisionLog(8))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// ① 不能是 503 —— 它的字面含义是"服务端暂时问题，稍后重试"，会诱导重试
	if resp.StatusCode == http.StatusServiceUnavailable {
		t.Fatal("超限不能回 503：那会诱导智能体重试一个永远不会成功的请求")
	}
	// ② 必须是客户端认得的句式，且带上两个 token 数
	m := clientContextRe.FindStringSubmatch(string(body))
	if m == nil {
		t.Fatalf("回的不是客户端认得的句式（它只认 prompt is too long: N tokens > M），实际 %s", body)
	}
	if m[1] != "994166" || m[2] != "524288" {
		t.Errorf("token 数没正确提取：actual=%s limit=%s，want 994166 / 524288", m[1], m[2])
	}
	// ③ 上游原文要留着（给人排查用）
	if !strings.Contains(string(body), "ContextWindowExceeded") {
		t.Errorf("应保留上游原文供人排查，实际 %s", body)
	}
	// ④ 决策留痕
	if rec := srv.declog.Recent(0); len(rec) == 0 {
		t.Error("超限也必须进决策日志")
	}
}
