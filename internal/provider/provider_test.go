package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// ── BuildURL ──

func TestBuildURL(t *testing.T) {
	cases := []struct {
		base, format, want string
	}{
		{"https://api.xkiro.com/v1", "openai", "https://api.xkiro.com/v1/chat/completions"},
		{"https://api.deepseek.com/anthropic", "anthropic", "https://api.deepseek.com/anthropic/v1/messages"},
		// 历史写法：base_url 已含端点名，不重复拼接
		{"https://api.deepseek.com/chat/completions", "openai", "https://api.deepseek.com/chat/completions"},
		{"https://open.bigmodel.cn/api/anthropic/v1/messages", "anthropic", "https://open.bigmodel.cn/api/anthropic/v1/messages"},
		// 尾部斜杠归一
		{"https://x.com/v1/", "openai", "https://x.com/v1/chat/completions"},
	}
	for _, c := range cases {
		got, err := BuildURL(c.base, c.format)
		if err != nil {
			t.Fatalf("BuildURL(%q): %v", c.base, err)
		}
		if got != c.want {
			t.Errorf("BuildURL(%q, %q) = %q, want %q", c.base, c.format, got, c.want)
		}
	}
}

// ── 非流式调用 ──

func TestInvoke_NonStreaming_Success(t *testing.T) {
	var gotAuth, gotCT, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		body := readAll(t, r)
		gotModel = extractModel(t, body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	client := &http.Client{}
	res, fail := Invoke(context.Background(), client, Request{
		Format:  "openai",
		BaseURL: srv.URL + "/v1",
		APIKey:  "sk-test",
		Model:   "agnes-2.5-flash",
		Body:    []byte(`{"model":"agnes-2.5-flash"}`),
		Kind:    NonStreaming,
	})
	if fail.Kind != FailNone {
		t.Fatalf("fail = %v, want none", fail)
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(string(res.Body), `"ok":true`) {
		t.Errorf("body = %s", res.Body)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth = %q, want Bearer sk-test", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotModel != "agnes-2.5-flash" {
		t.Errorf("model = %q", gotModel)
	}
}

// TestRetry_StreamStarted_NoReplay —— 矩阵 F7 时序/边界（I7）：
// 流式已发首块后上游断流 → FailStreamMid，且 IsRetryable()==false。
func TestRetry_StreamStarted_NoReplay(t *testing.T) {
	var streamStart func() // 控制"发 2 块后断流"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fl.Flush()
		// 发 2 块 data 就"断流"（handler 返回 = 上游关闭连接）
		io.WriteString(w, "data: {\"chunk\":1}\n\n")
		fl.Flush()
		io.WriteString(w, "data: {\"chunk\":2}\n\n")
		fl.Flush()
		streamStart() // 标记首块已发出
	}))
	defer srv.Close()

	var chunks int32
	_, fail := Invoke(context.Background(), &http.Client{}, Request{
		Format:  "openai",
		BaseURL: srv.URL + "/v1",
		APIKey:  "sk-test",
		Kind:    Streaming,
		StreamCB: func(c []byte) { atomic.AddInt32(&chunks, 1) },
	})
	if fail.Kind != FailStreamMid {
		t.Fatalf("fail kind = %v, want FailStreamMid (stream started then cut)", fail.Kind)
	}
	if fail.IsRetryable() {
		t.Error("stream-mid fail must NOT be retryable (I7: tool-call 不重复执行)")
	}
	if atomic.LoadInt32(&chunks) < 1 {
		t.Error("expected at least 1 streamed chunk before cutoff")
	}
}

// TestInvoke_429_Classified —— 矩阵 F2 错误（I8）：429 → FailRateLimit，可重放，且 key 应冷却。
func TestInvoke_429_Classified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	res, fail := Invoke(context.Background(), &http.Client{}, Request{
		Format: "openai", BaseURL: srv.URL + "/v1", APIKey: "sk", Kind: NonStreaming,
	})
	if fail.Kind != FailRateLimit {
		t.Fatalf("fail = %v, want FailRateLimit", fail.Kind)
	}
	if !fail.IsRetryable() {
		t.Error("429 must be retryable (before response bytes)")
	}
	if res.StatusCode != 429 {
		t.Errorf("status = %d, want 429", res.StatusCode)
	}
	if !strings.Contains(string(res.Body), "rate limited") {
		t.Errorf("body = %s", res.Body)
	}
}

// TestInvoke_5xx_Classified —— 矩阵 F2 错误：5xx → FailUpstream，可重放。
func TestInvoke_5xx_Classified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `upstream down`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, fail := Invoke(context.Background(), &http.Client{}, Request{
		Format: "openai", BaseURL: srv.URL + "/v1", APIKey: "sk", Kind: NonStreaming,
	})
	if fail.Kind != FailUpstream {
		t.Fatalf("fail = %v, want FailUpstream", fail.Kind)
	}
	if !fail.IsRetryable() {
		t.Error("5xx must be retryable")
	}
}

// TestInvoke_TransportError —— 连接错误 → FailTransport，可重放。
func TestInvoke_TransportError(t *testing.T) {
	_, fail := Invoke(context.Background(), &http.Client{}, Request{
		Format: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "sk", Kind: NonStreaming,
	})
	if fail.Kind != FailTransport {
		t.Fatalf("fail = %v, want FailTransport", fail.Kind)
	}
}

// TestInvoke_AnthropicHeaders —— anthropic 格式带 anthropic-version 头。
func TestInvoke_AnthropicHeaders(t *testing.T) {
	var gotVer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVer = r.Header.Get("anthropic-version")
		w.Write([]byte(`{"content":[]}`))
	}))
	defer srv.Close()

	Invoke(context.Background(), &http.Client{}, Request{
		Format: "anthropic", BaseURL: srv.URL + "/anthropic", APIKey: "sk", Kind: NonStreaming,
	})
	if gotVer != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", gotVer)
	}
}

// ── ProbeBody ──

// TestProbeBody_CarriesRealModel 探测体必须原样携带调用方给的模型名。
//
// 曾经的实现硬编码假名 "m"，上游一律拒（agnes 503 model_not_found / xkiro 404
// model does not exist）→ 探测恒不健康 → 状态机进了付费层就再也回不到免费。
func TestProbeBody_CarriesRealModel(t *testing.T) {
	// 含 ":" 的照抄（xkiro 的模型 ID 就长这样），不能被当成别的东西处理。
	for _, model := range []string{"agnes-2.5-flash", "minimax/minimax-m3:free"} {
		body := ProbeBody(model)
		if got := extractModel(t, string(body)); got != model {
			t.Errorf("ProbeBody(%q) 里的 model = %q, want 原样携带", model, got)
		}
		if bytes.Contains(body, []byte(`"model":"m"`)) {
			t.Errorf("ProbeBody(%q) 里仍有硬编码假名 m", model)
		}
	}
}

// TestProbeBody_ValidForBothFormats 同一形态要同时满足两个格式的硬要求：
// openai 要 model 非空，anthropic 要 max_tokens 显式声明。
func TestProbeBody_ValidForBothFormats(t *testing.T) {
	var probe struct {
		Model     string              `json:"model"`
		MaxTokens *int                `json:"max_tokens"`
		Messages  []map[string]string `json:"messages"`
	}
	if err := json.Unmarshal(ProbeBody("agnes-2.5-flash"), &probe); err != nil {
		t.Fatalf("探测体不是合法 JSON: %v", err)
	}
	if probe.Model == "" {
		t.Error("model 为空（openai 格式要求非空）")
	}
	if probe.MaxTokens == nil {
		t.Error("缺 max_tokens（anthropic 格式要求显式声明）")
	} else if *probe.MaxTokens != 1 {
		t.Errorf("max_tokens = %d, want 1（探测要近零成本）", *probe.MaxTokens)
	}
	if len(probe.Messages) != 1 || probe.Messages[0]["content"] == "" {
		t.Errorf("messages = %v, want 一条非空消息", probe.Messages)
	}
}

// ── 辅助 ──

func readAll(t *testing.T, r *http.Request) string {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func extractModel(t *testing.T, body string) string {
	t.Helper()
	// 最小 JSON 提取，避免引入编码/json 到辅助里（够测试用）
	if i := strings.Index(body, `"model":"`); i >= 0 {
		rest := body[i+len(`"model":"`):]
		if j := strings.Index(rest, `"`); j >= 0 {
			return rest[:j]
		}
	}
	return ""
}

var _ = io.WriteString // sse 测试用
