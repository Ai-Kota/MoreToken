package proxy

import (
	"context"
	"testing"
)

// TestClassifyOutcome_ClientCanceledIsNotGatewayFailure —— 2026-10-25 体检的直接判据：
// 客户端取消（context canceled）必须单独成类，不能记成网关 503 失败。
func TestClassifyOutcome_ClientCanceledIsNotGatewayFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := classifyOutcome(ctx, 503, ""); got != "client_canceled" {
		t.Errorf("取消请求 outcome = %q, want client_canceled", got)
	}
	if got := classifyOutcome(context.Background(), 503, "exhausted"); got != "exhausted" {
		t.Errorf("503 outcome = %q, want exhausted", got)
	}
	if got := classifyOutcome(context.Background(), 200, ""); got != "ok" {
		t.Errorf("200 outcome = %q, want ok", got)
	}
	if got := classifyOutcome(context.Background(), 413, ""); got != "request_too_large" {
		t.Errorf("413 outcome = %q, want request_too_large", got)
	}
}

func TestTruncateTel(t *testing.T) {
	if got := truncateTel("short"); got != "short" {
		t.Errorf("got %q", got)
	}
	long := make([]byte, 400)
	for i := range long {
		long[i] = 'x'
	}
	if got := truncateTel(string(long)); len(got) != 303 {
		t.Errorf("len = %d, want 303（300 字节 + UTF-8 省略号 3 字节）", len(got))
	}
}
