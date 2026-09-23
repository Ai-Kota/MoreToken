package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIsContextTooLong_UsesUpstreamWording —— 判据用**上游原话**，不是猜的。
//
// 原文（agnes，2026-09-22 实测）：
//
//	ContextWindowExceededError: ... The input (994166 tokens) is longer than
//	the model's context length (524288 tokens).
func TestIsContextTooLong_UsesUpstreamWording(t *testing.T) {
	real := `{"error":{"type":"400","message":"***.ContextWindowExceededError: ***.BadRequestError: ContextWindowExceededError: OpenAIException - {\"message\":\"The input (994166 tokens) is longer than the model's context length (524288 tokens).\"}"},"type":"error"}`
	if !isContextTooLong([]byte(real)) {
		t.Error("agnes 的真实原文必须被认出来")
	}
	for _, s := range []string{
		`{"error":{"message":"This model's maximum context length is 8192 tokens"}}`,
		`{"error":{"code":"context_length_exceeded"}}`,
	} {
		if !isContextTooLong([]byte(s)) {
			t.Errorf("常见变体没认出来：%s", s)
		}
	}
	// 反证：无关错误**不能**被误判（误判 = 本该重试的失败被当成确定性失败）
	for _, s := range []string{
		`{"error":"rate limit"}`, `{"error":"model not found"}`, ``, `{"error":"internal"}`,
	} {
		if isContextTooLong([]byte(s)) {
			t.Errorf("无关错误被误判成上下文超限：%s", s)
		}
	}
}

// TestInvoke_UpstreamErrorCarriesBody —— 5xx 的 reason 必须**带上上游原文**。
//
// 血账（2026-09-22）：排查"会话卡很久"时，留痕里只有 `status 502`，
// 而 agnes 真正想说的是"上下文超限"——线索就是被这里丢掉的。
// 本仓排查手册原话：「顺手改写成'上游不可用'就把线索丢了」。
func TestInvoke_UpstreamErrorCarriesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, `{"error":"upstream exploded: disk full"}`)
	}))
	t.Cleanup(srv.Close)

	_, fail := Invoke(context.Background(), srv.Client(), Request{
		Format: "openai", BaseURL: srv.URL, APIKey: "k", Body: []byte("{}"), Kind: NonStreaming,
	})
	if fail.Kind != FailUpstream {
		t.Fatalf("kind = %v, want FailUpstream", fail.Kind)
	}
	if !strings.Contains(fail.Reason, "disk full") {
		t.Errorf("reason = %q，必须带上上游原文（否则唯一的方向性线索就丢了）", fail.Reason)
	}
}

// TestInvoke_ContextTooLongIsItsOwnKind —— 上下文超限要单独成类，且**不可重试**。
func TestInvoke_ContextTooLongIsItsOwnKind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway) // 注意：挂在 502 上，所以判据只能看 body
		io.WriteString(w, `ContextWindowExceededError: The input (994166 tokens) is longer than the model's context length (524288 tokens).`)
	}))
	t.Cleanup(srv.Close)

	_, fail := Invoke(context.Background(), srv.Client(), Request{
		Format: "openai", BaseURL: srv.URL, APIKey: "k", Body: []byte("{}"), Kind: NonStreaming,
	})
	if fail.Kind != FailContextTooLong {
		t.Fatalf("kind = %v, want FailContextTooLong（状态码是 502，判据必须看 body）", fail.Kind)
	}
	if fail.IsRetryable() {
		t.Error("上下文超限是确定性失败，**不可重试**——重试只会让调用方白等")
	}
	if fail.Kind.String() != "context_too_long" {
		t.Errorf("短名 = %q，它是留痕契约", fail.Kind.String())
	}
}
