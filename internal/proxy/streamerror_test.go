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

// T-027 契约：**流式请求的路由错误必须把真实状态码送上 wire**。
//
// 缺陷（修复前）：streaming 在 Route 之前就 Flush SSE 头 ⇒ 200 已上线 ⇒
// 后续 404/400/503 的 http.Error 变 superfluous WriteHeader ⇒ 客户端
// （Claude Code 100% 流式）收到 `200 + SSE 头 + 裸错误 JSON` 的破碎流，
// T-023 的 /compact、/model 提示全部失效。
//
// 证据：2026-09-29 容器日志 10 分钟 3 次 `superfluous response.WriteHeader
// … proxy.go:307`（400 超限路径），全部来自用户真实 cc-ft 会话。
//
// 这组测试在**修复前必须红**（红 = 缺陷存在证明），修复后绿；
// 变异检验 = 回贴早 flush，1-3 号用例必须复红。

// postStream 发 stream:true 请求，返回 wire 上的真实 (状态码, Content-Type, body)。
// 用 httptest.NewServer（真 TCP）而非 ResponseRecorder——flush/wire 语义与生产一致。
func postStream(t *testing.T, url, body string) (int, string, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
}

// streamServer 单 provider（openai 格式、模型 "m"）+ 可定制上游行为的端到端服务器。
func streamServer(t *testing.T, upstream http.HandlerFunc) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	cfg := &config.Config{Providers: []config.Provider{
		{ID: "up", BaseURL: up.URL, Format: config.FormatOpenAI, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
	}}
	srv := NewServer(router.New(cfg, router.WithClient(http.DefaultClient)), cfg, NewDecisionLog(8))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// ①流式 + 字面模型不存在 → wire 404（不是 200 破碎流）。
func TestStream_ModelNotFound_RealStatus404(t *testing.T) {
	ts := literalModelServer(t, http.StatusServiceUnavailable, `{"error":"unknown model"}`)
	code, ct, body := postStream(t, ts.URL+"/v1/messages",
		`{"model":"claude-opus-4-8[1m]","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusNotFound {
		t.Fatalf("wire 状态 = %d, want 404（修复前这里是 200+破碎流，superfluous WriteHeader）", code)
	}
	if !strings.Contains(body, "not_found_error") || !strings.Contains(body, "model: ") {
		t.Fatalf("404 body 应为 anthropic not_found_error 形状（客户端据此提示 /model）: %s", body)
	}
	if strings.Contains(ct, "text/event-stream") {
		t.Fatalf("错误路径 Content-Type 不得是 SSE（http.Error 应已覆盖）: %s", ct)
	}
}

// ②流式 + 上下文超限 → wire 400 + 客户端认得的句式（/compact 提示的前提）。
// 上游形态照抄 context_test.go（502 + ContextWindowExceededError 原文）。
func TestStream_ContextTooLong_RealStatus400(t *testing.T) {
	ts := streamServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway) // 判据只能看 body（同 context_test）
		io.WriteString(w, `ContextWindowExceededError: The input (994166 tokens) is longer than the model's context length (524288 tokens).`)
	})
	code, _, body := postStream(t, ts.URL+"/v1/chat/completions",
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("wire 状态 = %d, want 400（修复前 200+破碎流 ⇒ 客户端不会提示 /compact）", code)
	}
	if !clientContextRe.MatchString(body) {
		t.Fatalf("body 应含客户端认得的 `prompt is too long: N tokens > M` 句式: %s", body)
	}
}

// ③流式 + 池耗尽 → wire 503（重试语义的前提：503 才是"暂时故障可重试"）。
func TestStream_Exhausted_RealStatus503(t *testing.T) {
	ts := streamServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	})
	code, _, body := postStream(t, ts.URL+"/v1/chat/completions",
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("wire 状态 = %d, want 503", code)
	}
	if !strings.Contains(body, "exhausted") {
		t.Fatalf("503 body 应说明耗尽: %s", body)
	}
}

// ④流式成功回归：wire 行为与修复前逐字节同构（200 + SSE 头 + data 帧透传）。
// 修复只许改错误路径的成功率，不许动成功路径的形状。
func TestStream_Success_WireUnchanged(t *testing.T) {
	ts := streamServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "data: {\"type\":\"chunk\",\"n\":1}\n\n")
		io.WriteString(w, "data: {\"type\":\"chunk\",\"n\":2}\n\n")
	})
	code, ct, body := postStream(t, ts.URL+"/v1/chat/completions",
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK {
		t.Fatalf("成功流 wire 状态 = %d, want 200", code)
	}
	if ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(body, `data: {"type":"chunk","n":1}`) ||
		!strings.Contains(body, `data: {"type":"chunk","n":2}`) {
		t.Fatalf("两个 data 帧都应透传（provider 剥前缀、proxy 补回线格式）: %q", body)
	}
}

// ⑤流式空流（上游 200 零数据块）→ 显式状态码 + SSE 头（不靠 Go 隐式 200 空响应）。
func TestStream_EmptyStream_Explicit200(t *testing.T) {
	ts := streamServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK) // 零数据块
	})
	code, ct, _ := postStream(t, ts.URL+"/v1/chat/completions",
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK {
		t.Fatalf("空流 wire 状态 = %d, want 200", code)
	}
	if ct != "text/event-stream" {
		t.Fatalf("空流也应带 SSE 头（客户端在等流）: %q", ct)
	}
}
